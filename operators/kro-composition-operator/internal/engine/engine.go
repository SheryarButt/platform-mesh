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

// Package engine drives KROaaS composition per consumer workspace: compile an RGD,
// publish its composite type as a bound API in the workspace, and materialize child
// resources for each instance — all as the operator's own kcp identity.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	krov1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"github.com/kubernetes-sigs/kro/pkg/dynamiccontroller"
	"github.com/kubernetes-sigs/kro/pkg/graph"
	"github.com/kubernetes-sigs/kro/pkg/requeue"

	"go.platform-mesh.io/kro-composition-operator/internal/composition"
	"go.platform-mesh.io/kro-composition-operator/internal/workspace"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

var ccGVK = schema.GroupVersionKind{Group: "ui.platform-mesh.io", Version: "v1alpha1", Kind: "ContentConfiguration"}

var (
	errNotEstablished  = fmt.Errorf("composite CRD not established yet")
	errDepsPending     = fmt.Errorf("composition dependencies pending")
	errTeardownPending = fmt.Errorf("composite teardown pending")
)

// rgdFinalizer holds the RGD in Terminating while the operator tears the published
// objects down in order — so the platform's APIBinding finalizer (which strips the
// type's OpenFGA authz) can run while the APIExport + schema still exist, before
// owner-ref GC would otherwise remove them all at once.
const rgdFinalizer = "kro-composition.platform-mesh.io/teardown"

// instanceRequeueInterval is the fixed delay before retrying an instance whose
// composition dependencies are not resolvable yet. Matches the default of kro's own
// instance reconciler, which requeues the same condition on a fixed interval.
const instanceRequeueInterval = 3 * time.Second

// transientError marks an expected, self-resolving condition (CRD still
// establishing, kcp openapi aggregation lag, dynamic controller still starting,
// composed provider API not yet available). Callers requeue quietly instead of
// logging these as failures.
type transientError struct{ err error }

func (e transientError) Error() string { return e.err.Error() }
func (e transientError) Unwrap() error { return e.err }

func transient(err error) error { return transientError{err: err} }

// IsTransient reports whether err is an expected, self-resolving requeue signal.
func IsTransient(err error) bool {
	var t transientError
	return errors.As(err, &t)
}

// Engine holds per-workspace state: compiled graphs and per-workspace dynamic
// controllers (one kro DynamicController per workspace, watching that workspace's
// composite instance types with a per-workspace client).
type Engine struct {
	Workspaces *workspace.Provider
	DCConfig   dynamiccontroller.Config

	// rootCtx bounds the lifetime of per-workspace dynamic-controller goroutines.
	rootCtx context.Context

	graphs  sync.Map // "cluster|group/version/resource" -> *graph.Graph
	rgdGVRs sync.Map // "cluster|rgdName" -> schema.GroupVersionResource (for delete cleanup)

	mu  sync.Mutex
	dcs map[string]*wsController // clusterName -> per-workspace controller
}

type wsController struct {
	dc  *dynamiccontroller.DynamicController
	mat *composition.Materializer
}

// New returns an Engine. rootCtx must outlive all workspaces (the manager's ctx).
func New(rootCtx context.Context, ws *workspace.Provider, dcConfig dynamiccontroller.Config) *Engine {
	return &Engine{Workspaces: ws, DCConfig: dcConfig, rootCtx: rootCtx, dcs: map[string]*wsController{}}
}

// workspaceTerminating reports whether the consumer workspace itself is being deleted
// (its LogicalCluster carries a deletionTimestamp) — meaning an RGD's teardown is part
// of the whole-workspace GC, so teardownComposite runs in force mode. Best-effort: if
// the LogicalCluster can't be read, assume the workspace is healthy (ordered path).
func workspaceTerminating(ctx context.Context, c ctrlruntimeclient.Client) bool {
	lc := &kcpcorev1alpha1.LogicalCluster{}
	if err := c.Get(ctx, types.NamespacedName{Name: "cluster"}, lc); err != nil {
		return false
	}
	return !lc.DeletionTimestamp.IsZero()
}

