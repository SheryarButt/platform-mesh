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

package templates

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, pmdeployv1alpha1.AddToScheme(s))
	return s
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

func TestRefs(t *testing.T) {
	t.Run("defaults the namespace and dedupes", func(t *testing.T) {
		got := Refs(templatedPlatformMesh("customer-a", "pm"))
		assert.Equal(t, map[Key]struct{}{
			{Kind: "RootShardTemplate", Namespace: "pm", Name: "root"}:          {},
			{Kind: "FrontProxyTemplate", Namespace: "pm", Name: "fp"}:           {},
			{Kind: "CacheServerTemplate", Namespace: "pm", Name: "cache"}:       {},
			{Kind: "ShardTemplate", Namespace: "pm", Name: "default"}:           {},
			{Kind: "VirtualWorkspaceTemplate", Namespace: "shared", Name: "vw"}: {},
		}, got)
	})

	t.Run("ignores unset refs", func(t *testing.T) {
		pm := &pmdeployv1alpha1.PlatformMesh{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "pm"}}
		assert.Empty(t, Refs(pm))
	})
}

func TestEnqueueTemplatesOfPlatformMesh(t *testing.T) {
	got := enqueueTemplatesOfPlatformMesh("VirtualWorkspaceTemplate")(t.Context(), templatedPlatformMesh("customer-a", "pm"))
	assert.Equal(t, []reconcile.Request{
		{NamespacedName: ctrlruntimeclient.ObjectKey{Namespace: "shared", Name: "vw"}},
	}, got)
}
