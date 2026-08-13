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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"

	"k8s.io/client-go/util/retry"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Scenario: somebody holds two granted groups, with different roles.
func TestScenarioSeveralGroupsUnionTheirRoles(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())

	admin := f.createUser(t, "owner@union.example")
	tenant := f.provisionTenant(t, "union", "Union", admin.Name)
	project := f.provisionProject(t, tenant, "union-prod", "production")
	cluster := project.Status.ClusterID

	f.grantGroup(t, tenant, "everyone", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleViewer)
	f.grantGroup(t, tenant, "platform-admins", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleAdmin)

	viewerOnly := []string{"pm:everyone"}
	f.awaitAdmitted(t, cluster, "pm:reader@union.example", viewerOnly, "the viewer group reads")
	assert.False(t,
		allowed(t, cluster, "pm:reader@union.example", viewerOnly, "create", "", "configmaps"),
		"the viewer group must not write")

	both := []string{"pm:everyone", "pm:platform-admins"}
	f.awaitVerb(t, cluster, "pm:boss@union.example", both,
		"escalate", "rbac.authorization.k8s.io", "clusterroles", true,
		"holding a weaker group as well must not cap the stronger one")
}

// Scenario: a group named something that cannot be a Kubernetes object.
func TestScenarioGroupNamesKubernetesCannotStore(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())

	admin := f.createUser(t, "owner@awkward.example")
	tenant := f.provisionTenant(t, "awkward", "Awkward", admin.Name)
	project := f.provisionProject(t, tenant, "awkward-prod", "production")
	cluster := project.Status.ClusterID

	for _, group := range []string{
		"team-a/admins",  // a path, which no object name may contain
		"Domain Users",   // capitals and a space
		"CN=ops,OU=corp", // an LDAP distinguished name
	} {
		t.Run(group, func(t *testing.T) {
			f.grantGroup(t, tenant, group, pmtenancyv1alpha1.MembershipScopeTenant, "",
				pmtenancyv1alpha1.MembershipRoleMember)

			// The binding names the group verbatim behind the prefix.
			f.awaitAdmitted(t, cluster, "pm:member@awkward.example", []string{"pm:" + group},
				"a group must be usable whatever the identity provider calls it")

			f.awaitGroupIndexed(t, group, tenant.Name)
		})
	}
}

// Scenario: a grant's role is changed rather than added or removed.
func TestScenarioChangingARoleReplacesTheBinding(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())

	admin := f.createUser(t, "owner@rolechange.example")
	tenant := f.provisionTenant(t, "rolechange", "Role Change", admin.Name)
	project := f.provisionProject(t, tenant, "rolechange-prod", "production")
	cluster := project.Status.ClusterID

	m := f.grantGroup(t, tenant, "contractors", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleViewer)

	groups := []string{"pm:contractors"}
	f.awaitAdmitted(t, cluster, "pm:temp@rolechange.example", groups, "a viewer reads")
	require.False(t, allowed(t, cluster, "pm:temp@rolechange.example", groups, "create", "", "configmaps"),
		"a viewer does not write")

	// Promote.
	t.Log("promoting the group from viewer to member")
	f.setRole(t, tenant, m, pmtenancyv1alpha1.MembershipRoleMember)

	f.awaitVerb(t, cluster, "pm:temp@rolechange.example", groups, "create", "", "configmaps", true,
		"a promotion must widen the access, which needs the binding replaced")

	// Demote.
	t.Log("demoting the group back to viewer")
	f.setRole(t, tenant, m, pmtenancyv1alpha1.MembershipRoleViewer)

	f.awaitVerb(t, cluster, "pm:temp@rolechange.example", groups, "create", "", "configmaps", false,
		"a demotion must actually narrow the access")
	assert.True(t, allowed(t, cluster, "pm:temp@rolechange.example", groups, "get", "", "configmaps"),
		"and must leave what the lower role still grants")
}

