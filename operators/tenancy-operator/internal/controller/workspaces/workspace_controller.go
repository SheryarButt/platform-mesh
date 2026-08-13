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

// Package workspaces does, in tenant workspaces, the work a custom WorkspaceType
// initializer would otherwise have done.
package workspaces

import (
	"context"
	"fmt"
	"time"

	"go.platform-mesh.io/tenancy-operator/internal/config"
	"go.platform-mesh.io/tenancy-operator/pkg/clusters"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// tenantWorkspaceType is the WorkspaceType whose workspaces need the treatment.
// Tenant workspaces do NOT: they extend root:universal, so kcp does the
// work itself.
const tenantWorkspaceType = "workspace"

// reconciler is one step run against a tenant workspace.
//
// Deliberately NOT chain.Reconciler, which the other controllers in this
// operator use: that interface requires chain.Object — GetConditions and
// SetConditions. A kcp Workspace is kcp's type, not ours, so there is no
// condition we may write, nothing to aggregate into a Ready, and no finalizer we
// should be adding. What is left is a step that acts and says whether to come
// back, which is the whole contract here.
type reconciler interface {
	// Name identifies the step. With no condition to report under, this is only
	// for logs and errors — not a public API surface as a chain step's is.
	Name() string

	// child is a client for the tenant workspace's OWN logical cluster, resolved
	// once by the controller and handed in, exactly as chain hands a step the
	// object's own client. Resolving it per step would repeat the lookup and
	// scatter the not-reachable-yet retry policy across every step.
	Reconcile(ctx context.Context, child ctrlruntimeclient.Client, ws *kcptenancyv1alpha1.Workspace) (ctrl.Result, error)
}

// Reconciler prepares tenant workspaces for use.
//
// The steps replace what a custom WorkspaceType initializer would have done. An
// initializer only this operator can clear wedges every new Workspace in
// Initializing whenever the operator is absent; a reconciler degrades the other
// way, leaving the Workspace usable and filling in the rest when we return.
//
// The trade is a window where the Workspace is Ready before those pieces exist —
// transient and self-healing, which "stuck in Initializing" is not.
type Reconciler struct {
	// provisioner spans every workspace binding `tenancy-provisioner`: the tenant
	// fleet root, where Tenant workspaces are created, and each Tenant
	// workspace, where tenant Workspaces are. That is where Workspace objects are
	// readable.
	provisioner mcmanager.Manager

	// access spans every workspace binding `tenancy-access` — that is, the tenant
	// workspaces themselves. Writes go here, bounded by that export's claims.
	// Reading Workspaces and writing into them are deliberately two different
	// exports with two different claim lists, which is why this is a second
	// manager rather than a reuse of provisioner.
	access mcmanager.Manager

	cfg *config.OperatorConfig

	steps []reconciler
}

// NewReconciler wires the reconciler to the two managers it needs.
func NewReconciler(provisioner, access mcmanager.Manager, cfg *config.OperatorConfig) (*Reconciler, error) {
	if provisioner == nil {
		return nil, fmt.Errorf("a provisioner manager is required")
	}
	if access == nil {
		return nil, fmt.Errorf("an access manager is required")
	}
	return &Reconciler{
		provisioner: provisioner,
		access:      access,
		cfg:         cfg,
		steps:       []reconciler{&defaultNamespace{}},
	}, nil
}

// Reconcile resolves one tenant Workspace and runs the steps against it.
//
// Everything up to the step loop is about whether there is anything to act on at
// all: the right kind of workspace, a logical cluster that exists, and a client
// that can reach it. A step is only ever called when all three hold, so no step
// has to re-establish them.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	cl, err := clusters.ClientForCluster(ctx, r.provisioner, string(req.ClusterName))
	if err != nil {
		return ctrl.Result{}, err
	}

	ws := &kcptenancyv1alpha1.Workspace{}
	if err := cl.Get(ctx, req.NamespacedName, ws); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Tenant workspaces come through this same export. They extend
	// root:universal and are already complete, so acting on them would be a write
	// we do not need and a claim we would rather not exercise.
	if ws.Spec.Type == nil || ws.Spec.Type.Name != tenantWorkspaceType {
		return ctrl.Result{}, nil
	}

	// Before Ready there is no logical cluster to write into. kcp's own
	// apibindings initializer is still running, so this is the normal path on
	// first sight rather than an error; the watch brings us back.
	if ws.Status.Phase != kcpcorev1alpha1.LogicalClusterPhaseReady {
		return ctrl.Result{}, nil
	}
	if ws.Spec.Cluster == "" {
		// Ready without a cluster ID should not happen; requeue rather than treat
		// it as terminal, because the alternative is a workspace left unprepared
		// with nothing left to retry.
		log.Info("workspace is Ready but carries no cluster ID; retrying", "workspace", ws.Name)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// The child's own logical cluster, reached through tenancy-access. It appears
	// there only because the tenant WorkspaceType binds that export by default —
	// no binding, no reach, which is the property that keeps this operator's
	// access declared rather than ambient.
	child, err := clusters.ClientForCluster(ctx, r.access, ws.Spec.Cluster)
	if err != nil {
		// The binding may not have landed yet even though the Workspace is Ready.
		log.V(1).Info("tenant workspace not reachable through tenancy-access yet; retrying",
			"workspace", ws.Name, "cluster", ws.Spec.Cluster, "error", err.Error())
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	var out ctrl.Result
	for _, step := range r.steps {
		res, err := step.Reconcile(ctx, child, ws)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("%s: %w", step.Name(), err)
		}
		// Steps are independent, so one asking to be retried must not stop the
		// others — the soonest request wins and they all run again together.
		out = soonest(out, res)
	}
	return out, nil
}

// soonest merges two requeue requests, keeping the earlier one.
func soonest(a, b ctrl.Result) ctrl.Result {
	if b.RequeueAfter == 0 {
		return a
	}
	if a.RequeueAfter == 0 || b.RequeueAfter < a.RequeueAfter {
		return b
	}
	return a
}

// ControllerName names the controller.
const ControllerName = "WorkspaceReconciler"

// SetupWithManager registers the controller on the PROVISIONER manager, because
// that is where Workspace objects are readable. It writes through the access
// manager, which is engaged separately.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	return mcbuilder.ControllerManagedBy(mgr).
		Named(ControllerName).
		For(&kcptenancyv1alpha1.Workspace{}).
		Complete(r)
}
