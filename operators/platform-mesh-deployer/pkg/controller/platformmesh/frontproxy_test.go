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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/names"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	operatorv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/operator/v1alpha1"
)

// moduleWithMapping builds a OCMModule whose status already carries a resolved
// mapping, which is what the topology merges into the front proxy.
func moduleWithMapping(name, component, path string) *pmdeployv1alpha1.OCMModule {
	return &pmdeployv1alpha1.OCMModule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "pm"},
		Spec: pmdeployv1alpha1.OCMModuleSpec{
			PlatformMeshRef: corev1.LocalObjectReference{Name: "customer-a"},
			Stage:           pmdeployv1alpha1.StagePostTopology,
			Component:       "github.com/platform-mesh/" + name,
			Version:         "0.1.0",
		},
		Status: pmdeployv1alpha1.OCMModuleStatus{
			Components: []pmdeployv1alpha1.OCMModuleComponentStatus{{
				Name: component,
				Instances: []pmdeployv1alpha1.OCMModuleInstanceStatus{{
					Cluster: "fp",
					Mapping: &pmdeployv1alpha1.ResolvedMapping{
						Path:    path,
						Backend: "https://" + name + "." + component + ".svc:8443",
					},
				}},
			}},
		},
	}
}

// The default "/services/" mapping is a prefix of every module path, so module
// mappings must be ordered longest first.
func TestFrontProxyMappingsSortedLongestFirst(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	objs := []ctrlruntimeclient.Object{
		pm,
		rootShardTemplate(),
		shardTemplate(),
		moduleWithMapping("acme", "vw", "/services/acme/"),
		moduleWithMapping("other", "vw", "/services/other/deeper/"),
	}
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build()

	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")

	_, err := newReconciler(t, cl, reg, pm).reconcileTopology(t.Context())
	require.NoError(t, err)

	fp := &operatorv1alpha1.FrontProxy{}
	require.NoError(t, cl.Get(t.Context(),
		ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: names.FrontProxy("customer-a", "fp", "fp")}, fp))

	require.Len(t, fp.Spec.AdditionalPathMappings, 2)
	assert.Equal(t, "/services/other/deeper/", fp.Spec.AdditionalPathMappings[0].Path)
	assert.Equal(t, "/services/acme/", fp.Spec.AdditionalPathMappings[1].Path)
	assert.Equal(t, "/etc/kcp/tls/ca/tls.crt", fp.Spec.AdditionalPathMappings[0].BackendServerCA)
}

// Two modules claiming one path would both be written and the front proxy would
// route by whichever won the sort.
func TestFrontProxyRejectsConflictingMappings(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	objs := []ctrlruntimeclient.Object{
		pm,
		rootShardTemplate(),
		shardTemplate(),
		moduleWithMapping("acme", "vw", "/services/shared/"),
		moduleWithMapping("other", "vw", "/services/shared/"),
	}
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build()

	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")

	_, err := newReconciler(t, cl, reg, pm).reconcileTopology(t.Context())
	require.ErrorContains(t, err, "claimed by both")
}

// A PlatformMesh without modules gets no additional mappings.
func TestFrontProxyWithoutModules(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), shardTemplate()).Build()

	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")

	_, err := newReconciler(t, cl, reg, pm).reconcileTopology(t.Context())
	require.NoError(t, err)

	fp := &operatorv1alpha1.FrontProxy{}
	require.NoError(t, cl.Get(t.Context(),
		ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: names.FrontProxy("customer-a", "fp", "fp")}, fp))
	assert.Empty(t, fp.Spec.AdditionalPathMappings)
}

// A FrontProxyTemplate is the only way a component installed by something other
// than an OCMModule can claim a path, so its mappings and the modules' end up on
// one FrontProxy and have to be reconciled rather than concatenated.
func TestMergePathMappings(t *testing.T) {
	t.Parallel()

	entry := func(path string) operatorv1alpha1.PathMappingEntry {
		return operatorv1alpha1.PathMappingEntry{Path: path, Backend: "https://x.svc:6443"}
	}
	owned := func(path, owner string) ownedMapping {
		return ownedMapping{entry: entry(path), owner: owner}
	}
	paths := func(in []operatorv1alpha1.PathMappingEntry) []string {
		out := make([]string, 0, len(in))
		for _, e := range in {
			out = append(out, e.Path)
		}
		return out
	}

	// The case appending got wrong: a template's short path landed ahead of a
	// module's longer one, where it shadows it. Sorting each source alone cannot
	// fix this — only sorting the merged list can.
	t.Run("a template path is ordered against module paths, not before them", func(t *testing.T) {
		got, err := mergePathMappings(
			[]operatorv1alpha1.PathMappingEntry{entry("/services/")},
			[]ownedMapping{owned("/services/tenancy/", "tenancy/vw")},
		)
		require.NoError(t, err)
		assert.Equal(t, []string{"/services/tenancy/", "/services/"}, paths(got))
	})

	t.Run("several template mappings merge with several module mappings", func(t *testing.T) {
		got, err := mergePathMappings(
			[]operatorv1alpha1.PathMappingEntry{entry("/services/a/"), entry("/services/aa/deeper/")},
			[]ownedMapping{owned("/services/bbb/", "m/one"), owned("/services/b/", "m/two")},
		)
		require.NoError(t, err)
		assert.Equal(t, []string{
			"/services/aa/deeper/",
			"/services/bbb/",
			"/services/a/",
			"/services/b/",
		}, paths(got))
	})

	// Silently accepted before, because the collision check only ever compared
	// modules with each other.
	t.Run("a template path colliding with a module path is refused", func(t *testing.T) {
		_, err := mergePathMappings(
			[]operatorv1alpha1.PathMappingEntry{entry("/services/tenancy/")},
			[]ownedMapping{owned("/services/tenancy/", "tenancy/vw")},
		)
		require.ErrorContains(t, err, "claimed by both")
		require.ErrorContains(t, err, templateOwner)
		require.ErrorContains(t, err, "tenancy/vw")
	})

	// Every instance of one component resolves to the same path — that is one
	// claim seen twice, not two owners.
	t.Run("one owner claiming a path repeatedly is not a conflict", func(t *testing.T) {
		got, err := mergePathMappings(nil, []ownedMapping{
			owned("/services/tenancy/", "tenancy/vw"),
			owned("/services/tenancy/", "tenancy/vw"),
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"/services/tenancy/", "/services/tenancy/"}, paths(got))
	})

	t.Run("nothing to merge is empty, not nil-dereferencing", func(t *testing.T) {
		got, err := mergePathMappings(nil, nil)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}
