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
	"sort"
	"strings"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/kcp"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Finalizer keeps a OCMModuleSetup until its kcp workspaces are gone. Workspaces
// live inside kcp and have no owner on the config plane, so nothing else would
// ever remove them.
const Finalizer = "deploy.platform-mesh.io/ocmmodule-workspaces"

func (r *reconciler) ensureFinalizer(ctx context.Context) (bool, error) {
	if controllerutil.ContainsFinalizer(r.setup, Finalizer) {
		return true, nil
	}
	controllerutil.AddFinalizer(r.setup, Finalizer)
	if err := r.opts.UpdateModuleSetup(ctx, r.setup); err != nil {
		return false, fmt.Errorf("adding finalizer: %w", err)
	}
	// The update re-triggers the watch, so the rest runs on the next pass
	// against an object whose resourceVersion the status patch agrees with.
	return false, nil
}

// finalize deletes the module's workspaces, deepest first so a parent is never
// removed while a child still exists.
func (r *reconciler) finalize(ctx context.Context) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(r.setup, Finalizer) {
		return reconcile.Result{}, nil
	}

	done, reason, err := r.deleteWorkspaces(ctx)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !done {
		// A deletion held up in kcp is otherwise invisible, since the
		// object shows only a deletionTimestamp and a finalizer.
		meta.SetStatusCondition(&r.setup.Status.Conditions,
			workspacesPending(r.setup.Generation, "WaitingForWorkspace", reason))
		return reconcile.Result{RequeueAfter: r.opts.Requeue}, r.commitStatus(ctx)
	}

	controllerutil.RemoveFinalizer(r.setup, Finalizer)
	if err := r.opts.UpdateModuleSetup(ctx, r.setup); err != nil {
		return reconcile.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	r.log.Info("released OCMModuleSetup, its kcp workspaces are gone")
	return reconcile.Result{}, nil
}

// deleteWorkspaces reports whether every workspace is gone, and why not.
func (r *reconciler) deleteWorkspaces(ctx context.Context) (bool, string, error) {
	key := ctrlruntimeclient.ObjectKey{Namespace: r.setup.Namespace, Name: r.setup.Spec.PlatformMeshRef.Name}
	pm, err := r.opts.GetPlatformMesh(ctx, key)
	if err != nil {
		// The whole installation is going away, so there is nothing left
		// to clean up inside it.
		if apierrors.IsNotFound(err) {
			return true, "", nil
		}
		return false, "", fmt.Errorf("getting PlatformMesh %q: %w", key.Name, err)
	}

	cfg, err := r.opts.KcpConfig(ctx, pm)
	if err != nil {
		// kcp is not reachable; without it the workspaces cannot be
		// removed, and they are gone anyway once kcp is.
		if errors.Is(err, kcp.ErrPending) {
			return true, "", nil
		}
		return false, "", fmt.Errorf("connecting to kcp: %w", err)
	}

	for _, path := range deepestFirst(r.setup.Spec.Workspaces) {
		if err := r.opts.DeletePath(ctx, cfg, path); err != nil {
			if errors.Is(err, kcp.ErrWorkspacePending) {
				return false, err.Error(), nil
			}
			return false, "", fmt.Errorf("deleting workspace %q: %w", path, err)
		}
	}
	return true, "", nil
}

// deepestFirst orders workspace paths so children are deleted before parents.
func deepestFirst(workspaces []pmdeployv1alpha1.OCMModuleSetupWorkspace) []string {
	paths := make([]string, 0, len(workspaces))
	for _, ws := range workspaces {
		paths = append(paths, ws.Path)
	}
	sort.Slice(paths, func(i, j int) bool {
		di, dj := strings.Count(paths[i], ":"), strings.Count(paths[j], ":")
		if di != dj {
			return di > dj
		}
		return paths[i] < paths[j]
	})
	return paths
}
