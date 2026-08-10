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

package virtualworkspace_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/virtualworkspace"
	"go.platform-mesh.io/tenancy-operator/pkg/membership"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/registry/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func (f *fleet) membershipStorage(t *testing.T) *virtualworkspace.MembershipStorage {
	t.Helper()
	return virtualworkspace.NewMembershipStorage(f.directory, f.clusterClient, testResolver(t))
}

func directoryUser(name string) *pmtenancyv1alpha1.User {
	return &pmtenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func tenantMembership(user, role string) *pmtenancyv1alpha1.Membership {
	return &pmtenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{Name: membership.Name(user, pmtenancyv1alpha1.MembershipScopeTenant, "")},
		Spec: pmtenancyv1alpha1.MembershipSpec{
			User: user, Scope: pmtenancyv1alpha1.MembershipScopeTenant, Role: role,
		},
	}
}

func submitMembership(user, scope, project, role string) *pmtenancyv1alpha1.Membership {
	return &pmtenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{pmtenancyv1alpha1.LabelTenant: "tenant"},
		},
		Spec: pmtenancyv1alpha1.MembershipSpec{User: user, Scope: scope, Project: project, Role: role},
	}
}

// Only an admin grants access. A member who could add members could promote
// themselves by proxy; a viewer is read-only by definition.
func TestMembershipCreateRequiresAdmin(t *testing.T) {
	for role, wantAllow := range map[string]bool{"admin": true, "member": false, "viewer": false} {
		t.Run(role, func(t *testing.T) {
			f := newFleet(t,
				[]ctrlruntimeclient.Object{
					directoryUser("teammate"),
					index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{
						TenantUUID: "tenant", TenantClusterID: "c1", Role: role,
					}),
				},
				map[string][]ctrlruntimeclient.Object{"c1": nil},
			)

			_, err := f.membershipStorage(t).Create(
				authenticated(testIssuer, testSubject, testEmail),
				submitMembership("teammate", pmtenancyv1alpha1.MembershipScopeTenant, "", "member"),
				nil, &metav1.CreateOptions{})

			if wantAllow {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsForbidden(err), "want Forbidden, got %v", err)
		})
	}
}

// A User exists only after its owner has signed in once, so granting access to
// someone who never has would write a Membership resolving to nobody — and the
// reconciler would report it as a failure rather than a pending invitation.
func TestMembershipCreateRejectsAnUnknownUser(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": nil},
	)

	_, err := f.membershipStorage(t).Create(
		authenticated(testIssuer, testSubject, testEmail),
		submitMembership("never-signed-in", pmtenancyv1alpha1.MembershipScopeTenant, "", "member"),
		nil, &metav1.CreateOptions{})

	require.Error(t, err)
	assert.True(t, apierrors.IsBadRequest(err), "want BadRequest, got %v", err)
	assert.Contains(t, err.Error(), "sign in once")
}

// Not a member: 404 rather than 403, so this cannot be used to discover which
// Tenant UUIDs exist.
func TestMembershipCreateInAnOrgYouAreNotInIs404(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{directoryUser("teammate"), index(wantName(t))},
		map[string][]ctrlruntimeclient.Object{"c1": nil},
	)

	_, err := f.membershipStorage(t).Create(
		authenticated(testIssuer, testSubject, testEmail),
		submitMembership("teammate", pmtenancyv1alpha1.MembershipScopeTenant, "", "member"),
		nil, &metav1.CreateOptions{})

	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err), "want NotFound, got %v", err)
	assert.False(t, apierrors.IsForbidden(err), "403 would confirm the tenant exists")
}

