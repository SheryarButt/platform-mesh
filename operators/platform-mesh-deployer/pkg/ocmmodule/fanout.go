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
	"fmt"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"
)

// Instance is one component placed on one cluster.
type Instance struct {
	Component pmdeployv1alpha1.OCMModuleComponent
	Cluster   clusters.Cluster
	// ShardGroup is set for per-shard instances, empty otherwise.
	ShardGroup string
}

// FanOut resolves each component's placement into the clusters it is deployed to.
// A component whose placement has no engaged cluster yields no instances, which is not an error: the cluster may be engaged later.
func FanOut(registry *clusters.Registry, mod *pmdeployv1alpha1.OCMModule) ([]Instance, error) {
	pm := mod.Spec.PlatformMeshRef.Name

	var out []Instance
	for _, component := range mod.Spec.Components {
		switch component.Placement {
		case pmdeployv1alpha1.PlacementRootShard:
			for _, c := range registry.ClustersFor(pm, components.RootShard) {
				out = append(out, Instance{Component: component, Cluster: c})
			}
		case pmdeployv1alpha1.PlacementPerFrontProxy:
			for _, c := range registry.ClustersFor(pm, components.FrontProxy) {
				out = append(out, Instance{Component: component, Cluster: c})
			}
		case pmdeployv1alpha1.PlacementPerShard:
			for _, group := range registry.ShardGroups(pm) {
				for _, c := range registry.ClustersFor(pm, components.Shard(group)) {
					out = append(out, Instance{Component: component, Cluster: c, ShardGroup: group})
				}
			}
		case pmdeployv1alpha1.PlacementAllClusters:
			for _, c := range registry.AllClustersFor(pm) {
				out = append(out, Instance{Component: component, Cluster: c})
			}
		default:
			return nil, fmt.Errorf("component %q: unknown placement %q", component.Name, component.Placement)
		}
	}
	return out, nil
}
