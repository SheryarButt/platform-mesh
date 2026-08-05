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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocm"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const deploymentManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${module}-agent
spec:
  replicas: ${values.replicas}
  template:
    spec:
      containers:
        - name: agent
          image: ${values.image}
          envFrom:
            - configMapRef:
                name: ${configMap}
`

func TestResolve(t *testing.T) {
	cv := newFakeCV(map[string]string{"agent-manifests": deploymentManifest})
	mod := moduleWith(component("agent", pmdeployv1alpha1.PlacementPerShard))
	fallback := &pmdeployv1alpha1.OCMRepository{URL: "http://registry:5000"}

	resolver := &fakeResolver{cv: cv}
	got, err := ocmmodule.Resolve(t.Context(), resolver, mod, fallback)
	require.NoError(t, err)
	assert.Same(t, cv, got.CV)
	assert.Equal(t, "http://registry:5000", resolver.gotURL, "falls back to the PlatformMesh repository")
}

func TestResolveUsesModuleRepository(t *testing.T) {
	mod := moduleWith(component("agent", pmdeployv1alpha1.PlacementPerShard))
	mod.Spec.OCM = &pmdeployv1alpha1.OCMRepository{URL: "http://own:5000"}

	resolver := &fakeResolver{cv: newFakeCV(map[string]string{"agent-manifests": deploymentManifest})}
	_, err := ocmmodule.Resolve(t.Context(), resolver, mod, &pmdeployv1alpha1.OCMRepository{URL: "http://fallback:5000"})
	require.NoError(t, err)
	assert.Equal(t, "http://own:5000", resolver.gotURL)
}

func TestResolveErrors(t *testing.T) {
	comp := component("agent", pmdeployv1alpha1.PlacementPerShard)

	tests := []struct {
		name     string
		resolver *fakeResolver
		fallback *pmdeployv1alpha1.OCMRepository
	}{
		{
			name:     "component version not found",
			resolver: &fakeResolver{err: ocm.ErrNotFound},
			fallback: &pmdeployv1alpha1.OCMRepository{URL: "http://registry:5000"},
		},
		{
			name:     "resolver failure",
			resolver: &fakeResolver{err: errors.New("boom")},
			fallback: &pmdeployv1alpha1.OCMRepository{URL: "http://registry:5000"},
		},
		{
			name:     "declared resource missing from the component version",
			resolver: &fakeResolver{cv: newFakeCV(map[string]string{"other": "{}"})},
			fallback: &pmdeployv1alpha1.OCMRepository{URL: "http://registry:5000"},
		},
		{
			name:     "no repository at all",
			resolver: &fakeResolver{cv: newFakeCV(map[string]string{"agent-manifests": "{}"})},
			fallback: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ocmmodule.Resolve(t.Context(), tt.resolver, moduleWith(comp), tt.fallback)
			require.Error(t, err)
		})
	}
}

// resolvedFor builds a Resolved plus the single instance of its component.
func resolvedFor(t *testing.T, mod *pmdeployv1alpha1.OCMModule, contents map[string]string, engaged string) (*ocmmodule.Resolved, ocmmodule.Instance) {
	t.Helper()
	resolver := &fakeResolver{cv: newFakeCV(contents)}
	resolved, err := ocmmodule.Resolve(t.Context(), resolver, mod, &pmdeployv1alpha1.OCMRepository{URL: "http://registry:5000"})
	require.NoError(t, err)

	reg := clusters.NewRegistry()
	engage(t, reg, engaged)
	instances, err := ocmmodule.FanOut(reg, mod)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	return resolved, instances[0]
}

func TestRender(t *testing.T) {
	mod := moduleWith(component("agent", pmdeployv1alpha1.PlacementPerShard))
	mod.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicas":3,"image":"acme:1.2"}`)}

	resolved, inst := resolvedFor(t, mod,
		map[string]string{"agent-manifests": deploymentManifest},
		"shards-default#customer-a--s1")

	objs, err := resolved.Render(t.Context(), inst, nil)
	require.NoError(t, err)
	require.Len(t, objs, 2, "generated ConfigMap plus the Deployment")

	cm := objs[0]
	assert.Equal(t, "ConfigMap", cm.GetKind())
	assert.Equal(t, "acme-agent", cm.GetName())
	data, _, err := unstructured.NestedStringMap(cm.Object, "data")
	require.NoError(t, err)
	assert.Equal(t, "acme", data["MODULE"])
	assert.Equal(t, "agent", data["COMPONENT"])
	assert.Equal(t, "per-shard", data["PLACEMENT"])
	assert.Equal(t, "s1", data["CLUSTER"])
	assert.Equal(t, "customer-a", data["PLATFORM_MESH"])
	assert.Equal(t, "default", data["SHARD_GROUP"])

	dep := objs[1]
	assert.Equal(t, "Deployment", dep.GetKind())
	assert.Equal(t, "acme-agent", dep.GetName(), "${module} interpolated")
	assert.Equal(t, "acme-system", dep.GetNamespace(), "namespace defaulted from the component")

	// A whole-leaf expression keeps its native type.
	replicas, found, err := unstructured.NestedInt64(dep.Object, "spec", "replicas")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(3), replicas)

	containers, found, err := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, containers, 1)
	container := containers[0].(map[string]any)
	assert.Equal(t, "acme:1.2", container["image"])
	envFrom := container["envFrom"].([]any)[0].(map[string]any)
	assert.Equal(t, "acme-agent", envFrom["configMapRef"].(map[string]any)["name"], "${configMap} points at the generated ConfigMap")

	// Every applied object is labelled for teardown.
	for _, obj := range objs {
		labels := obj.GetLabels()
		assert.Equal(t, "customer-a", labels[ocmmodule.LabelPlatformMesh])
		assert.Equal(t, "acme", labels[ocmmodule.LabelModule])
		assert.Equal(t, "agent", labels[ocmmodule.LabelComponent])
		assert.Equal(t, "s1", labels[ocmmodule.LabelCluster])
	}
}

