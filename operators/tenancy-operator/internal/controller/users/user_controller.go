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

// Package users reconciles the User half of the bootstrap state machine.
package users

import (
	"context"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	platformmeshconfig "go.platform-mesh.io/golang-commons/config"
	"go.platform-mesh.io/golang-commons/controller/filter"
	"go.platform-mesh.io/tenancy-operator/internal/config"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"
	"go.platform-mesh.io/tenancy-operator/pkg/clusters"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"
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
const ControllerName = "UserReconciler"

// Reconciler reacts to a User appearing in the directory workspace.
//
// A User is materialized by an explicit `create users` against the tenancy virtual
// workspace — nothing in the login path provisions it, because nothing in the
// login path is ours. This controller is what happens next, asynchronously: a
// brand-new user is authenticated but holds no membership for a few seconds, and
// nobody blocks on it. The client watches, and rows appear as they become Ready.
type Reconciler struct {
	mgr   mcmanager.Manager
	steps []chain.Reconciler[*pmtenancyv1alpha1.User]
}

// NewReconciler assembles the User bootstrap chain.
//
// provisioner reads the `tenancy-provisioner` export, which is how the seed step
// reaches inside a Tenant's own workspace to create a child Workspace.
func NewReconciler(mgr, provisioner, tenancy mcmanager.Manager, layout paths.Layout, resolver *identity.Resolver, cfg config.OperatorConfig) (*Reconciler, error) {
	// Resolved once here rather than per step: an unknown strategy is a
	// configuration error and belongs at construction, not on the first User.
	strategy, err := cfg.NamingStrategy()
	if err != nil {
		return nil, err
	}

	var steps []chain.Reconciler[*pmtenancyv1alpha1.User]

	// First: everything downstream reads spec.rbacIdentity, so it must be current
	// before anything is built from it.
	steps = append(steps, &rbacIdentity{resolver: resolver})

	// Order is the contract: there is nowhere to seed a Project until the
	// personal Tenant and its workspace exist.
	if cfg.Reconcilers.SeedTenant.Enabled {
		steps = append(steps, &seedTenant{cfg: cfg.Tenancy, naming: strategy})
	}
	if cfg.Reconcilers.SeedProject.Enabled {
		steps = append(steps, &seedProject{tenancy: tenancy, layout: layout, cfg: cfg.Tenancy, naming: strategy})
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
		For(&pmtenancyv1alpha1.User{}).
		WithOptions(opts).
		WithEventFilter(predicate.And(predicates...)).
		Complete(r)
}

// Reconcile fetches the User and runs the chain over it.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := clusters.ClientForCluster(ctx, r.mgr, string(req.ClusterName))
	if err != nil {
		return ctrl.Result{}, err
	}

	user := &pmtenancyv1alpha1.User{}
	if err := cl.Get(ctx, req.NamespacedName, user); err != nil {
		return ctrl.Result{}, chain.IgnoreNotFound(err)
	}
	original := user.DeepCopy()

	if !user.DeletionTimestamp.IsZero() {
		requeue, ferr := chain.RunFinalize(ctx, cl, user, r.steps)
		if err := chain.Commit(ctx, cl, original, user); err != nil {
			return ctrl.Result{}, err
		}
		return chain.RequeueResult(requeue), ferr
	}

	if chain.EnsureFinalizers(user, r.steps) {
		return ctrl.Result{}, cl.Patch(ctx, user, ctrlruntimeclient.MergeFrom(original))
	}

	requeue, cerr := chain.Run(ctx, cl, user, r.steps)
	chain.SetReady(user, pmtenancyv1alpha1.UserConditionReady, r.steps)

	if err := chain.Commit(ctx, cl, original, user); err != nil {
		return ctrl.Result{}, err
	}
	return chain.RequeueResult(requeue), cerr
}
