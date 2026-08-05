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
	"fmt"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	pmocmmodule "go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Finalizer keeps a OCMModule around until its objects are gone from every
// cluster it was deployed to. Owner references only reach objects on the
// config plane, so without this the workloads leak.
const Finalizer = "deploy.platform-mesh.io/ocmmodule-workloads"

func (r *reconciler) ensureFinalizer(ctx context.Context) (bool, error) {
	if controllerutil.ContainsFinalizer(r.mod, Finalizer) {
		return true, nil
	}
	controllerutil.AddFinalizer(r.mod, Finalizer)
	if err := r.opts.UpdateModule(ctx, r.mod); err != nil {
		return false, fmt.Errorf("adding finalizer: %w", err)
	}
	// The update re-triggers the watch, so the rest runs on the next pass
	// against an object whose resourceVersion the status patch agrees with.
	return false, nil
}

// finalize removes every object the module applied, on every cluster the
// PlatformMesh has engaged. It deliberately looks wider than the module's last
// known placement: a cluster that dropped out of the fan-out earlier may still
// be holding objects.
func (r *reconciler) finalize(ctx context.Context) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(r.mod, Finalizer) {
		return reconcile.Result{}, nil
	}

	mod := r.mod
	kinds := appliedKinds(mod)
	for _, c := range r.opts.AllClustersFor(mod.Spec.PlatformMeshRef.Name) {
		if err := sync.Prune(ctx, c.Cluster.GetClient(), kinds,
			pmocmmodule.OCMModuleSelector(mod, c.ClusterID), nil); err != nil {
			return reconcile.Result{}, fmt.Errorf("pruning on cluster %q: %w", c.ClusterID, err)
		}
	}

	controllerutil.RemoveFinalizer(mod, Finalizer)
	if err := r.opts.UpdateModule(ctx, mod); err != nil {
		return reconcile.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return reconcile.Result{}, nil
}

// appliedKinds are the kinds a module's objects can have. The payload is only
// known once resolved, which a deleted module may no longer be able to do, so
// the kinds recorded on the status are used and the always-applied ConfigMap
// and Secret are added.
func appliedKinds(mod *pmdeployv1alpha1.OCMModule) []schema.GroupVersionKind {
	seen := map[schema.GroupVersionKind]struct{}{
		{Version: "v1", Kind: "ConfigMap"}: {},
		{Version: "v1", Kind: "Secret"}:    {},
	}
	for _, gvk := range mod.Status.AppliedKinds {
		seen[schema.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}] = struct{}{}
	}
	return kindsOf(seen)
}
