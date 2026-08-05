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

package tenants

import (
	"context"
	"fmt"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"

	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// tenantIndexFinalizer keeps the Tenant around long enough to prune its rows.
// Without it a deleted Tenant would leave every member's index showing
// something they can no longer reach.
const tenantIndexFinalizer = "tenant.tenancy.platform-mesh.io/index"

// tenantIndex prunes this Tenant's rows from every index when it goes away.
//
// IT WRITES NO ROWS. Every row corresponds to one Membership, and the Membership
// reconciler owns it — including the first admin's, which this step used to write.
// Two writers on one row was survivable only while roles were immutable: this step
// hardcoded `admin`, so once `memberships set-role` could demote the owner, the two
// would have overwritten each other's answer on every pass, forever.
//
// The prune stays here because it cannot be done from the other side. Deleting an
// Tenant tears down the workspace its Memberships live in, so those objects
// stop being servable before their own finalizers can run — leaving every member's
// index advertising a tenant that no longer exists. This step sweeps all of them
// in one pass, from the tier that outlives them.
//
// The index is not a gate — kcp RBAC decides what a tenant may do. It exists so a
// client can list the caller's tenants without fanning out over the fleet. If the
// two disagree, RBAC wins and the index is what is wrong: it can be rebuilt.
type tenantIndex struct{}

func (r *tenantIndex) Name() string { return pmtenancyv1alpha1.TenantConditionIndexSynced }

func (r *tenantIndex) FinalizerName() string { return tenantIndexFinalizer }

// Reconcile does nothing but hold the finalizer that Finalize needs.
//
// Reported True rather than skipped, because the condition's meaning is "this
// Tenant's rows are accounted for" — and they are: by the Memberships that
// own them, plus the prune below. A step that left it False would make every
// healthy Tenant look stuck.
func (r *tenantIndex) Reconcile(_ context.Context, _ ctrlruntimeclient.Client, tenant *pmtenancyv1alpha1.Tenant) (chain.Status, error) {
	chain.MarkTrue(tenant, r.Name())
	return chain.Continue, nil
}

// finalize removes this Tenant's rows from every index that carries them.
func (r *tenantIndex) Finalize(ctx context.Context, cl ctrlruntimeclient.Client, tenant *pmtenancyv1alpha1.Tenant) (chain.Status, error) {
	list := &pmtenancyv1alpha1.UserMembershipIndexList{}
	if err := cl.List(ctx, list); err != nil {
		return chain.StopAndRequeue, fmt.Errorf("listing membership indexes: %w", err)
	}

	for i := range list.Items {
		umi := &list.Items[i]
		kept := make([]pmtenancyv1alpha1.MembershipIndexEntry, 0, len(umi.Spec.Entries))
		for _, e := range umi.Spec.Entries {
			if e.TenantUUID != tenant.Name {
				kept = append(kept, e)
			}
		}
		if len(kept) == len(umi.Spec.Entries) {
			continue
		}
		umi.Spec.Entries = kept
		if err := cl.Update(ctx, umi); err != nil {
			return chain.StopAndRequeue, fmt.Errorf("pruning index %q: %w", umi.Name, err)
		}

		// entryCount is a separate subresource write. Skipping it leaves an index
		// reporting rows it no longer has — which reads as "you still belong to
		// something" long after the Tenant is gone.
		umi.Status.EntryCount = int32(len(kept)) //nolint:gosec // entry count cannot realistically overflow int32
		umi.Status.ObservedGeneration = umi.Generation
		if err := cl.Status().Update(ctx, umi); err != nil {
			return chain.StopAndRequeue, fmt.Errorf("updating index status for %q: %w", umi.Name, err)
		}
	}

	return chain.Continue, nil
}
