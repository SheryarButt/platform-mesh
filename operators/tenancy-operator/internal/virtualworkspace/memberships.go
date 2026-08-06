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
	"sort"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"
	"go.platform-mesh.io/tenancy-operator/pkg/membership"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apiserver/pkg/registry/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var membershipsResource = pmtenancyv1alpha1.Resource("memberships")

// MembershipStorage serves `memberships`: who belongs to a Tenant, and as
// what.
//
// THIS IS THE ONLY WAY A TENANT MANAGES ACCESS. Memberships live in the
// Tenant workspace, and no tenant identity has a role binding in that
// logical cluster — deliberately, because a tenant who could write there could
// grant themselves anything while bypassing every check below. So there is no
// kubectl path to fall back on: this storage holds the only credential that
// reaches the tier, which is what makes the rules here enforceable rather than
// advisory.
//
// That is also why the guards are in the storage rather than in an authorizer. An
// authorizer answers "may this caller delete memberships" with a boolean; the
// questions that matter are "is this their OWN row" and "is this the last admin",
// and neither is answerable without reading the object and its neighbours.
type MembershipStorage struct {
	// directory reads the caller's membership index and resolves Users.
	directory ctrlruntimeclient.Client

	// clusterClient reaches one Tenant workspace.
	clusterClient ClusterClientFunc

	resolver *identity.Resolver
}

var (
	_ rest.Storage              = &MembershipStorage{}
	_ rest.Scoper               = &MembershipStorage{}
	_ rest.SingularNameProvider = &MembershipStorage{}
	_ rest.Getter               = &MembershipStorage{}
	_ rest.Lister               = &MembershipStorage{}
	_ rest.TableConvertor       = &MembershipStorage{}
	_ rest.Creater              = &MembershipStorage{} //nolint:misspell // rest.Creater is the upstream interface name
	_ rest.Updater              = &MembershipStorage{}
	_ rest.GracefulDeleter      = &MembershipStorage{}
)

// NewMembershipStorage returns the `memberships` REST storage.
func NewMembershipStorage(directory ctrlruntimeclient.Client, clusterClient ClusterClientFunc, resolver *identity.Resolver) *MembershipStorage {
	return &MembershipStorage{directory: directory, clusterClient: clusterClient, resolver: resolver}
}

func (s *MembershipStorage) New() runtime.Object     { return &pmtenancyv1alpha1.Membership{} }
func (s *MembershipStorage) NewList() runtime.Object { return &pmtenancyv1alpha1.MembershipList{} }
func (s *MembershipStorage) Destroy()                {}
func (s *MembershipStorage) NamespaceScoped() bool   { return false }
func (s *MembershipStorage) GetSingularName() string { return "membership" }

// List returns the Memberships of every Tenant the caller belongs to.
//
// Co-members are visible on purpose: knowing who else is in your Tenant is
// part of being in it, and a member who cannot see the roster cannot tell whether
// an admin still exists.
//
// Visible to every role, including viewer. A viewer may read the roster and change
// nothing in it — which is the same shape as every other read on this surface.
func (s *MembershipStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	self, err := s.callerName(ctx)
	if err != nil {
		return nil, err
	}

	view, err := resolveAccess(ctx, s.directory, self)
	if err != nil {
		return nil, err
	}

	out := &pmtenancyv1alpha1.MembershipList{}
	for _, tenant := range view.Tenants {
		if tenant.ClusterID == "" {
			continue
		}
		cl, err := s.clusterClient(tenant.ClusterID)
		if err != nil {
			// One unreachable Tenant must not fail the whole listing.
			continue
		}
		list := &pmtenancyv1alpha1.MembershipList{}
		if err := cl.List(ctx, list); err != nil {
			continue
		}
		for i := range list.Items {
			m := &list.Items[i]
			stampMembershipOrganization(m, tenant.UUID)
			out.Items = append(out.Items, *m)
		}
	}

	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Name < out.Items[j].Name })
	return out, nil
}

// Get returns one Membership from a Tenant the caller belongs to.
func (s *MembershipStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	m, _, err := s.find(ctx, name)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// find locates a Membership by name across the caller's Tenants, and returns
// the tenant view it was found in so a caller can apply role checks against it.
func (s *MembershipStorage) find(ctx context.Context, name string) (*pmtenancyv1alpha1.Membership, *tenantAccess, error) {
	self, err := s.callerName(ctx)
	if err != nil {
		return nil, nil, err
	}

	view, err := resolveAccess(ctx, s.directory, self)
	if err != nil {
		return nil, nil, err
	}

	for _, tenant := range view.Tenants {
		if tenant.ClusterID == "" {
			continue
		}
		cl, err := s.clusterClient(tenant.ClusterID)
		if err != nil {
			continue
		}
		m := &pmtenancyv1alpha1.Membership{}
		if err := cl.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name}, m); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, nil, err
		}
		stampMembershipOrganization(m, tenant.UUID)
		return m, tenant, nil
	}

	// 404, not 403: a 403 would confirm that a Membership with this name exists in
	// a Tenant the caller cannot see.
	return nil, nil, apierrors.NewNotFound(membershipsResource, name)
}

