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

package ocmmodule_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

func engage(t *testing.T, r *clusters.Registry, names ...string) {
	t.Helper()
	for _, name := range names {
		require.NoError(t, r.Engage(context.Background(), multicluster.ClusterName(name), nil))
	}
}

func moduleWith(components ...pmdeployv1alpha1.OCMModuleComponent) *pmdeployv1alpha1.OCMModule {
	return &pmdeployv1alpha1.OCMModule{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", Namespace: "pm"},
		Spec: pmdeployv1alpha1.OCMModuleSpec{
			PlatformMeshRef: corev1.LocalObjectReference{Name: "customer-a"},
			Stage:           pmdeployv1alpha1.StagePostTopology,
			Component:       "github.com/platform-mesh/e2e-acme",
			Version:         "0.1.0",
			Components:      components,
		},
	}
}

func component(name string, placement pmdeployv1alpha1.Placement) pmdeployv1alpha1.OCMModuleComponent {
	return pmdeployv1alpha1.OCMModuleComponent{
		Name:      name,
		Resource:  name + "-manifests",
		Placement: placement,
		Namespace: "acme-system",
	}
}

func TestFanOut(t *testing.T) {
	tests := []struct {
		name      string
		engaged   []string
		component pmdeployv1alpha1.OCMModuleComponent
		// want is the expected list of "<clusterID>[/<shardGroup>]".
		want []string
	}{
		{
			name:      "root-shard",
			engaged:   []string{"rootshard#customer-a--east", "frontproxy#customer-a--fp", "shards-default#customer-a--s1"},
			component: component("controller", pmdeployv1alpha1.PlacementRootShard),
			want:      []string{"east"},
		},
		{
			name:      "per-front-proxy",
			engaged:   []string{"rootshard#customer-a--east", "frontproxy#customer-a--fp"},
			component: component("gateway", pmdeployv1alpha1.PlacementPerFrontProxy),
			want:      []string{"fp"},
		},
		{
			name:      "per-shard across groups",
			engaged:   []string{"shards-default#customer-a--s1", "shards-default#customer-a--s2", "shards-eu#customer-a--s3"},
			component: component("agent", pmdeployv1alpha1.PlacementPerShard),
			want:      []string{"s1/default", "s2/default", "s3/eu"},
		},
		{
			name:      "all-clusters dedups a cluster engaged twice",
			engaged:   []string{"rootshard#customer-a--east", "frontproxy#customer-a--east", "shards-default#customer-a--s1"},
			component: component("bootstrap", pmdeployv1alpha1.PlacementAllClusters),
			want:      []string{"east", "s1"},
		},
		{
			name:      "other PlatformMesh is ignored",
			engaged:   []string{"rootshard#customer-b--east"},
			component: component("controller", pmdeployv1alpha1.PlacementRootShard),
			want:      nil,
		},
		{
			name:      "no engaged cluster yields no instances",
			engaged:   nil,
			component: component("controller", pmdeployv1alpha1.PlacementRootShard),
			want:      nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := clusters.NewRegistry()
			engage(t, reg, tt.engaged...)

			got, err := ocmmodule.FanOut(reg, moduleWith(tt.component))
			require.NoError(t, err)

			var ids []string
			for _, inst := range got {
				id := inst.Cluster.ClusterID
				if inst.ShardGroup != "" {
					id += "/" + inst.ShardGroup
				}
				ids = append(ids, id)
			}
			assert.ElementsMatch(t, tt.want, ids)
		})
	}
}

func TestFanOutMultipleComponents(t *testing.T) {
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east", "shards-default#customer-a--s1", "shards-default#customer-a--s2")

	mod := moduleWith(
		component("controller", pmdeployv1alpha1.PlacementRootShard),
		component("agent", pmdeployv1alpha1.PlacementPerShard),
	)
	got, err := ocmmodule.FanOut(reg, mod)
	require.NoError(t, err)
	require.Len(t, got, 3)

	byComponent := map[string]int{}
	for _, inst := range got {
		byComponent[inst.Component.Name]++
	}
	assert.Equal(t, map[string]int{"controller": 1, "agent": 2}, byComponent)
}

func TestFanOutUnknownPlacement(t *testing.T) {
	reg := clusters.NewRegistry()
	mod := moduleWith(component("broken", pmdeployv1alpha1.Placement("nowhere")))
	_, err := ocmmodule.FanOut(reg, mod)
	require.Error(t, err)
}
