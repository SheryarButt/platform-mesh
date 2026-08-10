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
	"strings"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// tenantAccess is what one caller may see in one Tenant.
type tenantAccess struct {
	// UUID and ClusterID name the Tenant and where its objects live.
	UUID      string
	ClusterID string

	// Role is the strongest role the caller holds anywhere in this Tenant.
	Role string

	// AllProjects is true when a tenant-scope Membership grants the caller every
	// Project in it. Otherwise Projects holds the specific ones.
	AllProjects bool
	Projects    map[string]struct{}
}

// access is the caller's whole view of the fleet.
type access struct {
	// Tenants, keyed by Tenant UUID.
	Tenants map[string]*tenantAccess
}

// resolveAccess reads the caller's membership index and turns it into what they
// may see.
//
// The index is the read model for exactly this question: "which Tenants
// and Projects do I belong to", answerable without fanning out over the fleet. It
// is NOT an authorization decision about what they may DO — kcp
// RBAC decides that inside each workspace, and if the two ever disagree the index
// is what is wrong.
//
// What it does decide is VISIBILITY here, which is why a caller with no index
// sees an empty list rather than an error: having no memberships is the normal
// state of a brand-new identity, not a failure.
func resolveAccess(ctx context.Context, directory ctrlruntimeclient.Client, userName string, groups ...string) (*access, error) {
	a := &access{Tenants: map[string]*tenantAccess{}}

	index := &pmtenancyv1alpha1.UserMembershipIndex{}
	if err := directory.Get(ctx, ctrlruntimeclient.ObjectKey{Name: userName}, index); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		// No memberships of their own. Not an error, and NOT the end: a caller can
		// hold everything they can reach through a group and have no index at all,
		// which is the normal state under a group-driven installation.
	}

	entries := index.Spec.Entries

	// The group half, read from the groups on the CALLER'S OWN TOKEN.
	//
	// This is the whole reason group access is indexed by group rather than fanned
	// out onto members: the membership half of the answer arrives with the request
	// and is never stored, so removing someone from a group at the identity
	// provider takes their access away on the next token, with nothing here to
	// invalidate and nothing to go stale. A stored copy — status.groups, say —
	// would be an access-control input that outlives its own revocation.
	for _, g := range groups {
		name, err := identity.GroupName(g)
		if err != nil {
			// An empty group name. Nothing to look up, and not worth failing a whole
			// listing over.
			continue
		}
		gmi := &pmtenancyv1alpha1.GroupMembershipIndex{}
		if err := directory.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name}, gmi); err != nil {
			if apierrors.IsNotFound(err) {
				// A group that has been granted nothing. By far the common case — a
				// caller carries every group their provider emits, and only some of
				// them mean anything here.
				continue
			}
			return nil, err
		}
		entries = append(entries, gmi.Spec.Entries...)
	}

	for _, e := range entries {
		if e.TenantUUID == "" {
			continue
		}

		tenant, ok := a.Tenants[e.TenantUUID]
		if !ok {
			tenant = &tenantAccess{
				UUID:     e.TenantUUID,
				Projects: map[string]struct{}{},
			}
			a.Tenants[e.TenantUUID] = tenant
		}
		if e.TenantClusterID != "" {
			tenant.ClusterID = e.TenantClusterID
		}
		// A user can hold several Memberships in one tenant — a tenant-scope row and a
		// per-project one — and the tenant view must report the strongest. Compared by
		// rank rather than "admin wins, else first seen": with three tiers the
		// first-seen arm turns iteration order into an access decision, so a viewer
		// row landing before a member row would silently under-report. Rank also
		// leaves an unrecognised role at 0, so it can never displace a known one.
		if pmtenancyv1alpha1.MembershipRoleRank(e.Role) > pmtenancyv1alpha1.MembershipRoleRank(tenant.Role) {
			tenant.Role = e.Role
		}

		if e.ProjectUUID == "" {
			// A tenant-scope row. Tenant membership carries access to every Project in
			// the tenant, which is the same implication the Membership reconciler
			// materializes as one role binding per Project.
			tenant.AllProjects = true
			continue
		}
		tenant.Projects[e.ProjectUUID] = struct{}{}
	}

	return a, nil
}

// resolveCallerAccess resolves the authenticated caller and their whole view in
// one step, and returns the caller's User name alongside it.
func resolveCallerAccess(ctx context.Context, directory ctrlruntimeclient.Client) (string, *access, error) {
	claims, err := claimsFrom(ctx)
	if err != nil {
		return "", nil, err
	}
	self, err := identity.UserName(claims.Issuer, claims.Subject)
	if err != nil {
		return "", nil, apierrors.NewBadRequest(err.Error())
	}
	view, err := resolveAccess(ctx, directory, self, grantableGroups(claims.Groups)...)
	if err != nil {
		return "", nil, err
	}
	return self, view, nil
}

// grantableGroups drops the groups a Membership must never be able to name.
func grantableGroups(groups []string) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if g == "" || strings.HasPrefix(g, "system:") {
			continue
		}
		out = append(out, g)
	}
	return out
}

// CanSeeProject reports whether the caller may see one Project of this tenant.
func (o *tenantAccess) CanSeeProject(uuid string) bool {
	if o.AllProjects {
		return true
	}
	_, ok := o.Projects[uuid]
	return ok
}
