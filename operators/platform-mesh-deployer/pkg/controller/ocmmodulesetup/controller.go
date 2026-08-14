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

package ocmmodulesetup

import (
	"context"
	"errors"
	"fmt"
	"time"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/kcp"
	pmocm "go.platform-mesh.io/platform-mesh-deployer/pkg/ocm"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

const ControllerName = "OCMModuleSetupReconciler"

// defaultRequeue is how long to wait on kcp, which is a separate API server
// and therefore not watched.
const defaultRequeue = 10 * time.Second

// Options configures the controller. Every dependency reaching outside the
// process is a func so the reconciler can be driven without a cluster.
type Options struct {
	GetPlatformMesh   func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error)
	GetModule         func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModule, error)
	GetModuleSetup    func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModuleSetup, error)
	UpdateModuleSetup func(ctx context.Context, setup *pmdeployv1alpha1.OCMModuleSetup) error
	PatchStatus       func(ctx context.Context, old, current *pmdeployv1alpha1.OCMModuleSetup) error

	// KcpConfig mints the admin kubeconfig and reports kcp.ErrPending until
	// kcp-operator has written it.
	KcpConfig func(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error)
	// EnsurePath creates every workspace along a path and returns a client
	// for the leaf, reporting kcp.ErrWorkspacePending until it is schedulable.
	EnsurePath func(ctx context.Context, cfg *rest.Config, path string) (ctrlruntimeclient.Client, error)
	DeletePath func(ctx context.Context, cfg *rest.Config, path string) error

	ResolveModule    func(ctx context.Context, mod *pmdeployv1alpha1.OCMModule, fallback *pmdeployv1alpha1.OCMRepository) (*ocmmodule.Resolved, error)
	DownloadResource func(ctx context.Context, resolved *ocmmodule.Resolved, name string) ([]byte, error)
	ApplyObject      func(ctx context.Context, ws ctrlruntimeclient.Client, obj *unstructured.Unstructured) error

	Requeue time.Duration
}

func (o *Options) validate() error {
	switch {
	case o.GetPlatformMesh == nil:
		return errors.New("GetPlatformMesh is required")
	case o.GetModule == nil:
		return errors.New("GetModule is required")
	case o.GetModuleSetup == nil:
		return errors.New("GetModuleSetup is required")
	case o.UpdateModuleSetup == nil:
		return errors.New("UpdateModuleSetup is required")
	case o.PatchStatus == nil:
		return errors.New("PatchStatus is required")
	case o.KcpConfig == nil:
		return errors.New("KcpConfig is required")
	case o.EnsurePath == nil:
		return errors.New("EnsurePath is required")
	case o.DeletePath == nil:
		return errors.New("DeletePath is required")
	case o.ResolveModule == nil:
		return errors.New("ResolveModule is required")
	case o.DownloadResource == nil:
		return errors.New("DownloadResource is required")
	}
	if o.ApplyObject == nil {
		o.ApplyObject = sync.Apply
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
		return nil, fmt.Errorf("invalid modulesetup controller options: %w", err)
	}
	return &Controller{opts: opts}, nil
}

// NewControllerFor wires the controller to a manager's local client and the
// kcp access it shares with the PlatformMesh controller.
func NewControllerFor(mgr mcmanager.Manager, access *kcp.Access, resolver pmocm.Resolver) (*Controller, error) {
	c := mgr.GetLocalManager().GetClient()
	return NewController(Options{
		GetPlatformMesh: func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			pm := &pmdeployv1alpha1.PlatformMesh{}
			return pm, c.Get(ctx, key, pm)
		},
		GetModule: func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModule, error) {
			mod := &pmdeployv1alpha1.OCMModule{}
			return mod, c.Get(ctx, key, mod)
		},
		GetModuleSetup: func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModuleSetup, error) {
			setup := &pmdeployv1alpha1.OCMModuleSetup{}
			return setup, c.Get(ctx, key, setup)
		},
		UpdateModuleSetup: func(ctx context.Context, setup *pmdeployv1alpha1.OCMModuleSetup) error {
			return c.Update(ctx, setup)
		},
		PatchStatus: func(ctx context.Context, old, current *pmdeployv1alpha1.OCMModuleSetup) error {
			return c.Status().Patch(ctx, current, ctrlruntimeclient.MergeFrom(old))
		},
		KcpConfig:  access.Config,
		EnsurePath: access.EnsurePath,
		DeletePath: access.DeletePath,
		ResolveModule: func(ctx context.Context, mod *pmdeployv1alpha1.OCMModule, fallback *pmdeployv1alpha1.OCMRepository) (*ocmmodule.Resolved, error) {
			return ocmmodule.Resolve(ctx, resolver, mod, fallback)
		},
		DownloadResource: downloadResource,
	})
}

func (c *Controller) SetupWithManager(mgr mcmanager.Manager) error {
	local := mgr.GetLocalManager()
	return ctrl.NewControllerManagedBy(local).
		For(&pmdeployv1alpha1.OCMModuleSetup{}).
		// The kcp side can only start once the root structure exists.
		Watches(&pmdeployv1alpha1.PlatformMesh{}, handler.EnqueueRequestsFromMapFunc(enqueueSetupsOfPlatformMesh(local.GetClient()))).
		Named(ControllerName).
		WithOptions(controller.Options{SkipNameValidation: ptr.To(true)}).
		Complete(c)
}

func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r := &reconciler{
		opts: c.opts,
		req:  req,
		log:  ctrllog.FromContext(ctx),
	}
	return r.reconcile(ctx)
}

func enqueueSetupsOfPlatformMesh(c ctrlruntimeclient.Client) handler.MapFunc {
	return func(ctx context.Context, obj ctrlruntimeclient.Object) []reconcile.Request {
		list := &pmdeployv1alpha1.OCMModuleSetupList{}
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
