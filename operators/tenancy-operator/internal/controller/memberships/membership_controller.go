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

// Package memberships turns a Membership into the RBAC that actually grants it.
//
// This is the join between the two halves of the model. A Membership is the
// RECORD of who may do what; kcp role bindings are what is ENFORCED. Nothing
// consults the membership index at request time, so until this runs, a User with
// a Membership saying `role: admin` is refused in their own workspace — correctly,
// because nothing named their identity.
package memberships

import (
	"context"
	"time"

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
	ctrlreconcile "sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/kcp-dev/logicalcluster/v3"
)

// ControllerName names the controller.
const ControllerName = "MembershipReconciler"

// resyncInterval is how often a settled Membership re-checks its role bindings.
//
// This controller writes objects nothing watches, so without a periodic pass
// anything that removes a binding leaves a Membership reporting RBACApplied=True
// over a grant that no longer exists.
//
// A slow repair is acceptable because the drift fails CLOSED: a deleted binding
// costs the owner a 403, not an escalation. The dangerous direction — a grant
// outliving its Membership — is handled synchronously by the finalizer.
const resyncInterval = 5 * time.Minute

// Reconciler projects Memberships into RBAC.
type Reconciler struct {
	// tenancy is where Memberships are READ from: they live in Tenant
	// workspaces, which bind the `tenancy` export. Writes go elsewhere.
	tenancy mcmanager.Manager
	steps   []chain.Reconciler[*pmtenancyv1alpha1.Membership]
}

// NewReconciler assembles the Membership chain.
func NewReconciler(platform, tenancy, access mcmanager.Manager, layout paths.Layout, resolver *identity.Resolver, cfg config.OperatorConfig) (*Reconciler, error) {
	var steps []chain.Reconciler[*pmtenancyv1alpha1.Membership]

	if cfg.Reconcilers.MembershipRBAC.Enabled {
		steps = append(steps, &applyRBAC{
			platform: platform,
			access:   access,
			resolver: resolver,
			layout:   layout,
		})
	}
	// After the RBAC, and the order is the contract: the index is what a client
	// lists Projects from, so a row written before the binding backing it exists
	// advertises a workspace that 403s when opened.
	if cfg.Reconcilers.Index.Enabled {
		steps = append(steps, &membershipIndex{platform: platform, layout: layout})
	}

	return &Reconciler{tenancy: tenancy, steps: steps}, nil
}

// SetupWithManager registers the controller on the TENANCY manager, which is the
// one that sees Membership objects.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager, cfg *platformmeshconfig.CommonServiceConfig, eventPredicates ...predicate.Predicate) error {
	opts := controller.TypedOptions[mcreconcile.Request]{
		MaxConcurrentReconciles: cfg.MaxConcurrentReconciles,
	}
	predicates := append([]predicate.Predicate{filter.DebugResourcesBehaviourPredicate(cfg.DebugLabelValue)}, eventPredicates...)
	return mcbuilder.ControllerManagedBy(mgr).
		Named(ControllerName).
		For(&pmtenancyv1alpha1.Membership{}).
		// A tenant-scope Membership grants admin in EVERY Project of the tenant, and
		// that implication is materialized as one binding per Project. A new
		// Project therefore needs the tenant's Memberships re-applied — without this
		// watch, a tenant admin is silently locked out of every Project created after
		// their Membership was written.
		Watches(&pmtenancyv1alpha1.Project{}, mchandler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, obj ctrlruntimeclient.Object) []ctrlreconcile.Request {
				return tenantMembershipRequests(ctx, mgr, obj)
			})).
		WithOptions(opts).
		WithEventFilter(predicate.And(predicates...)).
		Complete(r)
}

// Reconcile fetches the Membership and runs the chain over it.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := clusters.ClientForCluster(ctx, r.tenancy, string(req.ClusterName))
	if err != nil {
		return ctrl.Result{}, err
	}

	m := &pmtenancyv1alpha1.Membership{}
	if err := cl.Get(ctx, req.NamespacedName, m); err != nil {
		return ctrl.Result{}, chain.IgnoreNotFound(err)
	}
	original := m.DeepCopy()

	if !m.DeletionTimestamp.IsZero() {
		requeue, ferr := chain.RunFinalize(ctx, cl, m, r.steps)
		if err := chain.Commit(ctx, cl, original, m); err != nil {
			return ctrl.Result{}, err
		}
		return chain.RequeueResult(requeue), ferr
	}

	// The finalizer must be persisted BEFORE the binding is written, or a delete
	// arriving in between leaves a grant nothing points at.
	if chain.EnsureFinalizers(m, r.steps) {
		return ctrl.Result{}, cl.Patch(ctx, m, ctrlruntimeclient.MergeFrom(original))
	}

	requeue, cerr := chain.Run(ctx, cl, m, r.steps)
	chain.SetReady(m, pmtenancyv1alpha1.MembershipConditionReady, r.steps)

	if err := chain.Commit(ctx, cl, original, m); err != nil {
		return ctrl.Result{}, err
	}

	result := chain.RequeueResult(requeue)
	if result.RequeueAfter == 0 && cerr == nil {
		// Settled. Come back anyway — see resyncInterval.
		result.RequeueAfter = resyncInterval
	}
	return result, cerr
}

// tenantMembershipRequests maps a Project to the tenant-scope Memberships that must
// cover it.
//
// Project-scope Memberships are deliberately NOT returned: they name one Project
// each and are already reconciled by their own events. Only the tenant-scope ones
// have an implication that widens when a Project appears.
func tenantMembershipRequests(ctx context.Context, mgr mcmanager.Manager, obj ctrlruntimeclient.Object) []ctrlreconcile.Request {
	cluster := logicalcluster.From(obj)
	if cluster.Empty() {
		return nil
	}

	cl, err := clusters.ClientForCluster(ctx, mgr, cluster.String())
	if err != nil {
		// The next Project event, or the Membership's own resync, catches up.
		return nil
	}

	list := &pmtenancyv1alpha1.MembershipList{}
	if err := cl.List(ctx, list); err != nil {
		return nil
	}

	var reqs []ctrlreconcile.Request
	for i := range list.Items {
		if list.Items[i].Spec.Scope != pmtenancyv1alpha1.MembershipScopeTenant {
			continue
		}
		reqs = append(reqs, ctrlreconcile.Request{
			NamespacedName: ctrlruntimeclient.ObjectKeyFromObject(&list.Items[i]),
		})
	}
	return reqs
}
