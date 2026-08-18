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

	"go.platform-mesh.io/platform-mesh-deployer/pkg/kcp"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	"k8s.io/apimachinery/pkg/api/meta"
)

// roots are the workspaces every PlatformMesh gets. OCMModules live below
// root:modules.
//
// Organisations are NOT here. root:orgs used to be provisioned alongside, but
// nothing ever reconciled it, so it was an empty workspace whose name claimed
// something it did not own. A tenancy model that wants a tree of its own asks
// for the workspaces it needs rather than inheriting one nobody fills.
var roots = []string{ocmmodule.WorkspaceBase}

// reconcileRootStructure creates the kcp workspaces a PlatformMesh needs before
// anything can be installed into it. It runs after the topology because kcp
// only exists once that serves. kcp is a separate API server and is not
// watched, so waiting on it polls.
func (r *reconciler) reconcileRootStructure(ctx context.Context) (bool, error) {
	pm := r.pm

	cfg, err := r.opts.KcpConfig(ctx, pm)
	if err != nil {
		if errors.Is(err, kcp.ErrPending) {
			meta.SetStatusCondition(&pm.Status.Conditions,
				rootStructurePending(pm.Generation, "WaitingForKubeconfig", err.Error()))
			r.requeueAfter = r.opts.Requeue
			return false, nil
		}
		return false, fmt.Errorf("connecting to kcp: %w", err)
	}

	for _, path := range roots {
		if err := r.opts.EnsureKcpPath(ctx, cfg, path); err != nil {
			if errors.Is(err, kcp.ErrWorkspacePending) {
				meta.SetStatusCondition(&pm.Status.Conditions,
					rootStructurePending(pm.Generation, "WaitingForWorkspace", err.Error()))
				r.requeueAfter = r.opts.Requeue
				return false, nil
			}
			return false, fmt.Errorf("ensuring workspace %q: %w", path, err)
		}
	}

	meta.SetStatusCondition(&pm.Status.Conditions, rootStructureProvisioned(pm.Generation))
	return true, nil
}
