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

package ocmmodule

import (
	"context"
	"errors"
	"fmt"
	"time"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocm"
	pmocmmodule "go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

const ControllerName = "OCMModuleReconciler"

// defaultRequeue is how long to wait for the config-plane Secrets kcp-operator
// and cert-manager mint asynchronously, which are not watched.
const defaultRequeue = 15 * time.Second

// Options configures the controller. Every dependency reaching outside the
// process is a func so the reconciler can be driven without a cluster.
type Options struct {
	GetModule       func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModule, error)
	ListModules     func(ctx context.Context, namespace string) ([]pmdeployv1alpha1.OCMModule, error)
	GetPlatformMesh func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error)
	GetSecret       func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*corev1.Secret, error)
	UpdateModule    func(ctx context.Context, mod *pmdeployv1alpha1.OCMModule) error
	PatchStatus     func(ctx context.Context, old, current *pmdeployv1alpha1.OCMModule) error

	// Apply creates or updates a config-plane object owned by the OCMModule.
	Apply func(ctx context.Context, owner *pmdeployv1alpha1.OCMModule, obj ctrlruntimeclient.Object, mutate func() error) error

	ClustersFor    func(platformMesh, component string) []clusters.Cluster
	AllClustersFor func(platformMesh string) []clusters.Cluster
	RegistryEvents func() <-chan event.GenericEvent

	ResolveModule func(ctx context.Context, mod *pmdeployv1alpha1.OCMModule, fallback *pmdeployv1alpha1.OCMRepository) (*pmocmmodule.Resolved, error)
	FanOut        func(mod *pmdeployv1alpha1.OCMModule) ([]pmocmmodule.Instance, error)

	Requeue time.Duration
}

func (o *Options) validate() error {
	switch {
	case o.GetModule == nil:
		return errors.New("GetModule is required")
	case o.ListModules == nil:
		return errors.New("ListModules is required")
	case o.GetPlatformMesh == nil:
		return errors.New("GetPlatformMesh is required")
	case o.GetSecret == nil:
		return errors.New("GetSecret is required")
	case o.UpdateModule == nil:
		return errors.New("UpdateModule is required")
	case o.PatchStatus == nil:
		return errors.New("PatchStatus is required")
	case o.Apply == nil:
		return errors.New("an Apply func is required")
	case o.ClustersFor == nil:
		return errors.New("ClustersFor is required")
	case o.AllClustersFor == nil:
		return errors.New("AllClustersFor is required")
	case o.RegistryEvents == nil:
		return errors.New("RegistryEvents is required")
	case o.ResolveModule == nil:
		return errors.New("ResolveModule is required")
	case o.FanOut == nil:
		return errors.New("FanOut is required")
	}
	if o.Requeue == 0 {
		o.Requeue = defaultRequeue
	}
	return nil
}

type Controller struct {
	opts Options
}

func NewController(opts Options) (*Controller, error) {
	if err := opts.validate(); err != nil {
		return nil, fmt.Errorf("invalid module controller options: %w", err)
	}
	return &Controller{opts: opts}, nil
}

// ApplyWith creates or updates a config-plane object owned by the OCMModule.
func ApplyWith(c ctrlruntimeclient.Client) func(context.Context, *pmdeployv1alpha1.OCMModule, ctrlruntimeclient.Object, func() error) error {
	return func(ctx context.Context, owner *pmdeployv1alpha1.OCMModule, obj ctrlruntimeclient.Object, mutate func() error) error {
		_, err := controllerutil.CreateOrUpdate(ctx, c, obj, func() error {
			if err := mutate(); err != nil {
				return err
			}
			return controllerutil.SetControllerReference(owner, obj, c.Scheme())
		})
		return err
	}
}