// Scenario: the same person is granted directly AND through a group.
func TestScenarioUserAndGroupGrantsAreIndependent(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())

	admin := f.createUser(t, "owner@both.example")
	tenant := f.provisionTenant(t, "both", "Both", admin.Name)
	project := f.provisionProject(t, tenant, "both-prod", "production")
	cluster := project.Status.ClusterID

	dave := f.createUser(t, "dave@both.example")
	daveGroups := []string{"pm:both-engineering"}

	personal := f.grantUser(t, tenant, dave, pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleViewer)
	f.grantGroup(t, tenant, "both-engineering", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleMember)

	f.awaitAdmitted(t, cluster, rbacName("dave@both.example"), daveGroups, "dave is admitted")
	f.awaitVerb(t, cluster, rbacName("dave@both.example"), daveGroups, "create", "", "configmaps", true,
		"the group grant is the stronger of the two and must apply")

	// Revoke the PERSONAL grant. The group grant is a different object covering a
	// different subject, and must be untouched.
	t.Log("revoking dave's personal grant, leaving the group grant")
	f.revoke(t, tenant, personal)

	assert.True(t,
		allowed(t, cluster, rbacName("dave@both.example"), daveGroups, "get", "", "configmaps"),
		"revoking one subject's grant must not disturb the other's")

	f.awaitDenied(t, cluster, rbacName("dave@both.example"), nil,
		"the personal grant really was revoked")
}

// Scenario: two tenants, and nothing leaks between them.
func TestScenarioTenantsAreIsolated(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())

	admin := f.createUser(t, "owner@iso.example")

	acme := f.provisionTenant(t, "iso-acme", "ACME", admin.Name)
	acmeProject := f.provisionProject(t, acme, "iso-acme-prod", "production")
	f.grantGroup(t, acme, "acme-staff", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleAdmin)

	globex := f.provisionTenant(t, "iso-globex", "Globex", admin.Name)
	globexProject := f.provisionProject(t, globex, "iso-globex-prod", "production")
	f.grantGroup(t, globex, "globex-staff", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleAdmin)

	acmeStaff := []string{"pm:acme-staff"}
	globexStaff := []string{"pm:globex-staff"}

	f.awaitAdmitted(t, acmeProject.Status.ClusterID, "pm:a@iso.example", acmeStaff, "acme staff reach acme")
	f.awaitAdmitted(t, globexProject.Status.ClusterID, "pm:g@iso.example", globexStaff, "globex staff reach globex")

	assert.False(t,
		allowed(t, globexProject.Status.ClusterID, "pm:a@iso.example", acmeStaff, "get", "", "configmaps"),
		"admin in one tenant must be nobody in another")
	assert.False(t,
		allowed(t, acmeProject.Status.ClusterID, "pm:g@iso.example", globexStaff, "get", "", "configmaps"),
		"and the same in the other direction")
}

// Scenario: a grant is revoked and then given again.
func TestScenarioAccessCanBeGrantedAgainAfterRevoking(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())

	admin := f.createUser(t, "owner@regrant.example")
	tenant := f.provisionTenant(t, "regrant", "Regrant", admin.Name)
	project := f.provisionProject(t, tenant, "regrant-prod", "production")
	cluster := project.Status.ClusterID

	groups := []string{"pm:regrant-team"}

	first := f.grantGroup(t, tenant, "regrant-team", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleMember)
	f.awaitAdmitted(t, cluster, "pm:someone@regrant.example", groups, "granted once")

	f.revoke(t, tenant, first)
	f.awaitDenied(t, cluster, "pm:someone@regrant.example", groups, "revoked")

	t.Log("granting the same access again")
	second := f.grantGroup(t, tenant, "regrant-team", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleMember)
	assert.Equal(t, first.Name, second.Name,
		"the same grant must derive the same name, or a repeat would make a second object")

	f.awaitAdmitted(t, cluster, "pm:someone@regrant.example", groups,
		"regranting must work: nothing from the first grant may be left in the way")
}