// Create grants access.
//
// The Tenant is named by the tenancy.platform-mesh.io/tenant label —
// the same one List stamps on every row it returns, so what a client reads is what
// it writes back.
func (s *MembershipStorage) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	submitted, ok := obj.(*pmtenancyv1alpha1.Membership)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Membership, got %T", obj))
	}

	tenantUUID := submitted.Labels[pmtenancyv1alpha1.LabelTenant]
	if tenantUUID == "" {
		return nil, apierrors.NewBadRequest(fmt.Sprintf(
			"a Membership must say which Tenant it belongs to: set the %s label",
			pmtenancyv1alpha1.LabelTenant))
	}

	tenant, err := s.callerOrg(ctx, tenantUUID)
	if err != nil {
		return nil, err
	}

	// Only admins grant access. A member who could add members could promote
	// themselves by proxy, and a viewer is read-only by definition.
	if tenant.Role != pmtenancyv1alpha1.MembershipRoleAdmin {
		return nil, apierrors.NewForbidden(membershipsResource, "",
			fmt.Errorf("only an admin of tenant %s may grant access", tenantUUID))
	}

	if err := validateMembershipSpec(submitted.Spec); err != nil {
		return nil, apierrors.NewBadRequest(err.Error())
	}

	// The subject must already exist. A User is created only by its own identity
	// signing in (§Part 0), so granting access to someone who has never
	// authenticated would write a Membership that resolves to nobody — and the
	// reconciler would report it as a failure rather than a pending invitation.
	// Invitations are a separate object; this is deliberately not one.
	user := &pmtenancyv1alpha1.User{}
	if err := s.directory.Get(ctx, ctrlruntimeclient.ObjectKey{Name: submitted.Spec.User}, user); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, apierrors.NewBadRequest(fmt.Sprintf(
				"user %s does not exist: they must sign in once before they can be granted access",
				submitted.Spec.User))
		}
		return nil, err
	}

	if submitted.Spec.Scope == pmtenancyv1alpha1.MembershipScopeProject {
		cl, err := s.clusterClient(tenant.ClusterID)
		if err != nil {
			return nil, err
		}
		proj := &pmtenancyv1alpha1.Project{}
		if err := cl.Get(ctx, ctrlruntimeclient.ObjectKey{Name: submitted.Spec.Project}, proj); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, apierrors.NewBadRequest(fmt.Sprintf(
					"project %s does not exist in tenant %s", submitted.Spec.Project, tenantUUID))
			}
			return nil, err
		}
	}

	cl, err := s.clusterClient(tenant.ClusterID)
	if err != nil {
		return nil, err
	}

	// Derived, not minted: the name is a function of what the Membership grants, so
	// granting the same access twice collides on AlreadyExists instead of producing
	// two objects. Two Memberships for one grant means two role bindings, and
	// revoking one would leave the other live.
	m := &pmtenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{
			Name: membership.Name(submitted.Spec.User, submitted.Spec.Scope, submitted.Spec.Project),
		},
		Spec: pmtenancyv1alpha1.MembershipSpec{
			User:    submitted.Spec.User,
			Scope:   submitted.Spec.Scope,
			Project: submitted.Spec.Project,
			Role:    submitted.Spec.Role,
		},
	}
	if err := cl.Create(ctx, m); err != nil {
		return nil, err
	}

	stampMembershipOrganization(m, tenantUUID)
	return m, nil
}