// Deriving the name from what the grant covers makes a repeat grant collide rather
// than produce a second object. Two Memberships for one grant means two role
// bindings, and revoking one would leave the other live.
func TestMembershipCreateIsIdempotentByName(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			directoryUser("teammate"),
			index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": nil},
	)
	s := f.membershipStorage(t)
	ctx := authenticated(testIssuer, testSubject, testEmail)

	first, err := s.Create(ctx, submitMembership("teammate", pmtenancyv1alpha1.MembershipScopeTenant, "", "member"), nil, &metav1.CreateOptions{})
	require.NoError(t, err)
	assert.Equal(t, membership.Name("teammate", pmtenancyv1alpha1.MembershipScopeTenant, ""),
		first.(*pmtenancyv1alpha1.Membership).Name)

	_, err = s.Create(ctx, submitMembership("teammate", pmtenancyv1alpha1.MembershipScopeTenant, "", "admin"), nil, &metav1.CreateOptions{})
	require.Error(t, err)
	assert.True(t, apierrors.IsAlreadyExists(err), "want AlreadyExists, got %v", err)
}

// Self-leave is `delete` on your own row, not a separate endpoint — so a member
// with no admin rights may still remove themselves.
func TestMembershipDeleteAllowsSelfLeave(t *testing.T) {
	self := wantName(t)
	mine := tenantMembership(self, "member")
	other := tenantMembership("someone-else", "admin")

	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(self, pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "member",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {mine, other}},
	)

	_, _, err := f.membershipStorage(t).Delete(
		authenticated(testIssuer, testSubject, testEmail), mine.Name, nil, &metav1.DeleteOptions{})
	require.NoError(t, err)
}

// A member may remove themselves and nobody else.
func TestMembershipDeleteRefusesSomeoneElsesRow(t *testing.T) {
	other := tenantMembership("someone-else", "member")
	admin := tenantMembership("an-admin", "admin")

	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "member",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {other, admin}},
	)

	_, _, err := f.membershipStorage(t).Delete(
		authenticated(testIssuer, testSubject, testEmail), other.Name, nil, &metav1.DeleteOptions{})

	require.Error(t, err)
	assert.True(t, apierrors.IsForbidden(err), "want Forbidden, got %v", err)
}

// A Tenant with no admin is not degraded, it is UNRECOVERABLE: every write
// to the tier needs an admin and there is no kubectl path into the Tenant
// workspace to repair it from.
func TestMembershipDeleteRefusesTheLastAdmin(t *testing.T) {
	self := wantName(t)
	onlyAdmin := tenantMembership(self, "admin")
	aMember := tenantMembership("a-member", "member")

	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(self, pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {onlyAdmin, aMember}},
	)

	_, _, err := f.membershipStorage(t).Delete(
		authenticated(testIssuer, testSubject, testEmail), onlyAdmin.Name, nil, &metav1.DeleteOptions{})

	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err), "want Conflict, got %v", err)
	assert.Contains(t, err.Error(), "last admin")
}

// With a second admin present the same delete is fine — the guard is about the
// last one, not about admins in general.
func TestMembershipDeleteAllowsAnAdminWhenAnotherRemains(t *testing.T) {
	self := wantName(t)
	me := tenantMembership(self, "admin")
	spare := tenantMembership("second-admin", "admin")

	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(self, pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {me, spare}},
	)

	_, _, err := f.membershipStorage(t).Delete(
		authenticated(testIssuer, testSubject, testEmail), me.Name, nil, &metav1.DeleteOptions{})
	require.NoError(t, err)
}

// A Membership in a Tenant the caller cannot see is absent, not forbidden.
func TestMembershipGetOutsideYourOrgsIs404(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{index(wantName(t))},
		map[string][]ctrlruntimeclient.Object{"c1": {tenantMembership("stranger", "admin")}},
	)

	_, err := f.membershipStorage(t).Get(
		authenticated(testIssuer, testSubject, testEmail),
		membership.Name("stranger", pmtenancyv1alpha1.MembershipScopeTenant, ""), &metav1.GetOptions{})

	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err), "want NotFound, got %v", err)
}

// Co-members are visible on purpose: a member who cannot read the roster cannot
// tell whether an admin still exists.
func TestMembershipListShowsCoMembers(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "viewer",
			}),
		},
		map[string][]ctrlruntimeclient.Object{
			"c1": {tenantMembership("alice", "admin"), tenantMembership("bob", "member")},
		},
	)

	out, err := f.membershipStorage(t).List(authenticated(testIssuer, testSubject, testEmail), nil)
	require.NoError(t, err)

	list, ok := out.(*pmtenancyv1alpha1.MembershipList)
	require.True(t, ok)
	require.Len(t, list.Items, 2)
	for _, m := range list.Items {
		assert.Equal(t, "tenant", m.Labels[pmtenancyv1alpha1.LabelTenant],
			"a cross-tenant listing with no owner on each row cannot be grouped")
	}
}

