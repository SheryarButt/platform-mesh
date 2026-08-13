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
	"strings"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/kcp"

	"k8s.io/apimachinery/pkg/api/meta"
)

// rootStructureCondition is written by the PlatformMesh controller once the
// workspaces this module's own workspaces hang below exist.
const rootStructureCondition = "RootStructureProvisioned"

// awaitRootStructure gates on the PlatformMesh, which is watched, so it stops
// without a requeue.
func (r *reconciler) awaitRootStructure() (bool, error) {
	if meta.IsStatusConditionTrue(r.pm.Status.Conditions, rootStructureCondition) {
		return true, nil
	}
	meta.SetStatusCondition(&r.setup.Status.Conditions, workspacesPending(r.setup.Generation,
		"WaitingForRootStructure", "kcp root structure is not provisioned yet"))
	return false, nil
}

// connectKcp mints and reads the admin kubeconfig. kcp-operator writes the
// secret asynchronously and nothing watches it, so a pending mint requeues.
func (r *reconciler) connectKcp(ctx context.Context) (bool, error) {
	cfg, err := r.opts.KcpConfig(ctx, r.pm)
	if err != nil {
		if errors.Is(err, kcp.ErrPending) {
			meta.SetStatusCondition(&r.setup.Status.Conditions,
				workspacesPending(r.setup.Generation, "WaitingForKubeconfig", err.Error()))
			r.requeueAfter = r.opts.Requeue
			return false, nil
		}
		return false, fmt.Errorf("connecting to kcp: %w", err)
	}
	r.cfg = cfg
	return true, nil
}

// provisionWorkspaces creates each declared workspace and applies the content
// the module ships for it.
func (r *reconciler) provisionWorkspaces(ctx context.Context) (bool, error) {
	for _, ws := range r.setup.Spec.Workspaces {
		client, err := r.opts.EnsurePath(ctx, r.cfg, ws.Path)
		if err != nil {
			// A workspace lives in kcp, which is not watched.
			if errors.Is(err, kcp.ErrWorkspacePending) {
				meta.SetStatusCondition(&r.setup.Status.Conditions,
					workspacesPending(r.setup.Generation, "WaitingForWorkspace", err.Error()))
				r.requeueAfter = r.opts.Requeue
				return false, nil
			}
			return false, fmt.Errorf("ensuring workspace %q: %w", ws.Path, err)
		}
		if err := r.applyContent(ctx, client, ws); err != nil {
			meta.SetStatusCondition(&r.setup.Status.Conditions,
				workspacesPending(r.setup.Generation, "ContentFailed", err.Error()))
			return false, err
		}
	}
	meta.SetStatusCondition(&r.setup.Status.Conditions, workspacesProvisioned(r.setup.Generation))
	return true, nil
}

func (r *reconciler) publishEndpoints() {
	r.setup.Status.Endpoints = workspaceEndpoints(r.cfg.Host, r.setup.Spec.Workspaces)
}

// workspaceEndpoints publishes the URL of each provisioned workspace, so a
// module's payload can address its own kcp workspaces without knowing how the
// front proxy is exposed. The module workspace is published as "workspace";
// children keep their own name. Kept free of the client so the naming is
// testable without kcp.
func workspaceEndpoints(host string, workspaces []pmdeployv1alpha1.OCMModuleSetupWorkspace) map[string]string {
	if len(workspaces) == 0 {
		return nil
	}
	out := make(map[string]string, len(workspaces))
	shortest := ""
	for _, ws := range workspaces {
		if shortest == "" || len(ws.Path) < len(shortest) {
			shortest = ws.Path
		}
	}
	for _, ws := range workspaces {
		name := "workspace"
		if ws.Path != shortest {
			name = ws.Path[strings.LastIndex(ws.Path, ":")+1:]
		}
		out[name] = host + "/clusters/" + ws.Path
	}
	return out
}