// setRole changes an existing Membership's role, the one edit the model allows.
func (f *fleet) setRole(tb testing.TB, tenant *pmtenancyv1alpha1.Tenant, m *pmtenancyv1alpha1.Membership, role string) {
	tb.Helper()

	// Same conflict hazard as provisionTenant: the Membership controller adds
	// finalizers and writes status to this object while the test edits its spec.
	cl := clusterClient(f.tenantPath(tenant))
	require.NoError(tb, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &pmtenancyv1alpha1.Membership{}
		if err := cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: m.Name}, current); err != nil {
			return err
		}
		current.Spec.Role = role
		return cl.Update(context.Background(), current)
	}))
}

// Scenario: an admin provisions the tree deliberately and hands access to a group.
func TestScenarioGroupDrivenTenant(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())

	t.Log("an admin provisions a tenant and a project")
	admin := f.createUser(t, "admin@acme.example")

	// No personal tenant for anybody, including the admin: this installation says
	// the identity provider decides where people belong.
	assertNoPersonalTenant(t, f, admin)

	tenant := f.provisionTenant(t, "acme", "ACME", admin.Name)
	project := f.provisionProject(t, tenant, "acme-prod", "production")
	cluster := project.Status.ClusterID

	// The admin's own grant is seeded from status.firstAdmin — the break-glass
	// identity every tenant keeps, because a group cannot be verified to contain
	// anyone.
	f.awaitAdmitted(t, cluster, rbacName("admin@acme.example"), nil,
		"the first admin must be able to administer what they provisioned")

	// 2. Nobody else is in.
	const engineer = "pm:never-signed-in@acme.example"
	inGroup := []string{"pm:acme-engineering"}
	outOfGroup := []string{"pm:acme-sales"}

	require.False(t, allowed(t, cluster, engineer, inGroup, "get", "", "configmaps"),
		"holding a group grants nothing until the group is granted something")

	t.Log("the admin grants acme-engineering member access")
	grant := f.grantGroup(t, tenant, "acme-engineering", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleMember)

	// Someone with NO User object at all. This is the thing a user grant cannot
	// express: `spec.user` names an object that has to exist first, so a user
	// grant cannot reach anyone who has not signed in.
	f.awaitAdmitted(t, cluster, engineer, inGroup,
		"a group grant must reach somebody who has never signed in")

	// And a second person, admitted by the same single object. Nothing was created
	// for them — this is what "the next person to join the group" looks like.
	assert.True(t,
		allowed(t, cluster, "pm:someone-else@acme.example", inGroup, "get", "", "configmaps"),
		"one group grant must cover everyone in the group, with no per-person record")

	f.awaitGroupIndexed(t, "acme-engineering", tenant.Name)

	// 4. A different group is not this group.
	assert.False(t,
		allowed(t, cluster, engineer, outOfGroup, "get", "", "configmaps"),
		"a grant to one group must not admit the holder of another")

	// Leaving the group is the identity provider's business, and it takes effect
	// on the next token with nothing to clean up here: the same person, without
	// the group, is simply not admitted.
	assert.False(t,
		allowed(t, cluster, engineer, nil, "get", "", "configmaps"),
		"dropping the group at the IdP must take the access with it, with no platform write")

	t.Log("the admin revokes the group grant")
	f.revoke(t, tenant, grant)

	f.awaitDenied(t, cluster, engineer, inGroup,
		"revoking a group grant must revoke it for everyone it covered")
	assert.False(t,
		allowed(t, cluster, "pm:someone-else@acme.example", inGroup, "get", "", "configmaps"),
		"including the people who were never named anywhere")

	// The admin keeps theirs — the break-glass identity survives, which is exactly
	// why the last-admin guard refuses to count a group as the only admin.
	assert.True(t,
		allowed(t, cluster, rbacName("admin@acme.example"), nil, "get", "", "configmaps"),
		"the user-subject admin must outlive the group grant")
}