// ReconcileRGD handles one RGD in one workspace: compile it, publish the composite
// type as a bound API, and register an instance watch so instances materialize their
// children.
func (e *Engine) ReconcileRGD(ctx context.Context, clusterName, rgdName string) error {
	log := logf.FromContext(ctx).WithValues("cluster", clusterName, "rgd", rgdName)

	wc, err := e.Workspaces.For(ctx, clusterName)
	if err != nil {
		return fmt.Errorf("workspace clients: %w", err)
	}

	rgd := &krov1alpha1.ResourceGraphDefinition{}
	if err := wc.Client.Get(ctx, types.NamespacedName{Name: rgdName}, rgd); err != nil {
		if apierrors.IsNotFound(err) {
			return e.handleRGDDeleted(clusterName, rgdName)
		}
		return err
	}

	// Deletion path: tear the published objects down in order (APIBinding first, then
	// APIExport + schemas) so the platform's binding finalizer can strip authz first,
	// then release our finalizer to let the RGD (and its ContentConfiguration) be GC'd.
	//
	// If the whole workspace is terminating (account/workspace deletion), the RGD is
	// being GC'd along with everything else: the ordered authz-strip is moot and our
	// per-RGD APIBinding must not block that deletion, so tear down in force mode.
	if !rgd.DeletionTimestamp.IsZero() {
		done, err := teardownComposite(ctx, wc.Client, rgd, workspaceTerminating(ctx, wc.Client))
		if err != nil {
			return err
		}
		if !done {
			return transient(errTeardownPending)
		}
		if err := e.handleRGDDeleted(clusterName, rgdName); err != nil {
			return err
		}
		if controllerutil.RemoveFinalizer(rgd, rgdFinalizer) {
			if err := wc.Client.Update(ctx, rgd); err != nil {
				return err
			}
		}
		return nil
	}

	// Hold the RGD via a finalizer so we control teardown ordering on delete.
	if controllerutil.AddFinalizer(rgd, rgdFinalizer) {
		if err := wc.Client.Update(ctx, rgd); err != nil {
			return transient(fmt.Errorf("add finalizer: %w", err))
		}
	}

	compiler, err := composition.NewCompiler(wc.Config)
	if err != nil {
		return fmt.Errorf("compiler: %w", err)
	}
	g, err := compiler.Compile(rgd)
	if err != nil {
		// Usually the kcp openapi aggregation lag right after CRD creation, or a
		// composed provider API not yet available in the workspace — both resolve.
		return transient(fmt.Errorf("RGD not compilable yet: %w", err))
	}
	if g.CRD == nil {
		return fmt.Errorf("kro produced no CRD for RGD %q", rgdName)
	}

	// Publish the composite type as a *bound* API (APIResourceSchema + per-RGD
	// APIExport + self-binding) rather than a plain reflected CRD, so the graphql
	// gateway and security-operator (both boundResource-driven) treat it as first-class.
	bound, err := e.publishComposite(ctx, wc.Client, g.CRD, rgd)
	if err != nil {
		return err
	}
	if !bound {
		return transient(errNotEstablished)
	}

	gvr := instanceGVR(g.CRD)
	e.graphs.Store(graphKey(clusterName, gvr), g)
	e.rgdGVRs.Store(rgdName+"|"+clusterName, gvr)

	// Emit a portal ContentConfiguration for the generated type (best-effort: skips
	// where the ui.platform-mesh.io API isn't served, e.g. no portal).
	if err := e.ensureContentConfiguration(ctx, wc, rgd, g.CRD); err != nil {
		return err
	}

	wsc := e.ensureController(clusterName, wc)
	if err := wsc.dc.Register(ctx, gvr, e.instanceHandler(clusterName, gvr, wsc.mat)); err != nil {
		return transient(fmt.Errorf("register instance watch %s: %w", gvr, err))
	}

	// Report the RGD as Active (state + topological order + readiness conditions) so
	// the portal and kubectl show it serving — the CRD and controller are up now.
	if err := writeRGDStatus(ctx, wc.Client, rgd.Name, g.TopologicalOrder); err != nil {
		return transient(fmt.Errorf("write RGD status: %w", err))
	}

	log.Info("composite type ready and watched", "gvr", gvr.String())
	return nil
}

