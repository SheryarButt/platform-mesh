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

package memberships

import (
	"context"
	"strings"
	"time"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	platformmeshconfig "go.platform-mesh.io/golang-commons/config"
	"go.platform-mesh.io/tenancy-operator/pkg/clusters"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

const (
	// labelMembership records which Membership a role binding was written for.
	labelMembership = "tenancy.platform-mesh.io/membership"

	// bindingFinalizer keeps a binding readable long enough to notice it is going.
	bindingFinalizer = "membership.tenancy.platform-mesh.io/binding"

	// annotationRepairedAt nudges a Membership by changing it.
	//
	// A write is the only cross-manager signal available: this controller watches
	// through the tenancy-access export while the Membership controller watches
	// through `tenancy`, and a builder cannot take a source from another manager.
	// Touching the object turns "a binding vanished" into an ordinary Membership
	// event, which the existing reconciler already knows how to answer.
	annotationRepairedAt = "tenancy.platform-mesh.io/binding-repair-requested-at"
)

// BindingControllerName names the controller.
const BindingControllerName = "MembershipBindingReconciler"

// BindingReconciler repairs role bindings the moment one is removed.
//
// The Membership controller writes bindings into project workspaces and, on its
// own, would never learn they were deleted: nothing it watches changes. That left
// revocation-by-accident — a stray kubectl delete, a restore, a tenant admin
// tidying up — silently un-granting a Membership that still reports Ready.
//
// This closes it by watching the bindings themselves and nudging the owning
// Membership, which is the object that knows how to rebuild them.
type BindingReconciler struct {
	// access spans the workspaces bindings live in.
	access mcmanager.Manager

	// tenancy reaches the Tenant workspace the Membership lives in.
	tenancy mcmanager.Manager
}

// NewBindingReconciler returns the controller.
func NewBindingReconciler(access, tenancy mcmanager.Manager) *BindingReconciler {
	return &BindingReconciler{access: access, tenancy: tenancy}
}

// SetupWithManager registers it on the ACCESS manager, which is where role
// bindings are visible.
func (r *BindingReconciler) SetupWithManager(mgr mcmanager.Manager, cfg *platformmeshconfig.CommonServiceConfig) error {
	opts := controller.TypedOptions[mcreconcile.Request]{
		MaxConcurrentReconciles: cfg.MaxConcurrentReconciles,
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named(BindingControllerName).
		For(&rbacv1.ClusterRoleBinding{}).
		// Only ours. Every workspace has RBAC of its own and watching all of it
		// would mean reconciling a tenant's objects on every change they make.
		WithEventFilter(ownedBindings()).
		WithOptions(opts).
		Complete(r)
}

// ownedBindings keeps only the bindings this platform wrote.
func ownedBindings() predicate.Predicate {
	owned := func(obj ctrlruntimeclient.Object) bool {
		return obj != nil && strings.HasPrefix(obj.GetName(), bindingNamePrefix)
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return owned(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return owned(e.ObjectNew) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return owned(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return owned(e.Object) },
	}
}

// Reconcile releases a deleting binding and asks its Membership to rebuild it.
func (r *BindingReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := clusters.ClientForCluster(ctx, r.access, string(req.ClusterName))
	if err != nil {
		return ctrl.Result{}, err
	}

	binding := &rbacv1.ClusterRoleBinding{}
	if err := cl.Get(ctx, req.NamespacedName, binding); err != nil {
		// Already gone — the finalizer is what normally prevents this, so there is
		// nothing left to read and nothing to do. The Membership controller's
		// periodic pass is the backstop for that case.
		return ctrl.Result{}, ctrlruntimeclient.IgnoreNotFound(err)
	}

	if binding.DeletionTimestamp.IsZero() {
		// Present and not going anywhere. Content drift is the Membership
		// controller's business, not this one's — reacting to every update here
		// would react to our own writes.
		return ctrl.Result{}, nil
	}

	// Being deleted, and still readable BECAUSE of the finalizer. This is the one
	// moment the back-reference exists, so read it before letting go.
	membership := binding.Labels[labelMembership]
	tenant := binding.Labels[pmtenancyv1alpha1.LabelTenant]

	// RELEASE FIRST, then nudge. The order is the whole trick.
	//
	// Nudging while the finalizer still holds the object makes the Membership
	// reconcile against a binding that is still present: CreateOrUpdate finds it,
	// changes nothing, reports success — and the finalizer is dropped immediately
	// after, deleting the very object that was just "repaired". The grant is gone
	// and the Membership says RBACApplied=True.
	controllerutil.RemoveFinalizer(binding, bindingFinalizer)
	if err := cl.Update(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if membership == "" || tenant == "" {
		// Written before this controller existed, so there is nothing to trace it
		// back to. The Membership controller's periodic pass still catches it.
		return ctrl.Result{}, nil
	}

	if err := r.nudge(ctx, tenant, membership); err != nil {
		// The binding is already gone at this point, so a failure here loses the
		// immediate repair — the periodic resync is what still covers it. Returning
		// the error retries, but only until the object drops out of the queue.
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}
	return ctrl.Result{}, nil
}

// nudge marks the Membership so its own controller reconciles it.
func (r *BindingReconciler) nudge(ctx context.Context, tenantCluster, name string) error {
	tenantSide, err := clusters.ClientForCluster(ctx, r.tenancy, tenantCluster)
	if err != nil {
		return err
	}

	m := &pmtenancyv1alpha1.Membership{}
	if err := tenantSide.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name}, m); err != nil {
		// The Membership is gone, which is why the binding is going. Correct, and
		// nothing to rebuild.
		return ctrlruntimeclient.IgnoreNotFound(err)
	}
	if !m.DeletionTimestamp.IsZero() {
		return nil
	}

	patch := ctrlruntimeclient.MergeFrom(m.DeepCopy())
	if m.Annotations == nil {
		m.Annotations = map[string]string{}
	}
	// The value only has to differ from last time for the patch to be a change.
	m.Annotations[annotationRepairedAt] = time.Now().UTC().Format(time.RFC3339Nano)
	return tenantSide.Patch(ctx, m, patch)
}

// releaseBinding drops our finalizer so a binding we are revoking can go.
func releaseBinding(ctx context.Context, cl ctrlruntimeclient.Client, name string) error {
	binding := &rbacv1.ClusterRoleBinding{}
	if err := cl.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name}, binding); err != nil {
		return ctrlruntimeclient.IgnoreNotFound(err)
	}
	if !controllerutil.RemoveFinalizer(binding, bindingFinalizer) {
		return nil
	}
	if err := cl.Update(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
