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
	"go.platform-mesh.io/tenancy-operator/pkg/clusters"
	"go.platform-mesh.io/tenancy-operator/pkg/membership"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// ownerMembership gives the Tenant's first admin their tenant-scope Membership.
//
// Without this the bootstrap produces a Tenant whose owner cannot reach
// it: the membership index says `role: admin`, but no Membership exists, so
// nothing ever writes a role binding and kcp refuses — correctly, since nothing
// named that identity. The index is not a gate (its own doc says so), and this
// step is what stops it from disagreeing with the thing that is.
type ownerMembership struct {
	// tenancy reads and writes through the `tenancy` export, which every
	// Tenant workspace binds by default and which owns `memberships`.
	// Neither the platform nor the provisioner export can reach them.
	tenancy mcmanager.Manager
}

func (r *ownerMembership) Name() string {
	return pmtenancyv1alpha1.TenantConditionOwnerMembershipReady
}

func (r *ownerMembership) Reconcile(ctx context.Context, _ ctrlruntimeclient.Client, tenant *pmtenancyv1alpha1.Tenant) (chain.Status, error) {
	if tenant.Status.FirstAdmin == "" {
		// Nobody owns this Tenant yet; the User chain records that first.
		chain.MarkFalse(tenant, r.Name(), "Pending", "no first admin recorded yet")
		return chain.Stop, nil
	}
	if tenant.Status.ClusterID == "" {
		// Memberships live INSIDE the Tenant's workspace, so there is
		// nowhere to put one until that workspace is Ready.
		chain.MarkFalse(tenant, r.Name(), "Pending", "Tenant workspace is not ready yet")
		return chain.Stop, nil
	}

	cl, err := clusters.ClientForCluster(ctx, r.tenancy, tenant.Status.ClusterID)
	if err != nil {
		// The binding is created with the workspace, and the provider's cache warms
		// asynchronously. A wait, not a failure.
		chain.MarkFalse(tenant, r.Name(), "Pending", "Tenant workspace not reachable through the tenancy export yet")
		//nolint:nilerr // deliberate: not-yet-reachable is a wait, not a failure
		return chain.StopAndRequeue, nil
	}

	name := membership.Name(tenant.Status.FirstAdmin, pmtenancyv1alpha1.MembershipScopeTenant, "")
	m := &pmtenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: pmtenancyv1alpha1.MembershipSpec{
			User:  tenant.Status.FirstAdmin,
			Scope: pmtenancyv1alpha1.MembershipScopeTenant,
			Role:  pmtenancyv1alpha1.MembershipRoleAdmin,
		},
	}

	// Create, not CreateOrUpdate: this seeds the owner's grant once. If they later
	// hand admin to someone else and remove their own row, re-imposing it would
	// undo a deliberate act — the same reasoning that makes the Tenant
	// itself one-shot.
	if err := cl.Create(ctx, m); err != nil && !apierrors.IsAlreadyExists(err) {
		err = fmt.Errorf("creating the owner Membership in Tenant %s: %w", tenant.Name, err)
		chain.MarkFalse(tenant, r.Name(), "Error", err.Error())
		return chain.StopAndRequeue, err
	}

	chain.MarkTrue(tenant, r.Name())
	return chain.Continue, nil
}
