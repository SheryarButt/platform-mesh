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

package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/controller/memberships"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/kcp-dev/multicluster-provider/envtest"
	"github.com/kcp-dev/sdk/apis/core"
)

// workspace returns a bare workspace to write RBAC into, standing in for the
// Project workspace a Membership binds in.
func workspace(t *testing.T, prefix string) string {
	t.Helper()
	ws, path := envtest.NewWorkspaceFixture(t, kcpClient, core.RootCluster.Path(), envtest.WithNamePrefix(prefix))
	_ = ws
	return path.String()
}

// Scenario: kcp admits somebody because of a Group binding we wrote.
func TestScenarioKcpAdmitsAGroupSubject(t *testing.T) {
	path := workspace(t, "group-rbac")

	resolver, err := identity.NewResolver(identity.Config{
		UsernameClaim:  identity.ClaimEmail,
		UsernamePrefix: "pm:",
		GroupsPrefix:   "pm:",
	})
	require.NoError(t, err)

	// Exactly what the reconciler would write for a group-subject Membership.
	subject, err := resolver.RBACGroup("acme-engineering")
	require.NoError(t, err)
	rules, err := memberships.RulesFor(pmtenancyv1alpha1.MembershipRoleMember)
	require.NoError(t, err)
	role, err := memberships.ClusterRoleFor(pmtenancyv1alpha1.MembershipRoleMember)
	require.NoError(t, err)

	bindRole(t, path, role, rules, rbacv1.Subject{
		APIGroup: rbacv1.GroupName, Kind: rbacv1.GroupKind, Name: subject,
	})

	// Someone the platform has never heard of, carrying the group.
	assert.True(t,
		allowed(t, path, "pm:never-seen@example.com", []string{"pm:acme-engineering"}, "create", "", "configmaps"),
		"a group binding must admit anyone whose token carries the group — including someone with no User object")

	// The same person without it.
	assert.False(t,
		allowed(t, path, "pm:never-seen@example.com", []string{"pm:some-other-group"}, "create", "", "configmaps"),
		"a group binding must not admit somebody who does not hold the group")
}

// Scenario: a prefix mismatch is a group nobody holds, and reports no error.
func TestScenarioGroupPrefixIsLoadBearing(t *testing.T) {
	path := workspace(t, "group-prefix")

	rules, err := memberships.RulesFor(pmtenancyv1alpha1.MembershipRoleMember)
	require.NoError(t, err)
	role, err := memberships.ClusterRoleFor(pmtenancyv1alpha1.MembershipRoleMember)
	require.NoError(t, err)

	bindRole(t, path, role, rules, rbacv1.Subject{
		APIGroup: rbacv1.GroupName, Kind: rbacv1.GroupKind, Name: "pm:acme-engineering",
	})

	assert.True(t,
		allowed(t, path, "pm:alice@example.com", []string{"pm:acme-engineering"}, "get", "", "configmaps"))
	assert.False(t,
		allowed(t, path, "pm:alice@example.com", []string{"acme-engineering"}, "get", "", "configmaps"),
		"an unprefixed group must NOT match a prefixed binding: this is the drift that has no error message")
	assert.False(t,
		allowed(t, path, "pm:alice@example.com", []string{"oidc:acme-engineering"}, "get", "", "configmaps"),
		"kcp's own default prefix is `oidc:`, so this is the mismatch a forgotten groupsPrefix produces")
}

// Scenario: the three roles differ where it counts, on `escalate` and `bind`.
func TestScenarioRolesDifferAsClaimed(t *testing.T) {
	for _, tc := range []struct {
		role                        string
		wantWrite, wantEscalateBind bool
	}{
		{pmtenancyv1alpha1.MembershipRoleAdmin, true, true},
		{pmtenancyv1alpha1.MembershipRoleMember, true, false},
		{pmtenancyv1alpha1.MembershipRoleViewer, false, false},
	} {
		t.Run(tc.role, func(t *testing.T) {
			path := workspace(t, "role-"+tc.role)

			rules, err := memberships.RulesFor(tc.role)
			require.NoError(t, err)
			role, err := memberships.ClusterRoleFor(tc.role)
			require.NoError(t, err)

			user := "pm:" + tc.role + "@example.com"
			bindRole(t, path, role, rules, rbacv1.Subject{
				APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: user,
			})

			assert.True(t, allowed(t, path, user, nil, "get", "", "configmaps"),
				"every role reads")
			assert.Equal(t, tc.wantWrite, allowed(t, path, user, nil, "create", "", "configmaps"),
				"a viewer must not create: on some resources create IS a write in disguise")

			// The real boundary. `escalate` is what stops a caller writing a Role
			// granting more than they hold, and `bind` what stops them binding one.
			assert.Equal(t, tc.wantEscalateBind,
				allowed(t, path, user, nil, "escalate", "rbac.authorization.k8s.io", "clusterroles"),
				"escalate is what separates admin from member")
			assert.Equal(t, tc.wantEscalateBind,
				allowed(t, path, user, nil, "bind", "rbac.authorization.k8s.io", "clusterroles"))
		})
	}
}

// Scenario: entering a workspace at all needs the non-resource rules.
func TestScenarioWorkspaceEntryRulesAdmitEntry(t *testing.T) {
	path := workspace(t, "entry-rules")

	rules, err := memberships.RulesFor(pmtenancyv1alpha1.MembershipRoleViewer)
	require.NoError(t, err)
	role, err := memberships.ClusterRoleFor(pmtenancyv1alpha1.MembershipRoleViewer)
	require.NoError(t, err)

	bindRole(t, path, role, rules, rbacv1.Subject{
		APIGroup: rbacv1.GroupName, Kind: rbacv1.GroupKind, Name: "pm:acme-engineering",
	})

	groups := []string{"pm:acme-engineering"}
	assert.True(t, allowedNonResource(t, path, "pm:alice@example.com", groups, "access", "/"),
		"kcp's content authorizer gates the workspace on `access` to `/`")
	assert.True(t, allowedNonResource(t, path, "pm:alice@example.com", groups, "get", "/apis"),
		"kubectl asks for /apis before anything else")
}

// Scenario: a group named like a user must not match a User-kind binding.
func TestScenarioAUserSubjectIsNotAGroup(t *testing.T) {
	path := workspace(t, "kind-matters")

	rules, err := memberships.RulesFor(pmtenancyv1alpha1.MembershipRoleMember)
	require.NoError(t, err)
	role, err := memberships.ClusterRoleFor(pmtenancyv1alpha1.MembershipRoleMember)
	require.NoError(t, err)

	bindRole(t, path, role, rules, rbacv1.Subject{
		APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: "pm:alice@example.com",
	})

	assert.True(t, allowed(t, path, "pm:alice@example.com", nil, "get", "", "configmaps"))
	assert.False(t,
		allowed(t, path, "pm:bob@example.com", []string{"pm:alice@example.com"}, "get", "", "configmaps"),
		"a GROUP named like a user must not match a User-kind binding")
}
