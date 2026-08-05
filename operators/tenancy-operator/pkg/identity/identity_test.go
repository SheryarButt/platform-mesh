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

package identity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.platform-mesh.io/tenancy-operator/pkg/identity"

	"k8s.io/apimachinery/pkg/util/validation"
)

var claims = identity.Claims{
	Issuer:  "https://auth.example.com",
	Subject: "9c4b8e1f-1111-2222-3333-444455556666",
	Email:   "alice@acme.example",
	Name:    "Alice Doe",
}

// pm:alice@acme.example is what one deployment's configuration happens to
// produce, not a format this model owns. Each row is a different kcp setting.
func TestRBACIdentityMirrorsConfiguration(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  identity.Config
		want string
	}{
		"email claim with prefix": {
			cfg:  identity.Config{UsernameClaim: identity.ClaimEmail, UsernamePrefix: "pm:"},
			want: "pm:alice@acme.example",
		},
		"sub claim with prefix": {
			cfg:  identity.Config{UsernameClaim: identity.ClaimSub, UsernamePrefix: "pm:"},
			want: "pm:9c4b8e1f-1111-2222-3333-444455556666",
		},
		"no prefix": {
			cfg:  identity.Config{UsernameClaim: identity.ClaimEmail},
			want: "alice@acme.example",
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := identity.NewResolver(tc.cfg)
			require.NoError(t, err)

			got, err := r.RBACIdentity(claims)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// Guessing a username convention produces bindings that silently never match, so
// an unmirrorable configuration must fail at construction rather than at runtime.
func TestNewResolverRejectsUnmirrorableConfig(t *testing.T) {
	for name, claim := range map[string]identity.UsernameClaim{
		"unset":                  "",
		"unsupported claim":      "preferred_username",
		"a CEL-style expression": "claims.email + ':' + claims.sub",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := identity.NewResolver(identity.Config{UsernameClaim: claim})
			assert.Error(t, err)
		})
	}
}

func TestRBACIdentityRejectsEmptyClaim(t *testing.T) {
	r, err := identity.NewResolver(identity.Config{UsernameClaim: identity.ClaimEmail, UsernamePrefix: "pm:"})
	require.NoError(t, err)

	_, err = r.RBACIdentity(identity.Claims{Issuer: claims.Issuer, Subject: claims.Subject})
	assert.Error(t, err, "an empty claim must not silently produce the bare prefix as a username")
}

func TestMutable(t *testing.T) {
	email, err := identity.NewResolver(identity.Config{UsernameClaim: identity.ClaimEmail})
	require.NoError(t, err)
	assert.True(t, email.Mutable(), "email can change for a person who stays the same person")

	sub, err := identity.NewResolver(identity.Config{UsernameClaim: identity.ClaimSub})
	require.NoError(t, err)
	assert.False(t, sub.Mutable())
}

// The User name identifies a person. No claim convention affects it, which is why
// changing the username claim never orphans a User.
func TestUserNameIsIndependentOfClaimConfiguration(t *testing.T) {
	a, err := identity.UserName(claims.Issuer, claims.Subject)
	require.NoError(t, err)
	b, err := identity.UserName(claims.Issuer, claims.Subject)
	require.NoError(t, err)
	assert.Equal(t, a, b, "the name must be stable — self-provision relies on it colliding on AlreadyExists")

	other, err := identity.UserName("https://other.example.com", claims.Subject)
	require.NoError(t, err)
	assert.NotEqual(t, a, other, "the same sub at a different issuer is a different person")
}

// The separator must make the concatenation injective. With "/" it was not:
// ("https://a.example/b", "c") and ("https://a.example", "b/c") produced the same
// digest, silently merging two identities into one project. A newline cannot
// appear in an issuer URL or a sub, so the split point is unambiguous.
func TestUserNameSeparatorIsUnambiguous(t *testing.T) {
	x, err := identity.UserName("https://a.example/b", "c")
	require.NoError(t, err)
	y, err := identity.UserName("https://a.example", "b/c")
	require.NoError(t, err)
	assert.NotEqual(t, x, y, "distinct identities must not share a name")
}

// The name goes into metadata.name, so it has to be a valid RFC 1123 subdomain:
// full 64-char digest, lowercase hex, never truncated.
func TestUserNameIsAValidObjectName(t *testing.T) {
	n, err := identity.UserName(claims.Issuer, claims.Subject)
	require.NoError(t, err)
	assert.Len(t, n, 64, "the full sha256 digest, not the old 63-char label truncation")
	assert.Regexp(t, "^[0-9a-f]{64}$", n)
	assert.Empty(t, validation.IsDNS1123Subdomain(n))
}

func TestUserNameRequiresBothParts(t *testing.T) {
	_, err := identity.UserName("", claims.Subject)
	assert.Error(t, err)
	_, err = identity.UserName(claims.Issuer, "  ")
	assert.Error(t, err)
}
