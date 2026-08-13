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

package platformmesh

import (
	"context"
	"errors"
	"fmt"
	"time"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/kcp"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/templates"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
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

const ControllerName = "PlatformMeshReconciler"

// defaultRequeue is how long to wait on kcp, which is a separate API server
// and therefore not watched.
const defaultRequeue = 10 * time.Second

// Options configures the controller. Every dependency reaching outside the
// process is a func so the reconciler can be driven without a cluster.
type Options struct {
	GetPlatformMesh func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error)
	PatchStatus     func(ctx context.Context, old, current *pmdeployv1alpha1.PlatformMesh) error
	ListModules     func(ctx context.Context, namespace string) ([]pmdeployv1alpha1.OCMModule, error)

	// GetTemplate reads one topology template into the given object.
	GetTemplate func(ctx context.Context, key ctrlruntimeclient.ObjectKey, into ctrlruntimeclient.Object) error
	// Apply creates or updates an admin CR owned by the PlatformMesh.
	Apply func(ctx context.Context, owner *pmdeployv1alpha1.PlatformMesh, obj ctrlruntimeclient.Object, mutate func()) error
	// Teardown deletes the component's admin CRs that are no longer desired.
	Teardown func(ctx context.Context, owner *pmdeployv1alpha1.PlatformMesh, component string, list ctrlruntimeclient.ObjectList, desired map[string]struct{}) error

	ClustersFor func(platformMesh, component string) []clusters.Cluster
	// RegistryEvents signals that the clusters engaged for a PlatformMesh
	// changed, which the topology and the exposure both render from.
	RegistryEvents func() <-chan event.GenericEvent

	// KcpConfig and EnsureKcpPath provision the root structure. Both nil
	// where this deployer does not run the provisioner controller, which
	// drops that step from the chain.
	KcpConfig     func(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error)
	EnsureKcpPath func(ctx context.Context, cfg *rest.Config, path string) error

	Requeue time.Duration
}

func (o *Options) validate() error {
	switch {
	case o.GetPlatformMesh == nil:
		return errors.New("GetPlatformMesh is required")
	case o.PatchStatus == nil:
		return errors.New("PatchStatus is required")
	case o.ListModules == nil:
		return errors.New("ListModules is required")
	case o.GetTemplate == nil:
		return errors.New("GetTemplate is required")
	case o.Apply == nil:
		return errors.New("an Apply func is required")
	case o.Teardown == nil:
		return errors.New("a Teardown func is required")
	case o.ClustersFor == nil:
		return errors.New("ClustersFor is required")
	case o.RegistryEvents == nil:
		return errors.New("RegistryEvents is required")
	case (o.KcpConfig == nil) != (o.EnsureKcpPath == nil):
		return errors.New("KcpConfig and EnsureKcpPath must be set together")
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
		return nil, fmt.Errorf("invalid platformmesh controller options: %w", err)
	}
	return &Controller{opts: opts}, nil
}

// NewControllerFor wires the controller to a manager's local client. A nil
// access leaves the root structure step out, for a deployer that does not run
// the provisioner controller.
func NewControllerFor(mgr mcmanager.Manager, registry *clusters.Registry, access *kcp.Access) (*Controller, error) {
	local := mgr.GetLocalManager()
	c := local.GetClient()

	opts := Options{
		GetPlatformMesh: func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			pm := &pmdeployv1alpha1.PlatformMesh{}
			return pm, c.Get(ctx, key, pm)
		},
		PatchStatus: func(ctx context.Context, old, current *pmdeployv1alpha1.PlatformMesh) error {
			return c.Status().Patch(ctx, current, ctrlruntimeclient.MergeFrom(old))
		},
		ListModules: func(ctx context.Context, namespace string) ([]pmdeployv1alpha1.OCMModule, error) {
			list := &pmdeployv1alpha1.OCMModuleList{}
			if err := c.List(ctx, list, ctrlruntimeclient.InNamespace(namespace)); err != nil {
				return nil, err
			}
			return list.Items, nil
		},
		GetTemplate: func(ctx context.Context, key ctrlruntimeclient.ObjectKey, into ctrlruntimeclient.Object) error {
			return c.Get(ctx, key, into)
		},
		Apply:          ApplyWith(c),
		Teardown:       TeardownWith(c),
		ClustersFor:    registry.ClustersFor,
		RegistryEvents: registry.Events,
	}
	if access != nil {
		opts.KcpConfig = access.Config
		opts.EnsureKcpPath = func(ctx context.Context, cfg *rest.Config, path string) error {
			_, err := access.EnsurePath(ctx, cfg, path)
			return err
		}
	}

	return NewController(opts)
}

