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

// Package projects reconciles Projects — the tenant-facing work tier.
//
// A Project is the only "place where work happens" a client ever names; the kcp
// Workspace behind it is an implementation detail this package owns. Projects
// nest, and every Project object lives in its Tenant's workspace no matter
// how deep it sits, so listing a tenant's whole tree is one call.
package projects

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
const ControllerName = "ProjectReconciler"

// Reconciler materializes the workspace behind each Project.
type Reconciler struct {
	// tenancy is where Projects are read from: they live in Tenant
	// workspaces, which bind the `tenancy` export.
	tenancy mcmanager.Manager
	steps   []chain.Reconciler[*pmtenancyv1alpha1.Project]
}

// NewReconciler assembles the Project chain.
func NewReconciler(provisioner, tenancy mcmanager.Manager, layout paths.Layout, cfg config.OperatorConfig) (*Reconciler, error) {
	var steps []chain.Reconciler[*pmtenancyv1alpha1.Project]

	if cfg.Reconcilers.ProjectWorkspace.Enabled {
		steps = append(steps, &projectWorkspace{provisioner: provisioner, layout: layout, cfg: cfg.Tenancy})
	}
	// LAST, so it finalizes FIRST: chain.RunFinalize walks the steps in reverse, and
	// the grants naming this Project have to be revoked while the workspace they
	// point at still exists.
	//
	// Not gated by config, unlike the steps above. A cleanup path you can switch off
	// is a leak you can switch on, and the objects it strands are grants — the one
	// kind of leftover that is worth more than tidiness.
	steps = append(steps, &projectMemberships{})

	return &Reconciler{tenancy: tenancy, steps: steps}, nil
}

// SetupWithManager registers the controller on the tenancy manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager, cfg *platformmeshconfig.CommonServiceConfig, eventPredicates ...predicate.Predicate) error {
	opts := controller.TypedOptions[mcreconcile.Request]{
		MaxConcurrentReconciles: cfg.MaxConcurrentReconciles,
	}
	predicates := append([]predicate.Predicate{filter.DebugResourcesBehaviourPredicate(cfg.DebugLabelValue)}, eventPredicates...)
	return mcbuilder.ControllerManagedBy(mgr).
		Named(ControllerName).
		For(&pmtenancyv1alpha1.Project{}).
		WithOptions(opts).
		WithEventFilter(predicate.And(predicates...)).
		Complete(r)
}

// Reconcile fetches the Project and runs the chain over it.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := clusters.ClientForCluster(ctx, r.tenancy, string(req.ClusterName))
	if err != nil {
		return ctrl.Result{}, err
	}

	proj := &pmtenancyv1alpha1.Project{}
	if err := cl.Get(ctx, req.NamespacedName, proj); err != nil {
		return ctrl.Result{}, chain.IgnoreNotFound(err)
	}
	original := proj.DeepCopy()

	if !proj.DeletionTimestamp.IsZero() {
		requeue, ferr := chain.RunFinalize(ctx, cl, proj, r.steps)
		if err := chain.Commit(ctx, cl, original, proj); err != nil {
			return ctrl.Result{}, err
		}
		return chain.RequeueResult(requeue), ferr
	}

	if chain.EnsureFinalizers(proj, r.steps) {
		return ctrl.Result{}, cl.Patch(ctx, proj, ctrlruntimeclient.MergeFrom(original))
	}

	requeue, cerr := chain.Run(ctx, cl, proj, r.steps)
	chain.SetReady(proj, pmtenancyv1alpha1.ProjectConditionReady, r.steps)

	if err := chain.Commit(ctx, cl, original, proj); err != nil {
		return ctrl.Result{}, err
	}
	return chain.RequeueResult(requeue), cerr
}
