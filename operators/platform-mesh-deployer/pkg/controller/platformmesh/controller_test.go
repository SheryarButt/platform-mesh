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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func namedPlatformMesh(name string) *pmdeployv1alpha1.PlatformMesh {
	return &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "pm"},
	}
}

func moduleOf(name, platformMesh string) *pmdeployv1alpha1.OCMModule {
	return &pmdeployv1alpha1.OCMModule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "pm"},
		Spec: pmdeployv1alpha1.OCMModuleSpec{
			PlatformMeshRef: corev1.LocalObjectReference{Name: platformMesh},
		},
	}
}

func ref(name, namespace string) *pmdeployv1alpha1.TemplateReference {
	return &pmdeployv1alpha1.TemplateReference{Name: name, Namespace: namespace}
}

func templatedPlatformMesh(name, namespace string) *pmdeployv1alpha1.PlatformMesh {
	return &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: pmdeployv1alpha1.PlatformMeshSpec{
			Topology: pmdeployv1alpha1.Topology{
				RootShard: pmdeployv1alpha1.RootShard{
					Name:        "root",
					TemplateRef: ref("root", ""),
					VirtualWorkspaces: pmdeployv1alpha1.VirtualWorkspaceSpec{
						TemplateRef: ref("vw", "shared"),
					},
				},
				FrontProxy: pmdeployv1alpha1.FrontProxy{
					Name:        "fp",
					TemplateRef: ref("fp", ""),
				},
				CacheServer: &pmdeployv1alpha1.CacheServer{
					Name:        "cache",
					TemplateRef: ref("cache", ""),
				},
				ShardGroups: []pmdeployv1alpha1.ShardGroup{{
					Name:        "default",
					TemplateRef: ref("default", ""),
					VirtualWorkspaces: pmdeployv1alpha1.VirtualWorkspaceSpec{
						TemplateRef: ref("vw", "shared"),
					},
				}},
			},
		},
	}
}

// The cluster registry signals by PlatformMesh name, so the mapping has to find
// the object with that name rather than the signalling object itself.
func TestEnqueuePlatformMeshByName(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(namedPlatformMesh("customer-a"), namedPlatformMesh("customer-b")).Build()

	signal := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "customer-a"}}
	got := enqueuePlatformMeshByName(cl)(t.Context(), signal)

	require.Len(t, got, 1)
	assert.Equal(t, "customer-a", got[0].Name)
	assert.Equal(t, "pm", got[0].Namespace)
}

func TestEnqueuePlatformMeshByNameUnknown(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(namedPlatformMesh("customer-a")).Build()

	signal := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "nope"}}
	assert.Empty(t, enqueuePlatformMeshByName(cl)(t.Context(), signal))
}

// A module publishes its front proxy mapping in its status, which the topology
// merges, so the PlatformMesh has to be reconciled again.
func TestEnqueuePlatformMeshOfModule(t *testing.T) {
	t.Parallel()
	got := enqueuePlatformMeshOfModule()(t.Context(), moduleOf("acme", "customer-a"))

	require.Len(t, got, 1)
	assert.Equal(t, "customer-a", got[0].Name)
	assert.Equal(t, "pm", got[0].Namespace)
}

func TestEnqueuePlatformMeshOfModuleWrongType(t *testing.T) {
	t.Parallel()
	assert.Empty(t, enqueuePlatformMeshOfModule()(t.Context(), namedPlatformMesh("customer-a")))
}

func TestEnqueuePlatformMeshesUsingTemplate(t *testing.T) {
	t.Parallel()
	// Two installations in different namespaces sharing one template.
	a := templatedPlatformMesh("customer-a", "pm-a")
	b := templatedPlatformMesh("customer-b", "pm-b")
	unrelated := templatedPlatformMesh("customer-c", "pm-c")
	unrelated.Spec.Topology.RootShard.VirtualWorkspaces.TemplateRef = ref("other", "shared")
	unrelated.Spec.Topology.ShardGroups[0].VirtualWorkspaces.TemplateRef = nil

	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a, b, unrelated).Build()
	shared := &pmdeployv1alpha1.VirtualWorkspaceTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "vw", Namespace: "shared"},
	}

	got := enqueuePlatformMeshesUsingTemplate(cl, "VirtualWorkspaceTemplate")(t.Context(), shared)
	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: ctrlruntimeclient.ObjectKey{Namespace: "pm-a", Name: "customer-a"}},
		{NamespacedName: ctrlruntimeclient.ObjectKey{Namespace: "pm-b", Name: "customer-b"}},
	}, got)
}

// A failing client must not enqueue anything rather than panic.
func TestEnqueueWithFailingClient(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()

	signal := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "customer-a"}}
	assert.Empty(t, enqueuePlatformMeshByName(cl)(t.Context(), signal))
	assert.Empty(t, enqueuePlatformMeshesUsingTemplate(cl, "RootShardTemplate")(t.Context(), namedPlatformMesh("customer-a")))
}