func TestRenderMultiDocumentManifest(t *testing.T) {
	const manifest = `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${module}-sa
---
apiVersion: v1
kind: Service
metadata:
  name: ${module}-svc
---
`
	mod := moduleWith(component("agent", pmdeployv1alpha1.PlacementPerShard))
	resolved, inst := resolvedFor(t, mod,
		map[string]string{"agent-manifests": manifest},
		"shards-default#customer-a--s1")

	objs, err := resolved.Render(t.Context(), inst, nil)
	require.NoError(t, err)
	require.Len(t, objs, 3, "ConfigMap plus both documents, empty document skipped")
	assert.Equal(t, "acme-sa", objs[1].GetName())
	assert.Equal(t, "acme-svc", objs[2].GetName())
}

func TestRenderKeepsExplicitNamespace(t *testing.T) {
	const manifest = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: pinned
  namespace: kube-system
`
	mod := moduleWith(component("agent", pmdeployv1alpha1.PlacementPerShard))
	resolved, inst := resolvedFor(t, mod,
		map[string]string{"agent-manifests": manifest},
		"shards-default#customer-a--s1")

	objs, err := resolved.Render(t.Context(), inst, nil)
	require.NoError(t, err)
	assert.Equal(t, "kube-system", objs[1].GetNamespace())
}

func TestRenderErrors(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
	}{
		{name: "unknown template variable", manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ${nope}\n"},
		{name: "missing value", manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ${values.absent}\n"},
		{name: "invalid yaml", manifest: "\tnot: [valid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod := moduleWith(component("agent", pmdeployv1alpha1.PlacementPerShard))
			resolved, inst := resolvedFor(t, mod,
				map[string]string{"agent-manifests": tt.manifest},
				"shards-default#customer-a--s1")

			_, err := resolved.Render(t.Context(), inst, nil)
			require.Error(t, err)
		})
	}
}

func TestValues(t *testing.T) {
	mod := moduleWith(component("agent", pmdeployv1alpha1.PlacementPerShard))
	resolved, _ := resolvedFor(t, mod,
		map[string]string{"agent-manifests": "{}"},
		"shards-default#customer-a--s1")

	values, err := resolved.Values()
	require.NoError(t, err)
	assert.Empty(t, values, "absent spec.values yields an empty map, not nil")

	mod.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(`{"a":1}`)}
	values, err = resolved.Values()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"a": int64(1)}, values, "whole numbers stay integers for Kubernetes objects")

	mod.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(`not json`)}
	_, err = resolved.Values()
	require.Error(t, err)
}

func TestSelectors(t *testing.T) {
	mod := moduleWith(component("agent", pmdeployv1alpha1.PlacementPerShard))
	resolved, inst := resolvedFor(t, mod,
		map[string]string{"agent-manifests": "{}"},
		"shards-default#customer-a--s1")

	assert.Equal(t, map[string]string{
		ocmmodule.LabelPlatformMesh: "customer-a",
		ocmmodule.LabelModule:       "acme",
		ocmmodule.LabelComponent:    "agent",
		ocmmodule.LabelCluster:      "s1",
	}, resolved.InstanceSelector(inst))

	assert.Equal(t, map[string]string{
		ocmmodule.LabelPlatformMesh: "customer-a",
		ocmmodule.LabelModule:       "acme",
		ocmmodule.LabelCluster:      "s1",
	}, ocmmodule.OCMModuleSelector(mod, "s1"))
}
