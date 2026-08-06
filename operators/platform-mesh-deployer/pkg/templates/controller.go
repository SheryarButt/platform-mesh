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

package templates

import (
	"context"
	"fmt"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// Reconciler holds a finalizer on a topology template for as long as a
// PlatformMesh references it.
type Reconciler struct {
	client ctrlruntimeclient.Client
	kind   string
	object func() ctrlruntimeclient.Object
}

// NewReconcilers returns one reconciler per topology template kind.
func NewReconcilers(mgr mcmanager.Manager) []*Reconciler {
	local := mgr.GetLocalManager().GetClient()
	out := make([]*Reconciler, 0, len(Kinds))
	for _, tk := range Kinds {
		out = append(out, &Reconciler{client: local, kind: tk.Kind, object: tk.Object})
	}
	return out
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr.GetLocalManager()).
		For(r.object()).
		// A PlatformMesh gaining or dropping a reference changes whether
		// its templates may be deleted.
		Watches(&pmdeployv1alpha1.PlatformMesh{}, handler.EnqueueRequestsFromMapFunc(enqueueTemplatesOfPlatformMesh(r.kind))).
		Named(r.kind + "Reconciler").
		WithOptions(controller.Options{SkipNameValidation: ptr.To(true)}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	tpl := r.object()
	if err := r.client.Get(ctx, req.NamespacedName, tpl); err != nil {
		return reconcile.Result{}, ctrlruntimeclient.IgnoreNotFound(err)
	}

	using, err := PlatformMeshesUsing(ctx, r.client, Key{
		Kind: r.kind, Namespace: req.Namespace, Name: req.Name,
	})
	if err != nil {
		return reconcile.Result{}, err
	}

	inUse := len(using) > 0
	if controllerutil.ContainsFinalizer(tpl, pmdeployv1alpha1.TemplateFinalizer) == inUse {
		return reconcile.Result{}, nil
	}
	if inUse {
		controllerutil.AddFinalizer(tpl, pmdeployv1alpha1.TemplateFinalizer)
	} else {
		controllerutil.RemoveFinalizer(tpl, pmdeployv1alpha1.TemplateFinalizer)
	}
	if err := r.client.Update(ctx, tpl); err != nil && !apierrors.IsConflict(err) {
		return reconcile.Result{}, fmt.Errorf("updating %s %s finalizer: %w", r.kind, req.NamespacedName, err)
	}
	return reconcile.Result{}, nil
}
