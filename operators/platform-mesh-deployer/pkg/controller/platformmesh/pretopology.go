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
	"fmt"
	"sort"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	"k8s.io/apimachinery/pkg/api/meta"
)

// awaitPreTopology blocks the kcp topology until the modules that have to
// exist before it are ready, such as the etcd the shards will store into.
// OCMModules are watched, so this stops without polling.
func (r *reconciler) awaitPreTopology(ctx context.Context) (bool, error) {
	pm := r.pm

	modules, err := r.opts.ListModules(ctx, pm.Namespace)
	if err != nil {
		return false, fmt.Errorf("listing modules: %w", err)
	}
	if pending := pendingModules(pm, modules); len(pending) > 0 {
		meta.SetStatusCondition(&pm.Status.Conditions, preTopologyPending(pm.Generation,
			fmt.Sprintf("waiting for pre-topology modules: %v", pending)))
		return false, nil
	}

	meta.SetStatusCondition(&pm.Status.Conditions, preTopologyReady(pm.Generation))
	return true, nil
}

// pendingModules lists the PlatformMesh's pre-topology modules that are not
// ready. A module that has not been reconciled yet counts as pending, so the
// topology never races ahead of one that was only just created. Kept free of
// the client so the gate is testable without a cluster.
func pendingModules(pm *pmdeployv1alpha1.PlatformMesh, modules []pmdeployv1alpha1.OCMModule) []string {
	var pending []string
	for i := range modules {
		mod := &modules[i]
		if mod.Spec.PlatformMeshRef.Name != pm.Name {
			continue
		}
		if mod.Spec.Stage != pmdeployv1alpha1.StagePreTopology {
			continue
		}
		if !meta.IsStatusConditionTrue(mod.Status.Conditions, "Ready") {
			pending = append(pending, mod.Name)
		}
	}
	sort.Strings(pending)
	return pending
}