// NewControllerFor wires the controller to a manager's local client.
func NewControllerFor(mgr mcmanager.Manager, registry *clusters.Registry, resolver ocm.Resolver) (*Controller, error) {
	c := mgr.GetLocalManager().GetClient()
	return NewController(Options{
		GetModule: func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModule, error) {
			mod := &pmdeployv1alpha1.OCMModule{}
			return mod, c.Get(ctx, key, mod)
		},
		ListModules: func(ctx context.Context, namespace string) ([]pmdeployv1alpha1.OCMModule, error) {
			list := &pmdeployv1alpha1.OCMModuleList{}
			if err := c.List(ctx, list, ctrlruntimeclient.InNamespace(namespace)); err != nil {
				return nil, err
			}
			return list.Items, nil
		},
		GetPlatformMesh: func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			pm := &pmdeployv1alpha1.PlatformMesh{}
			return pm, c.Get(ctx, key, pm)
		},
		GetSecret: func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*corev1.Secret, error) {
			secret := &corev1.Secret{}
			return secret, c.Get(ctx, key, secret)
		},
		UpdateModule: func(ctx context.Context, mod *pmdeployv1alpha1.OCMModule) error {
			return c.Update(ctx, mod)
		},
		PatchStatus: func(ctx context.Context, old, current *pmdeployv1alpha1.OCMModule) error {
			return c.Status().Patch(ctx, current, ctrlruntimeclient.MergeFrom(old))
		},
		Apply:          ApplyWith(c),
		ClustersFor:    registry.ClustersFor,
		AllClustersFor: registry.AllClustersFor,
		RegistryEvents: registry.Events,
		ResolveModule: func(ctx context.Context, mod *pmdeployv1alpha1.OCMModule, fallback *pmdeployv1alpha1.OCMRepository) (*pmocmmodule.Resolved, error) {
			return pmocmmodule.Resolve(ctx, resolver, mod, fallback)
		},
		FanOut: func(mod *pmdeployv1alpha1.OCMModule) ([]pmocmmodule.Instance, error) {
			return pmocmmodule.FanOut(registry, mod)
		},
	})
}

func (c *Controller) SetupWithManager(mgr mcmanager.Manager) error {
	local := mgr.GetLocalManager()
	return ctrl.NewControllerManagedBy(local).
		For(&pmdeployv1alpha1.OCMModule{}).
		// The kcp side is done by the provisioner, which reports back on
		// the OCMModuleSetup this controller owns.
		Owns(&pmdeployv1alpha1.OCMModuleSetup{}).
		// A module gates on its PlatformMesh's topology, and on the modules
		// it depends on becoming ready.
		Watches(&pmdeployv1alpha1.PlatformMesh{}, handler.EnqueueRequestsFromMapFunc(enqueueModulesOfPlatformMesh(local.GetClient()))).
		Watches(&pmdeployv1alpha1.OCMModule{}, handler.EnqueueRequestsFromMapFunc(enqueueDependentModules(local.GetClient()))).
		WatchesRawSource(source.Channel(
			c.opts.RegistryEvents(),
			handler.EnqueueRequestsFromMapFunc(enqueueModulesOfPlatformMesh(local.GetClient())),
		)).
		Named(ControllerName).
		WithOptions(controller.Options{SkipNameValidation: ptr.To(true)}).
		Complete(c)
}

func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r := &reconciler{opts: c.opts, req: req}
	return r.reconcile(ctx)
}

// enqueueModulesOfPlatformMesh maps a PlatformMesh to its OCMModules, which gate
// on its topology.
func enqueueModulesOfPlatformMesh(c ctrlruntimeclient.Client) handler.MapFunc {
	return func(ctx context.Context, obj ctrlruntimeclient.Object) []reconcile.Request {
		list := &pmdeployv1alpha1.OCMModuleList{}
		if err := c.List(ctx, list); err != nil {
			return nil
		}
		var reqs []reconcile.Request
		for i := range list.Items {
			if list.Items[i].Spec.PlatformMeshRef.Name != obj.GetName() {
				continue
			}
			reqs = append(reqs, reconcile.Request{NamespacedName: ctrlruntimeclient.ObjectKeyFromObject(&list.Items[i])})
		}
		return reqs
	}
}

// enqueueDependentModules maps a OCMModule to the modules that declared it in
// spec.dependsOn. For sets the changed object alone, so without this a
// dependency becoming ready would never wake the modules waiting on it.
func enqueueDependentModules(c ctrlruntimeclient.Client) handler.MapFunc {
	return func(ctx context.Context, obj ctrlruntimeclient.Object) []reconcile.Request {
		list := &pmdeployv1alpha1.OCMModuleList{}
		if err := c.List(ctx, list, ctrlruntimeclient.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var reqs []reconcile.Request
		for i := range list.Items {
			for _, dep := range list.Items[i].Spec.DependsOn {
				if dep.Name != obj.GetName() {
					continue
				}
				reqs = append(reqs, reconcile.Request{NamespacedName: ctrlruntimeclient.ObjectKeyFromObject(&list.Items[i])})
				break
			}
		}
		return reqs
	}
}
