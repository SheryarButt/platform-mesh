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

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"

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
func resolveAccess(ctx context.Context, directory ctrlruntimeclient.Client, userName string) (*access, error) {
	a := &access{Tenants: map[string]*tenantAccess{}}

	index := &pmtenancyv1alpha1.UserMembershipIndex{}
	if err := directory.Get(ctx, ctrlruntimeclient.ObjectKey{Name: userName}, index); err != nil {
		if apierrors.IsNotFound(err) {
			// No memberships yet. An empty view, not an error.
			return a, nil
		}
		return nil, err
	}

	for _, e := range index.Spec.Entries {
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

// CanSeeProject reports whether the caller may see one Project of this tenant.
func (o *tenantAccess) CanSeeProject(uuid string) bool {
	if o.AllProjects {
		return true
	}
	_, ok := o.Projects[uuid]
	return ok
}