// Update changes a role. It is the only edit this API accepts.
//
// ADMIN ONLY, with no self-service arm: `delete` lets you remove your own grant
// because leaving is yours to decide, but `update` would let you change what your
// own grant says — which is self-promotion with extra steps.
//
// Only spec.role is mutable. The other three fields are the IDENTITY of the grant,
// not attributes of it: metadata.name is derived from (user, scope, project), so
// changing any of them describes a different Membership that happens to be stored
// under this one's name. Two grants would then share a name, and revoking one
// would silently revoke the other.
func (s *MembershipStorage) Update(
	ctx context.Context,
	name string,
	objInfo rest.UpdatedObjectInfo,
	_ rest.ValidateObjectFunc,
	_ rest.ValidateObjectUpdateFunc,
	_ bool,
	_ *metav1.UpdateOptions,
) (runtime.Object, bool, error) {
	current, tenant, err := s.find(ctx, name)
	if err != nil {
		return nil, false, err
	}

	if tenant.Role != pmtenancyv1alpha1.MembershipRoleAdmin {
		return nil, false, apierrors.NewForbidden(membershipsResource, name,
			fmt.Errorf("only an admin of tenant %s may change a role", tenant.UUID))
	}

	updatedObj, err := objInfo.UpdatedObject(ctx, current.DeepCopy())
	if err != nil {
		return nil, false, err
	}
	updated, ok := updatedObj.(*pmtenancyv1alpha1.Membership)
	if !ok {
		return nil, false, apierrors.NewBadRequest(fmt.Sprintf("expected a Membership, got %T", updatedObj))
	}

	if updated.Spec.User != current.Spec.User ||
		updated.Spec.Scope != current.Spec.Scope ||
		updated.Spec.Project != current.Spec.Project {
		return nil, false, apierrors.NewBadRequest(
			"only spec.role may be changed: user, scope and project identify the grant, and " +
				"metadata.name is derived from them. Delete this Membership and create the one you want instead")
	}
	if err := validateMembershipSpec(updated.Spec); err != nil {
		return nil, false, apierrors.NewBadRequest(err.Error())
	}

	if updated.Spec.Role == current.Spec.Role {
		return current, false, nil
	}

	cl, err := s.clusterClient(tenant.ClusterID)
	if err != nil {
		return nil, false, err
	}

	// Demoting the last admin leaves a Tenant nobody can administer, exactly
	// as deleting them would. Same guard, because the hazard is the grant going
	// away and not the verb used to take it.
	if current.Spec.Role == pmtenancyv1alpha1.MembershipRoleAdmin &&
		updated.Spec.Role != pmtenancyv1alpha1.MembershipRoleAdmin {
		if err := s.guardLastAdmin(ctx, cl, current); err != nil {
			return nil, false, err
		}
	}

	// Patched field by field onto the stored object rather than written wholesale:
	// the submitted object came from a client and carries its own resourceVersion,
	// status and labels — including the tenant label this API stamps on reads,
	// which is derived and must not be persisted.
	stored := &pmtenancyv1alpha1.Membership{}
	if err := cl.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name}, stored); err != nil {
		return nil, false, err
	}
	stored.Spec.Role = updated.Spec.Role
	if err := cl.Update(ctx, stored); err != nil {
		return nil, false, err
	}

	stampMembershipOrganization(stored, tenant.UUID)
	return stored, false, nil
}

// Delete revokes access.
//
// Two callers are allowed: an admin of the Tenant, and the subject
// themselves. The second is self-leave — `delete` on your own row, rather than a
// `…/memberships/me` route, because anything that reads like a verb is better as a
// resource.
func (s *MembershipStorage) Delete(ctx context.Context, name string, _ rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	self, err := s.callerName(ctx)
	if err != nil {
		return nil, false, err
	}

	m, tenant, err := s.find(ctx, name)
	if err != nil {
		return nil, false, err
	}

	if tenant.Role != pmtenancyv1alpha1.MembershipRoleAdmin && m.Spec.User != self {
		return nil, false, apierrors.NewForbidden(membershipsResource, name,
			fmt.Errorf("only an admin of tenant %s, or the subject themselves, may revoke this", tenant.UUID))
	}

	cl, err := s.clusterClient(tenant.ClusterID)
	if err != nil {
		return nil, false, err
	}

	if err := s.guardLastAdmin(ctx, cl, m); err != nil {
		return nil, false, err
	}

	if err := cl.Delete(ctx, m); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, apierrors.NewNotFound(membershipsResource, name)
		}
		return nil, false, err
	}

	// The object is not gone when this returns: the Membership carries a finalizer
	// that removes the role bindings first. Reported as such rather than as a
	// completed delete, so a client that watches sees the real sequence.
	return m, false, nil
}

// guardLastAdmin refuses to remove the last tenant-scope admin of a Tenant.
//
// Not a nicety. Every write to this tier requires an admin, and there is no
// kubectl path into the Tenant workspace to repair it from — so an
// Tenant with zero admins is not degraded, it is unrecoverable: nobody can
// grant a new admin, add a member, or create a Project in it ever again.
//
// The check is a read-then-write and therefore racy: two admins leaving at the
// same instant can both observe the other and both succeed. That is accepted here
// because closing it needs a lock or a serialized write path, and the failure it
// leaves is the one this guard exists to make rare rather than impossible. A
// reconciler that reports an ownerless Tenant is the durable answer.
func (s *MembershipStorage) guardLastAdmin(ctx context.Context, cl ctrlruntimeclient.Client, doomed *pmtenancyv1alpha1.Membership) error {
	if doomed.Spec.Scope != pmtenancyv1alpha1.MembershipScopeTenant ||
		doomed.Spec.Role != pmtenancyv1alpha1.MembershipRoleAdmin {
		return nil
	}

	list := &pmtenancyv1alpha1.MembershipList{}
	if err := cl.List(ctx, list); err != nil {
		return err
	}
	for i := range list.Items {
		m := &list.Items[i]
		if m.Name == doomed.Name || !m.DeletionTimestamp.IsZero() {
			continue
		}
		if m.Spec.Scope == pmtenancyv1alpha1.MembershipScopeTenant && m.Spec.Role == pmtenancyv1alpha1.MembershipRoleAdmin {
			return nil
		}
	}

	return apierrors.NewConflict(membershipsResource, doomed.Name,
		fmt.Errorf("this is the last admin of the tenant; promote another member to admin first, "+
			"because a tenant with no admin cannot be administered again"))
}

