/*
Copyright The Platform Mesh Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package virtualworkspace

import (
	"context"
	"fmt"
	"net/http"

	"go.platform-mesh.io/tenancy-operator/pkg/identity"

	"k8s.io/apiserver/pkg/apis/apiserver"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/group"
	"k8s.io/apiserver/pkg/authentication/request/bearertoken"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/server/dynamiccertificates"
	oidcauth "k8s.io/apiserver/plugin/pkg/authenticator/token/oidc"
)

// newAuthenticator builds the OIDC verifier and stashes the issuer and subject
// where the storage can read them.
//
// The claims matter as much as the verification: a User is keyed on
// sha256(issuer + "\n" + sub), so the storage needs the RAW issuer and subject,
// not the derived username. Recovering them by parsing the username would tie
// identity to a mutable claim, which is exactly what keying on the subject
// avoids — an email change would otherwise mint a second project.
func newAuthenticator(opts OIDCOptions) (authenticator.Request, error) {
	if opts.IssuerURL == "" || opts.ClientID == "" {
		return nil, fmt.Errorf(
			"--oidc-issuer-url and --oidc-client-id are required: the virtual workspace verifies tokens itself, " +
				"and must be configured with the same issuer as kcp")
	}

	jwt := jwtAuthenticatorFor(opts)

	// A private-CA or self-signed issuer is the normal case for a dev broker, so
	// the CA is configuration rather than an edge case.
	var ca dynamiccertificates.CAContentProvider
	if opts.CAFile != "" {
		var err error
		ca, err = dynamiccertificates.NewDynamicCAContentFromFile("oidc-ca", opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading the OIDC CA from %s: %w", opts.CAFile, err)
		}
	}

	tokenAuth, err := oidcauth.New(context.Background(), oidcauth.Options{
		JWTAuthenticator:     jwt,
		CAContentProvider:    ca,
		SupportedSigningAlgs: []string{"RS256", "ES256"},
	})
	if err != nil {
		return nil, fmt.Errorf("building the OIDC authenticator for issuer %s: %w", opts.IssuerURL, err)
	}

	return &claimsAuthenticator{delegate: group.NewAuthenticatedGroupAdder(bearertoken.New(tokenAuth))}, nil
}

// jwtAuthenticatorFor is the claim mapping alone, separated from the plumbing
// around it so the mirror can be asserted in a test.
func jwtAuthenticatorFor(opts OIDCOptions) apiserver.JWTAuthenticator {
	jwt := apiserver.JWTAuthenticator{
		Issuer: apiserver.Issuer{
			URL:       opts.IssuerURL,
			Audiences: []string{opts.ClientID},
		},
		ClaimMappings: apiserver.ClaimMappings{
			Username: apiserver.PrefixedClaimOrExpression{
				Claim:  opts.UsernameClaim,
				Prefix: &opts.UsernamePrefix,
			},
			// Carry the raw claims through as Extra so the storage can rebuild the
			// identity without re-parsing a token it never sees.
			//
			// This mapping is what makes that true. Without it the authenticator
			// emits only a username, and every request fails deep in the storage
			// with "authenticated token carries no issuer/subject" — for a token it
			// verified perfectly well.
			//
			// iss and sub are required of every JWT, so they are mapped directly.
			// email is optional in general (a deployment keying usernames on `sub`
			// need not have one), so it is guarded rather than assumed.
			Extra: []apiserver.ExtraMapping{
				{Key: ExtraIssuer, ValueExpression: "claims.iss"},
				{Key: ExtraSubject, ValueExpression: "claims.sub"},
				{Key: ExtraEmail, ValueExpression: `has(claims.email) ? claims.email : ""`},
				// The token's own `name` claim, NOT the mapped username. The username
				// carries the RBAC prefix (pm:), and using it as a display name leaks
				// that prefix into every UI string — "pm:dex@pm.localhost's personal"
				// rather than "dex's personal".
				{Key: ExtraName, ValueExpression: `has(claims.name) ? claims.name : ""`},
			},
		},
		UserValidationRules: nil,
	}

	// Groups, when the deployment has an issuer that carries them.
	//
	// Mapped the same way kcp maps them, because the two answers have to agree:
	// kcp evaluates a Group subject in a ClusterRoleBinding against the groups IT
	// extracted, while this server decides what the caller may see through the
	// tenancy API against the groups extracted HERE. A prefix set on one side only
	// does not fail — it silently splits one group into two, and the caller is
	// admitted by one plane and not the other.
	//
	// Left unset when there is no claim configured: an empty Claim with a non-nil
	// Prefix is rejected by the authenticator's own validation, so "no groups" has
	// to be the absence of the mapping rather than an empty one.
	//
	// Unlike the username claim, this one is OPTIONAL in the token. A token with no
	// `groups` claim authenticates and simply carries no groups, which is what lets
	// one issuer serve identities that are in groups and identities that are not.
	if opts.GroupsClaim != "" {
		jwt.ClaimMappings.Groups = apiserver.PrefixedClaimOrExpression{
			Claim:  opts.GroupsClaim,
			Prefix: &opts.GroupsPrefix,
		}
	}

	return jwt
}

// claimsAuthenticator decorates a successful authentication with the raw issuer
// and subject the tenancy model keys on.
type claimsAuthenticator struct {
	delegate authenticator.Request
}

func (a *claimsAuthenticator) AuthenticateRequest(req *http.Request) (*authenticator.Response, bool, error) {
	resp, ok, err := a.delegate.AuthenticateRequest(req)
	if err != nil || !ok {
		return resp, ok, err
	}

	// Read only the keys newAuthenticator's ClaimMappings actually write. These
	// used to also probe `authentication.kubernetes.io/...` first, which was both
	// dead — that prefix is reserved, and structured authentication forbids an
	// ExtraMapping from writing under it, so nothing ever landed there — and
	// backwards, since a hit would have outranked the mapping we configure
	// ourselves. One source, no precedence to reason about.
	extra := resp.User.GetExtra()
	claims := identity.Claims{
		Issuer:  firstOf(extra, ExtraIssuer),
		Subject: firstOf(extra, ExtraSubject),
		Email:   firstOf(extra, ExtraEmail),
		// Display name only, and empty when the token has no `name` claim. NOT
		// defaulted to the username: that carries the RBAC prefix, so it renders as
		// "pm:dex@pm.localhost's personal" wherever a display name is shown. The
		// seed reconciler already falls back — to spec.name, then spec.email, then
		// the User's name — using values that have no prefix on them.
		Name: firstOf(extra, ExtraName),
	}

	merged := map[string][]string{}
	for k, v := range extra {
		merged[k] = v
	}
	merged[ExtraIssuer] = []string{claims.Issuer}
	merged[ExtraSubject] = []string{claims.Subject}
	if claims.Email != "" {
		merged[ExtraEmail] = []string{claims.Email}
	}
	merged[ExtraName] = []string{claims.Name}

	resp.User = &user.DefaultInfo{
		Name:   resp.User.GetName(),
		UID:    resp.User.GetUID(),
		Groups: resp.User.GetGroups(),
		Extra:  merged,
	}
	return resp, true, nil
}

func firstOf(extra map[string][]string, keys ...string) string {
	for _, k := range keys {
		if v := extra[k]; len(v) > 0 && v[0] != "" {
			return v[0]
		}
	}
	return ""
}