// ensureContentConfiguration writes a portal ContentConfiguration for the generated
// type into the workspace (owned by the RGD, so it is GC'd with it). It is
// best-effort: if the ui.platform-mesh.io ContentConfiguration API is not served in
// the workspace (e.g. a portal-less environment), it logs and skips.
func (e *Engine) ensureContentConfiguration(ctx context.Context, wc *workspace.Clients, rgd *krov1alpha1.ResourceGraphDefinition, crd *apiextensionsv1.CustomResourceDefinition) error {
	version := storageVersion(crd)
	content := composition.BuildContentConfig(
		crd.Spec.Group, version, crd.Spec.Names.Kind, crd.Spec.Names.Plural,
		crd.Spec.Scope == apiextensionsv1.NamespaceScoped, specFieldsFromCRD(crd, version),
	)

	cc := &unstructured.Unstructured{}
	cc.SetGroupVersionKind(ccGVK)
	cc.SetName(contentConfigName(rgd.Name))

	yes := true
	_, err := controllerutil.CreateOrUpdate(ctx, wc.Client, cc, func() error {
		cc.SetLabels(map[string]string{"ui.platform-mesh.io/entity": composition.AccountEntity})
		cc.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion:         krov1alpha1.GroupVersion.String(),
			Kind:               "ResourceGraphDefinition",
			Name:               rgd.Name,
			UID:                rgd.UID,
			BlockOwnerDeletion: &yes,
		}})
		return unstructured.SetNestedMap(cc.Object,
			map[string]any{"contentType": "json", "content": content},
			"spec", "inlineConfiguration")
	})
	if err != nil {
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			logf.FromContext(ctx).V(1).Info("ContentConfiguration API not served; skipping portal config", "gvr", ccGVK.String())
			return nil
		}
		return fmt.Errorf("ensure ContentConfiguration: %w", err)
	}
	return nil
}

// handleRGDDeleted stops watching the composite type and drops caches. The
// composite CRD (and its instances and their children) are removed by owner-ref
// garbage collection (CRD owned by the RGD; children owned by their instance).
func (e *Engine) handleRGDDeleted(clusterName, rgdName string) error {
	v, ok := e.rgdGVRs.LoadAndDelete(rgdName + "|" + clusterName)
	if !ok {
		return nil // never fully reconciled; nothing to clean up
	}
	gvr := v.(schema.GroupVersionResource)
	e.graphs.Delete(graphKey(clusterName, gvr))

	e.mu.Lock()
	wsc, running := e.dcs[clusterName]
	e.mu.Unlock()
	if running {
		if err := wsc.dc.Deregister(context.Background(), gvr); err != nil {
			return fmt.Errorf("deregister %s: %w", gvr, err)
		}
	}
	logf.Log.WithName("engine").Info("RGD deleted; stopped watching composite type", "cluster", clusterName, "rgd", rgdName, "gvr", gvr.String())
	return nil
}

// ensureController lazily creates and starts a kro DynamicController for a workspace.
func (e *Engine) ensureController(clusterName string, wc *workspace.Clients) *wsController {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.dcs[clusterName]; ok {
		return c
	}
	dc := dynamiccontroller.NewDynamicController(
		logf.Log.WithName("dynamic-controller").WithValues("cluster", clusterName),
		e.DCConfig,
		wc.Metadata,
		wc.Mapper,
	)
	c := &wsController{dc: dc, mat: &composition.Materializer{Dyn: wc.Dynamic, Mapper: wc.Mapper}}
	e.dcs[clusterName] = c
	go func() {
		if err := dc.Start(e.rootCtx); err != nil && e.rootCtx.Err() == nil {
			logf.Log.WithName("engine").Error(err, "workspace dynamic controller stopped", "cluster", clusterName)
		}
	}()
	return c
}

func (e *Engine) instanceHandler(clusterName string, gvr schema.GroupVersionResource, mat *composition.Materializer) dynamiccontroller.Handler {
	return func(ctx context.Context, req ctrl.Request) error {
		log := logf.FromContext(ctx).WithValues("cluster", clusterName, "instance", req.String())

		wc, err := e.Workspaces.For(ctx, clusterName)
		if err != nil {
			return err
		}
		gAny, ok := e.graphs.Load(graphKey(clusterName, gvr))
		if !ok {
			return fmt.Errorf("no graph for %s in %s", gvr, clusterName)
		}
		g := gAny.(*graph.Graph)

		inst, err := wc.Dynamic.Resource(gvr).Namespace(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			// Instance deleted; its children are garbage-collected by their owner
			// references to it (set in Materialize), so there is nothing to do here.
			return nil
		}
		if err != nil {
			return fmt.Errorf("get instance: %w", err)
		}

		ready, err := mat.Materialize(ctx, g, gvr, inst)
		if err != nil {
			return err
		}
		if !ready {
			// A pending dependency is not a failure, so it must not travel the queue's
			// rate-limited path: that counts against QueueMaxRetries and drops the item
			// once exhausted, after which nothing reconciles the instance until it is
			// updated or the informer resyncs. requeue.NeededAfter re-adds the item after
			// a fixed delay without incrementing the retry count, so an instance waiting
			// on a slow child keeps converging however long that child takes.
			return requeue.NeededAfter(errDepsPending, instanceRequeueInterval)
		}
		log.Info("instance materialized")
		return nil
	}
}
