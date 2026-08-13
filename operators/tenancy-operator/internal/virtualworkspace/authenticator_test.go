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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/apis/apiserver"
	"k8s.io/apiserver/pkg/apis/apiserver/validation"
	authenticationcel "k8s.io/apiserver/pkg/authentication/cel"
)

// The mirror this server keeps of kcp's authenticator is asserted rather than
// trusted, because every way it can be wrong is silent: a caller kcp knows as
// `pm:x` and this server knows as `oidc:x` is not an error on either side.
func TestJWTAuthenticatorFor(t *testing.T) {
	base := OIDCOptions{
		IssuerURL:      "https://idp.example",
		ClientID:       "platform-mesh",
		UsernameClaim:  "email",
		UsernamePrefix: "pm:",
	}

	t.Run("groups are mapped with the configured prefix", func(t *testing.T) {
		opts := base
		opts.GroupsClaim = "groups"
		opts.GroupsPrefix = "pm:"

		jwt := jwtAuthenticatorFor(opts)

		assert.Equal(t, "groups", jwt.ClaimMappings.Groups.Claim)
		require.NotNil(t, jwt.ClaimMappings.Groups.Prefix)
		assert.Equal(t, "pm:", *jwt.ClaimMappings.Groups.Prefix)
	})

	// An empty prefix is a CHOICE and has to survive as one. Dropping it would
	// leave the prefix nil, which is how kcp spells "use the default", and kcp's
	// default is `oidc:` — so the value that means "no prefix" would arrive
	// meaning the opposite.
	t.Run("an empty groups prefix is still a prefix", func(t *testing.T) {
		opts := base
		opts.GroupsClaim = "groups"
		opts.GroupsPrefix = ""

		jwt := jwtAuthenticatorFor(opts)

		require.NotNil(t, jwt.ClaimMappings.Groups.Prefix)
		assert.Equal(t, "", *jwt.ClaimMappings.Groups.Prefix)
	})

	// No claim configured is a deployment whose issuer has no groups, not a
	// misconfiguration. It must produce NO mapping at all: an empty claim with a
	// non-nil prefix fails the authenticator's own validation, which would take
	// down a server that only meant to leave groups alone.
	t.Run("no claim leaves the mapping unset", func(t *testing.T) {
		jwt := jwtAuthenticatorFor(base)

		assert.Empty(t, jwt.ClaimMappings.Groups.Claim)
		assert.Nil(t, jwt.ClaimMappings.Groups.Prefix)
	})

	// Both shapes have to pass the same validation the apiserver applies at
	// startup, since that is what turns a bad mapping into a boot failure rather
	// than a runtime one.
	t.Run("every shape is a valid JWTAuthenticator", func(t *testing.T) {
		for name, opts := range map[string]OIDCOptions{
			"with groups":    withGroups(base, "groups", "pm:"),
			"empty prefix":   withGroups(base, "groups", ""),
			"without groups": base,
		} {
			t.Run(name, func(t *testing.T) {
				jwt := jwtAuthenticatorFor(opts)
				cfg := &apiserver.AuthenticationConfiguration{JWT: []apiserver.JWTAuthenticator{jwt}}
				errs := validation.ValidateAuthenticationConfiguration(
					authenticationcel.NewDefaultCompiler(), cfg, []string{})
				assert.Empty(t, errs, "%s", errs.ToAggregate())
			})
		}
	})
}

func withGroups(opts OIDCOptions, claim, prefix string) OIDCOptions {
	opts.GroupsClaim = claim
	opts.GroupsPrefix = prefix
	return opts
}
