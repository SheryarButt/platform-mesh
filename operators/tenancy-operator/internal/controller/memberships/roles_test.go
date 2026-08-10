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

package memberships

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
)

// verbsFor returns the verbs a role grants on resources, ignoring the
// non-resource entry rules.
func verbsFor(t *testing.T, role string) []string {
	t.Helper()
	rules, err := RulesFor(role)
	require.NoError(t, err)

	for _, r := range rules {
		if len(r.Resources) > 0 {
			return r.Verbs
		}
	}
	t.Fatalf("role %q grants no resource rule at all", role)
	return nil
}

// The whole point of having tiers: each must be strictly less than the one above.
// If two roles ever grant the same verbs, the model claims a distinction it does
// not enforce.
func TestRolesAreStrictlyOrdered(t *testing.T) {
	admin := verbsFor(t, pmtenancyv1alpha1.MembershipRoleAdmin)
	member := verbsFor(t, pmtenancyv1alpha1.MembershipRoleMember)
	viewer := verbsFor(t, pmtenancyv1alpha1.MembershipRoleViewer)

	assert.Equal(t, []string{"*"}, admin)
	assert.Subset(t, member, viewer, "viewer must grant nothing a member does not")
	assert.NotEqual(t, member, viewer, "viewer must be strictly less than member")
	assert.Greater(t, len(member), len(viewer))
}

// The two verbs that decide whether a caller can grow their own rights. Neither
// may reach a non-admin, or kube's escalation prevention stops applying.
func TestOnlyAdminCanEscalateOrBind(t *testing.T) {
	for _, role := range []string{pmtenancyv1alpha1.MembershipRoleMember, pmtenancyv1alpha1.MembershipRoleViewer} {
		verbs := verbsFor(t, role)
		for _, forbidden := range []string{"escalate", "bind", "impersonate", "*"} {
			assert.NotContains(t, verbs, forbidden, "%s must not grant %q", role, forbidden)
		}
	}
}

// `create` is a write in disguise on several resources — SubjectAccessReview,
// TokenRequest, `.../exec` — so a read-only role must not carry it.
func TestViewerIsReadOnly(t *testing.T) {
	assert.ElementsMatch(t, []string{"get", "list", "watch"},
		verbsFor(t, pmtenancyv1alpha1.MembershipRoleViewer))
}

// Entering a workspace is the precondition for having privileges, not a
// privilege: without these two non-resource rules kcp's content authorizer
// refuses every request, and kubectl cannot discover anything.
func TestEveryRoleCanEnterAndDiscover(t *testing.T) {
	for _, role := range []string{
		pmtenancyv1alpha1.MembershipRoleAdmin,
		pmtenancyv1alpha1.MembershipRoleMember,
		pmtenancyv1alpha1.MembershipRoleViewer,
	} {
		rules, err := RulesFor(role)
		require.NoError(t, err)

		var access, discovery bool
		for _, r := range rules {
			if contains(r.NonResourceURLs, "/") && contains(r.Verbs, "access") {
				access = true
			}
			if contains(r.NonResourceURLs, "/apis") && contains(r.Verbs, "get") {
				discovery = true
			}
		}
		assert.True(t, access, "%s cannot enter a workspace", role)
		assert.True(t, discovery, "%s cannot run discovery", role)
	}
}

// An unknown role is terminal, not a retry: no amount of requeuing fixes a bad
// spec, and defaulting to something would grant access nobody asked for.
func TestUnknownRoleIsAnError(t *testing.T) {
	_, err := RulesFor("superuser")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown role")

	_, err = ClusterRoleFor("superuser")
	require.Error(t, err)

	_, err = RulesFor("")
	require.Error(t, err)
}

// Every role in the API enum must map to a ClusterRole, and no two roles may map
// to the same one — that would silently merge two tiers.
func TestEveryRoleMapsToItsOwnClusterRole(t *testing.T) {
	seen := map[string]string{}
	for _, role := range []string{
		pmtenancyv1alpha1.MembershipRoleAdmin,
		pmtenancyv1alpha1.MembershipRoleMember,
		pmtenancyv1alpha1.MembershipRoleViewer,
	} {
		name, err := ClusterRoleFor(role)
		require.NoError(t, err, "role %q has no ClusterRole", role)

		if other, dup := seen[name]; dup {
			t.Fatalf("roles %q and %q both map to ClusterRole %q", role, other, name)
		}
		seen[name] = role
	}
}

// The prefix is how the binding watcher tells platform-owned bindings apart from
// a tenant's own RBAC, and how a human reading `kubectl get clusterrolebindings`
// knows hand-editing will be undone.
func TestBindingNameIsPrefixedAndDerived(t *testing.T) {
	name := bindingName("m-1")
	assert.True(t, len(name) > len("m-1"))
	assert.Contains(t, name, "m-1")
	assert.Equal(t, name, bindingName("m-1"), "the binding name must be derivable again on delete")
	assert.NotEqual(t, name, bindingName("m-2"))
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