// updateTo returns the objInfo a storage Update expects: the stored object with
// one field changed, which is what a read-modify-write client sends.
func updateTo(base *pmtenancyv1alpha1.Membership, role string) rest.UpdatedObjectInfo {
	m := base.DeepCopy()
	m.Spec.Role = role
	return rest.DefaultUpdatedObjectInfo(m)
}

// Editing a role is admin-only, with NO self-service arm: `delete` lets you drop
// your own grant because leaving is yours to decide, but editing your own is
// self-promotion with extra steps.
func TestMembershipUpdateRequiresAdmin(t *testing.T) {
	self := wantName(t)
	mine := tenantMembership(self, "member")

	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(self, pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "member",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {mine, tenantMembership("an-admin", "admin")}},
	)

	_, _, err := f.membershipStorage(t).Update(
		authenticated(testIssuer, testSubject, testEmail), mine.Name,
		updateTo(mine, "admin"), nil, nil, false, &metav1.UpdateOptions{})

	require.Error(t, err)
	assert.True(t, apierrors.IsForbidden(err), "want Forbidden, got %v", err)
}

// Demoting the last admin leaves a Tenant nobody can administer, exactly as
// deleting them would — the hazard is the grant going away, not the verb used.
func TestMembershipUpdateRefusesDemotingTheLastAdmin(t *testing.T) {
	self := wantName(t)
	onlyAdmin := tenantMembership(self, "admin")

	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(self, pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {onlyAdmin, tenantMembership("a-member", "member")}},
	)

	_, _, err := f.membershipStorage(t).Update(
		authenticated(testIssuer, testSubject, testEmail), onlyAdmin.Name,
		updateTo(onlyAdmin, "viewer"), nil, nil, false, &metav1.UpdateOptions{})

	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err), "want Conflict, got %v", err)
}

// The role is the only mutable field: metadata.name is derived from the other
// three, so changing them describes a DIFFERENT grant stored under this one's
// name — two grants sharing a name, where revoking one silently revokes the other.
func TestMembershipUpdateRefusesChangingWhoTheGrantIsFor(t *testing.T) {
	self := wantName(t)
	target := tenantMembership("teammate", "member")

	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(self, pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {target, tenantMembership("other-admin", "admin")}},
	)

	repointed := target.DeepCopy()
	repointed.Spec.User = "somebody-else"
	repoint := rest.DefaultUpdatedObjectInfo(repointed)

	_, _, err := f.membershipStorage(t).Update(
		authenticated(testIssuer, testSubject, testEmail), target.Name,
		repoint, nil, nil, false, &metav1.UpdateOptions{})

	require.Error(t, err)
	assert.True(t, apierrors.IsBadRequest(err), "want BadRequest, got %v", err)
	assert.Contains(t, err.Error(), "only spec.role")
}

// The ordinary case: an admin promotes a member.
func TestMembershipUpdateChangesTheRole(t *testing.T) {
	self := wantName(t)
	target := tenantMembership("teammate", "member")

	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(self, pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {target, tenantMembership(self, "admin")}},
	)

	out, _, err := f.membershipStorage(t).Update(
		authenticated(testIssuer, testSubject, testEmail), target.Name,
		updateTo(target, "admin"), nil, nil, false, &metav1.UpdateOptions{})

	require.NoError(t, err)
	assert.Equal(t, "admin", out.(*pmtenancyv1alpha1.Membership).Spec.Role)
}

func groupMembership(group, role string) *pmtenancyv1alpha1.Membership {
	return &pmtenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{Name: membership.NameForGroup(group, pmtenancyv1alpha1.MembershipScopeTenant, "")},
		Spec: pmtenancyv1alpha1.MembershipSpec{
			Group: group, Scope: pmtenancyv1alpha1.MembershipScopeTenant, Role: role,
		},
	}
}

