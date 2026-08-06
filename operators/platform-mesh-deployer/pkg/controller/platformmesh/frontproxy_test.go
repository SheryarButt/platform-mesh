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
