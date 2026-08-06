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

// Package tenants reconciles the Tenant half of the bootstrap state
// machine: materialize the kcp workspace, then index it.
package tenants

import (
	"context"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	platformmeshconfig "go.platform-mesh.io/golang-commons/config"
	"go.platform-mesh.io/golang-commons/controller/filter"
	"go.platform-mesh.io/tenancy-operator/internal/config"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"
	"go.platform-mesh.io/tenancy-operator/pkg/clusters"
	"go.platform-mesh.io/tenancy-operator/pkg/paths"

	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// ControllerName names the controller.
const ControllerName = "TenantReconciler"

// Reconciler runs the Tenant half of the bootstrap state machine.
//
// The steps run in order and each reports its own condition, so an observer can
// see exactly which step a stalled Tenant is on rather than a single opaque
// Ready=False.
type Reconciler struct {
	mgr   mcmanager.Manager
	steps []chain.Reconciler[*pmtenancyv1alpha1.Tenant]
}

// NewReconciler assembles the Tenant chain.
//
// provisioner is the manager reading the `tenancy-provisioner` export, which is
// the only path into the fleet root where Tenant workspaces are created.
func NewReconciler(mgr, provisioner, tenancy mcmanager.Manager, layout paths.Layout, cfg config.OperatorConfig) (*Reconciler, error) {
	var steps []chain.Reconciler[*pmtenancyv1alpha1.Tenant]

	// Order is the contract: the index row carries the workspace's cluster ID, so
	// it cannot be written before the workspace exists.
	if cfg.Reconcilers.TenantWorkspace.Enabled {
		steps = append(steps, &tenantWorkspace{provisioner: provisioner, layout: layout, cfg: cfg.Tenancy})
	}
	// Between the two on purpose: the Membership needs the workspace to exist, and
	// the index row should not claim `role: admin` before the grant backing it
	// does.
	if cfg.Reconcilers.OwnerMembership.Enabled {
		steps = append(steps, &ownerMembership{tenancy: tenancy})
	}
	if cfg.Reconcilers.Index.Enabled {
		steps = append(steps, &tenantIndex{})
	}

	return &Reconciler{mgr: mgr, steps: steps}, nil
}

// SetupWithManager registers the controller.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager, cfg *platformmeshconfig.CommonServiceConfig, eventPredicates ...predicate.Predicate) error {
	opts := controller.TypedOptions[mcreconcile.Request]{
		MaxConcurrentReconciles: cfg.MaxConcurrentReconciles,
	}
	predicates := append([]predicate.Predicate{filter.DebugResourcesBehaviourPredicate(cfg.DebugLabelValue)}, eventPredicates...)
	return mcbuilder.ControllerManagedBy(mgr).
		Named(ControllerName).
		For(&pmtenancyv1alpha1.Tenant{}).
		WithOptions(opts).
		WithEventFilter(predicate.And(predicates...)).
		Complete(r)
}

// Reconcile fetches the Tenant and runs the chain over it.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := clusters.ClientForCluster(ctx, r.mgr, string(req.ClusterName))
	if err != nil {
		return ctrl.Result{}, err
	}

	tenant := &pmtenancyv1alpha1.Tenant{}
	if err := cl.Get(ctx, req.NamespacedName, tenant); err != nil {
		return ctrl.Result{}, chain.IgnoreNotFound(err)
	}
	original := tenant.DeepCopy()

	if !tenant.DeletionTimestamp.IsZero() {
		requeue, ferr := chain.RunFinalize(ctx, cl, tenant, r.steps)
		if err := chain.Commit(ctx, cl, original, tenant); err != nil {
			return ctrl.Result{}, err
		}
		return chain.RequeueResult(requeue), ferr
	}

	// Finalizers must land before any step creates external state, so this
	// returns immediately after adding them: the resulting watch event brings us
	// straight back with the object persisted.
	if chain.EnsureFinalizers(tenant, r.steps) {
		return ctrl.Result{}, cl.Patch(ctx, tenant, ctrlruntimeclient.MergeFrom(original))
	}

	requeue, cerr := chain.Run(ctx, cl, tenant, r.steps)
	chain.SetReady(tenant, pmtenancyv1alpha1.TenantConditionReady, r.steps)

	if err := chain.Commit(ctx, cl, original, tenant); err != nil {
		return ctrl.Result{}, err
	}
	return chain.RequeueResult(requeue), cerr
}