// ApplyWith creates or updates an admin CR owned by the PlatformMesh.
func ApplyWith(c ctrlruntimeclient.Client) func(context.Context, *pmdeployv1alpha1.PlatformMesh, ctrlruntimeclient.Object, func()) error {
	return func(ctx context.Context, owner *pmdeployv1alpha1.PlatformMesh, obj ctrlruntimeclient.Object, mutate func()) error {
		_, err := controllerutil.CreateOrUpdate(ctx, c, obj, func() error {
			mutate()
			return controllerutil.SetControllerReference(owner, obj, c.Scheme())
		})
		return err
	}
}

// TeardownWith deletes the component's admin CRs that are no longer desired.
// The label selector is what decides that, so tests drive this rather than
// their own copy of it.
func TeardownWith(c ctrlruntimeclient.Client) func(context.Context, *pmdeployv1alpha1.PlatformMesh, string, ctrlruntimeclient.ObjectList, map[string]struct{}) error {
	return func(ctx context.Context, owner *pmdeployv1alpha1.PlatformMesh, component string, list ctrlruntimeclient.ObjectList, desired map[string]struct{}) error {
		if err := c.List(ctx, list,
			ctrlruntimeclient.InNamespace(owner.Namespace),
			ctrlruntimeclient.MatchingLabels{components.LabelPlatformMesh: owner.Name, components.LabelComponent: component},
		); err != nil {
			return err
		}
		items, err := meta.ExtractList(list)
		if err != nil {
			return err
		}
		for _, item := range items {
			obj := item.(ctrlruntimeclient.Object)
			if _, ok := desired[obj.GetName()]; ok {
				continue
			}
			if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		return nil
	}
}

func (c *Controller) SetupWithManager(mgr mcmanager.Manager) error {
	local := mgr.GetLocalManager()
	b := ctrl.NewControllerManagedBy(local).
		For(&pmdeployv1alpha1.PlatformMesh{}).
		WatchesRawSource(source.Channel(
			c.opts.RegistryEvents(),
			handler.EnqueueRequestsFromMapFunc(enqueuePlatformMeshByName(local.GetClient())),
		)).
		// A module publishes its resolved front proxy mapping in its
		// status, which the topology merges into the FrontProxy, and the
		// pre-topology gate waits on modules becoming ready.
		Watches(&pmdeployv1alpha1.OCMModule{}, handler.EnqueueRequestsFromMapFunc(enqueuePlatformMeshOfModule()))

	for _, tk := range templates.Kinds {
		b = b.Watches(tk.Object(), handler.EnqueueRequestsFromMapFunc(
			enqueuePlatformMeshesUsingTemplate(local.GetClient(), tk.Kind)))
	}

	return b.
		Named(ControllerName).
		WithOptions(controller.Options{SkipNameValidation: ptr.To(true)}).
		Complete(c)
}

func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r := &reconciler{opts: c.opts, req: req}
	return r.reconcile(ctx)
}

// enqueuePlatformMeshesUsingTemplate maps a template to every PlatformMesh
// referencing it, which several may.
func enqueuePlatformMeshesUsingTemplate(c ctrlruntimeclient.Client, kind string) handler.MapFunc {
	return func(ctx context.Context, obj ctrlruntimeclient.Object) []reconcile.Request {
		key := templates.Key{Kind: kind, Namespace: obj.GetNamespace(), Name: obj.GetName()}
		using, err := templates.PlatformMeshesUsing(ctx, c, key)
		if err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(using))
		for i := range using {
			reqs = append(reqs, reconcile.Request{NamespacedName: ctrlruntimeclient.ObjectKeyFromObject(&using[i])})
		}
		return reqs
	}
}

// enqueuePlatformMeshOfModule maps a OCMModule to the PlatformMesh it belongs to.
func enqueuePlatformMeshOfModule() handler.MapFunc {
	return func(_ context.Context, obj ctrlruntimeclient.Object) []reconcile.Request {
		mod, ok := obj.(*pmdeployv1alpha1.OCMModule)
		if !ok {
			return nil
		}
		return []reconcile.Request{{NamespacedName: ctrlruntimeclient.ObjectKey{
			Namespace: mod.Namespace,
			Name:      mod.Spec.PlatformMeshRef.Name,
		}}}
	}
}

// enqueuePlatformMeshByName maps a signal from the deployer's clusters.Registry
// to a new request for the PlatformMesh with a matching name.
func enqueuePlatformMeshByName(c ctrlruntimeclient.Client) handler.MapFunc {
	return func(ctx context.Context, obj ctrlruntimeclient.Object) []reconcile.Request {
		name := obj.GetName()
		list := &pmdeployv1alpha1.PlatformMeshList{}
		if err := c.List(ctx, list); err != nil {
			return nil
		}
		var reqs []reconcile.Request
		for i := range list.Items {
			if list.Items[i].Name != name {
				continue
			}
			reqs = append(reqs, reconcile.Request{NamespacedName: ctrlruntimeclient.ObjectKeyFromObject(&list.Items[i])})
		}
		return reqs
	}
}