// A group subject needs no prior sign-in, which is the one thing a user subject
// cannot do — and the reason group grants exist. Nothing is checked because
// nothing CAN be: the platform holds no object for a group.
func TestMembershipCreateAcceptsAnUncheckableGroup(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": nil},
	)

	submitted := submitMembership("", pmtenancyv1alpha1.MembershipScopeTenant, "", "member")
	submitted.Spec.Group = "acme-engineering"

	obj, err := f.membershipStorage(t).Create(
		authenticated(testIssuer, testSubject, testEmail), submitted, nil, &metav1.CreateOptions{})
	require.NoError(t, err)

	created := obj.(*pmtenancyv1alpha1.Membership)
	assert.Equal(t, "acme-engineering", created.Spec.Group)
	assert.Empty(t, created.Spec.User)
	assert.Equal(t, pmtenancyv1alpha1.SubjectKindGroup, created.SubjectKind())
}

// One subject per grant. metadata.name is derived from it, so a Membership
// carrying both is a grant whose identity depends on which field was read first.
func TestMembershipCreateRefusesTwoSubjects(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			directoryUser("teammate"),
			index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": nil},
	)

	submitted := submitMembership("teammate", pmtenancyv1alpha1.MembershipScopeTenant, "", "member")
	submitted.Spec.Group = "acme-engineering"

	_, err := f.membershipStorage(t).Create(
		authenticated(testIssuer, testSubject, testEmail), submitted, nil, &metav1.CreateOptions{})
	require.Error(t, err)
	assert.True(t, apierrors.IsBadRequest(err), "want BadRequest, got %v", err)
}

// THE SHARPEST CONSEQUENCE OF GROUP GRANTS. A group-subject admin is not evidence
// that any admin exists: the platform cannot tell an empty group from a full one.
// Counting one as the survivor would let the guard pass while leaving the tenant
// exactly as unrecoverable as deleting its last admin outright.
func TestMembershipDeleteRefusesTheLastUserAdminEvenWithAGroupAdmin(t *testing.T) {
	self := wantName(t)
	onlyUserAdmin := tenantMembership(self, "admin")
	groupAdmin := groupMembership("acme-owners", "admin")

	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(self, pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {onlyUserAdmin, groupAdmin}},
	)

	_, _, err := f.membershipStorage(t).Delete(
		authenticated(testIssuer, testSubject, testEmail), onlyUserAdmin.Name, nil, &metav1.DeleteOptions{})

	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err), "want Conflict, got %v", err)
}

// The converse: removing a GROUP admin is never what strands a tenant, because it
// was never what proved the tenant had one. The guard must not block it.
func TestMembershipDeleteAllowsRemovingAGroupAdmin(t *testing.T) {
	self := wantName(t)
	userAdmin := tenantMembership(self, "admin")
	groupAdmin := groupMembership("acme-owners", "admin")

	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(self, pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {userAdmin, groupAdmin}},
	)

	_, _, err := f.membershipStorage(t).Delete(
		authenticated(testIssuer, testSubject, testEmail), groupAdmin.Name, nil, &metav1.DeleteOptions{})
	require.NoError(t, err)
}

// Self-leave is for a grant made to YOU. Deleting a group grant revokes it for
// everyone holding that group, so letting a non-admin "leave" through this path
// would be one member removing every other member's access.
func TestMembershipDeleteRefusesSelfLeaveOfAGroupGrant(t *testing.T) {
	self := wantName(t)
	mine := tenantMembership(self, "member")
	groupGrant := groupMembership("acme-engineering", "member")

	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(self, pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "member",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {mine, groupGrant}},
	)

	_, _, err := f.membershipStorage(t).Delete(
		authenticated(testIssuer, testSubject, testEmail), groupGrant.Name, nil, &metav1.DeleteOptions{})

	require.Error(t, err)
	assert.True(t, apierrors.IsForbidden(err), "want Forbidden, got %v", err)
	assert.Contains(t, err.Error(), "identity provider")
}