// validateMembershipSpec checks what the enum cannot.
func validateMembershipSpec(spec pmtenancyv1alpha1.MembershipSpec) error {
	if spec.User == "" {
		return fmt.Errorf("spec.user is required")
	}
	switch spec.Role {
	case pmtenancyv1alpha1.MembershipRoleAdmin, pmtenancyv1alpha1.MembershipRoleMember, pmtenancyv1alpha1.MembershipRoleViewer:
	default:
		return fmt.Errorf("spec.role must be one of %s, %s, %s",
			pmtenancyv1alpha1.MembershipRoleAdmin, pmtenancyv1alpha1.MembershipRoleMember, pmtenancyv1alpha1.MembershipRoleViewer)
	}
	switch spec.Scope {
	case pmtenancyv1alpha1.MembershipScopeTenant:
		if spec.Project != "" {
			return fmt.Errorf("spec.project must be empty when scope is %s", pmtenancyv1alpha1.MembershipScopeTenant)
		}
	case pmtenancyv1alpha1.MembershipScopeProject:
		if spec.Project == "" {
			return fmt.Errorf("spec.project is required when scope is %s", pmtenancyv1alpha1.MembershipScopeProject)
		}
	default:
		return fmt.Errorf("spec.scope must be %s or %s",
			pmtenancyv1alpha1.MembershipScopeTenant, pmtenancyv1alpha1.MembershipScopeProject)
	}
	return nil
}

// callerOrg resolves one Tenant from the caller's own view.
//
// A 404 rather than a 403 when they are not a member, so this cannot be used to
// discover which Tenant UUIDs exist.
func (s *MembershipStorage) callerOrg(ctx context.Context, tenantUUID string) (*tenantAccess, error) {
	self, err := s.callerName(ctx)
	if err != nil {
		return nil, err
	}
	view, err := resolveAccess(ctx, s.directory, self)
	if err != nil {
		return nil, err
	}
	tenant, ok := view.Tenants[tenantUUID]
	if !ok {
		return nil, apierrors.NewNotFound(organizationsResource, tenantUUID)
	}
	if tenant.ClusterID == "" {
		return nil, apierrors.NewServiceUnavailable(fmt.Sprintf(
			"tenant %s has no workspace yet; retry once it is Ready", tenantUUID))
	}
	return tenant, nil
}

// ConvertToTable renders `kubectl get` output.
func (s *MembershipStorage) ConvertToTable(_ context.Context, object runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "User", Type: "string"},
			{Name: "Scope", Type: "string"},
			{Name: "Project", Type: "string"},
			{Name: "Role", Type: "string"},
			{Name: "Age", Type: "date"},
		},
	}

	add := func(m *pmtenancyv1alpha1.Membership) {
		table.Rows = append(table.Rows, metav1.TableRow{
			Cells: []any{m.Name, m.Spec.User, m.Spec.Scope, m.Spec.Project, m.Spec.Role,
				duration.HumanDuration(timeSince(m.CreationTimestamp))},
			Object: runtime.RawExtension{Object: m},
		})
	}

	switch t := object.(type) {
	case *pmtenancyv1alpha1.Membership:
		add(t)
	case *pmtenancyv1alpha1.MembershipList:
		table.ResourceVersion = t.ResourceVersion
		for i := range t.Items {
			add(&t.Items[i])
		}
	default:
		return nil, fmt.Errorf("cannot render %T as a table", object)
	}
	return table, nil
}

func (s *MembershipStorage) callerName(ctx context.Context) (string, error) {
	claims, err := claimsFrom(ctx)
	if err != nil {
		return "", err
	}
	return identity.UserName(claims.Issuer, claims.Subject)
}

// stampMembershipOrganization records which Tenant a Membership came from.
//
// Derived rather than stored, for the same reason it is on Projects: inside its
// own workspace the answer is implicit, but this API lists across every
// Tenant the caller belongs to, and a flat list with no owner cannot be
// grouped.
func stampMembershipOrganization(m *pmtenancyv1alpha1.Membership, tenantUUID string) {
	if m.Labels == nil {
		m.Labels = map[string]string{}
	}
	m.Labels[pmtenancyv1alpha1.LabelTenant] = tenantUUID
}
