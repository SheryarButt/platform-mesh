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

package sync_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.platform-mesh.io/platform-mesh-deployer/pkg/sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	return s
}

var configMapGVK = schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}

func configMap(name, namespace string, labels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(configMapGVK)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	obj.SetLabels(labels)
	return obj
}

func TestStrip(t *testing.T) {
	obj := configMap("cm", "ns", map[string]string{"keep": "me"})
	obj.SetResourceVersion("42")
	obj.SetUID(types.UID("abc"))
	obj.SetGeneration(7)
	obj.SetCreationTimestamp(metav1.NewTime(time.Now()))
	obj.SetFinalizers([]string{"pending"})
	obj.SetOwnerReferences([]metav1.OwnerReference{{Name: "owner", Kind: "X", APIVersion: "v1", UID: "u"}})
	obj.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "kubectl"}})
	require.NoError(t, unstructured.SetNestedField(obj.Object, "value", "metadata", "selfLink"))
	require.NoError(t, unstructured.SetNestedField(obj.Object, map[string]any{"phase": "Bound"}, "status"))

	sync.Strip(obj)

	assert.Empty(t, obj.GetResourceVersion())
	assert.Empty(t, string(obj.GetUID()))
	assert.Zero(t, obj.GetGeneration())
	created := obj.GetCreationTimestamp()
	assert.True(t, created.IsZero())
	assert.Nil(t, obj.GetFinalizers())
	assert.Nil(t, obj.GetOwnerReferences())
	assert.Nil(t, obj.GetManagedFields())

	_, found, err := unstructured.NestedString(obj.Object, "metadata", "selfLink")
	require.NoError(t, err)
	assert.False(t, found)
	_, found, err = unstructured.NestedMap(obj.Object, "status")
	require.NoError(t, err)
	assert.False(t, found, "status must not travel to the target cluster")

	// Identity and payload survive.
	assert.Equal(t, "cm", obj.GetName())
	assert.Equal(t, "ns", obj.GetNamespace())
	assert.Equal(t, map[string]string{"keep": "me"}, obj.GetLabels())
}

func TestEnsureNamespace(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).Build()

	require.NoError(t, sync.EnsureNamespace(t.Context(), cl, "acme"))
	var ns corev1.Namespace
	require.NoError(t, cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Name: "acme"}, &ns))

	// Idempotent.
	require.NoError(t, sync.EnsureNamespace(t.Context(), cl, "acme"))

	// An empty name is a no-op, not an error.
	require.NoError(t, sync.EnsureNamespace(t.Context(), cl, ""))
}

func TestApply(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).Build()

	obj := configMap("cm", "ns", map[string]string{"a": "1"})
	require.NoError(t, unstructured.SetNestedStringMap(obj.Object, map[string]string{"key": "value"}, "data"))
	require.NoError(t, sync.Apply(t.Context(), cl, obj))

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(configMapGVK)
	require.NoError(t, cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "ns", Name: "cm"}, got))
	data, _, err := unstructured.NestedStringMap(got.Object, "data")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"key": "value"}, data)

	// Re-applying with changed content converges.
	obj = configMap("cm", "ns", map[string]string{"a": "2"})
	require.NoError(t, unstructured.SetNestedStringMap(obj.Object, map[string]string{"key": "other"}, "data"))
	require.NoError(t, sync.Apply(t.Context(), cl, obj))

	require.NoError(t, cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "ns", Name: "cm"}, got))
	data, _, err = unstructured.NestedStringMap(got.Object, "data")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"key": "other"}, data)
	assert.Equal(t, "2", got.GetLabels()["a"])
}

func TestPrune(t *testing.T) {
	owned := map[string]string{"deploy.platform-mesh.io/ocmmodule": "acme"}
	foreign := map[string]string{"other": "true"}

	keep := configMap("keep", "ns", owned)
	drop := configMap("drop", "ns", owned)
	untouched := configMap("untouched", "ns", foreign)

	cl := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(keep, drop, untouched).Build()

	err := sync.Prune(t.Context(), cl,
		[]schema.GroupVersionKind{configMapGVK},
		owned,
		map[sync.ObjectKey]struct{}{sync.KeyOf(keep): {}},
	)
	require.NoError(t, err)

	get := func(name string) error {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(configMapGVK)
		return cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "ns", Name: name}, obj)
	}
	assert.NoError(t, get("keep"), "desired object must survive")
	assert.True(t, apierrors.IsNotFound(get("drop")), "undesired owned object must be deleted")
	assert.NoError(t, get("untouched"), "objects outside the selector must not be touched")
}

func TestPruneEmptyKeepDeletesAll(t *testing.T) {
	owned := map[string]string{"deploy.platform-mesh.io/ocmmodule": "acme"}
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(configMap("a", "ns", owned), configMap("b", "ns", owned)).Build()

	require.NoError(t, sync.Prune(t.Context(), cl,
		[]schema.GroupVersionKind{configMapGVK}, owned, nil))

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMapList"})
	require.NoError(t, cl.List(t.Context(), list, ctrlruntimeclient.MatchingLabels(owned)))
	assert.Empty(t, list.Items)
}

func TestKeyOf(t *testing.T) {
	obj := configMap("cm", "ns", nil)
	assert.Equal(t, sync.ObjectKey{GVK: configMapGVK, Namespace: "ns", Name: "cm"}, sync.KeyOf(obj))
}

// deploymentGVK is a kind with both a spec and a status, unlike ConfigMap.
var deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

func deployment(name, namespace string, replicas int64) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(deploymentGVK)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	obj.SetLabels(map[string]string{"owner": "deployer"})
	if err := unstructured.SetNestedField(obj.Object, replicas, "spec", "replicas"); err != nil {
		panic(err)
	}
	if err := unstructured.SetNestedStringMap(obj.Object, map[string]string{"app": name}, "spec", "selector", "matchLabels"); err != nil {
		panic(err)
	}
	return obj
}

func TestCopySpec(t *testing.T) {
	src := deployment("app", "ns", 2)
	// The source's status must not travel with the spec.
	require.NoError(t, unstructured.SetNestedField(src.Object, int64(2), "status", "readyReplicas"))

	dst := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	key := ctrlruntimeclient.ObjectKey{Namespace: "ns", Name: "app"}
	require.NoError(t, sync.CopySpec(t.Context(), dst, deploymentGVK, key, src))

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(deploymentGVK)
	require.NoError(t, dst.Get(t.Context(), key, got))
	replicas, _, err := unstructured.NestedInt64(got.Object, "spec", "replicas")
	require.NoError(t, err)
	assert.Equal(t, int64(2), replicas)
	assert.Equal(t, "deployer", got.GetLabels()["owner"])
	status, _, err := unstructured.NestedMap(got.Object, "status")
	require.NoError(t, err)
	assert.Empty(t, status, "status belongs to the target cluster's controller")

	// A changed spec converges on the next copy.
	require.NoError(t, sync.CopySpec(t.Context(), dst, deploymentGVK, key, deployment("app", "ns", 5)))
	require.NoError(t, dst.Get(t.Context(), key, got))
	replicas, _, err = unstructured.NestedInt64(got.Object, "spec", "replicas")
	require.NoError(t, err)
	assert.Equal(t, int64(5), replicas)
}

func TestReflectStatus(t *testing.T) {
	key := ctrlruntimeclient.ObjectKey{Namespace: "ns", Name: "app"}

	rendered := deployment("app", "ns", 2)
	require.NoError(t, unstructured.SetNestedField(rendered.Object, int64(2), "status", "readyReplicas"))
	from := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(rendered).WithStatusSubresource(rendered).Build()

	owner := deployment("app", "ns", 2)
	to := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(owner).WithStatusSubresource(owner).Build()

	require.NoError(t, sync.ReflectStatus(t.Context(), from, to, deploymentGVK, key))

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(deploymentGVK)
	require.NoError(t, to.Get(t.Context(), key, got))
	ready, found, err := unstructured.NestedInt64(got.Object, "status", "readyReplicas")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(2), ready)
}

func TestReflectStatusToleratesMissingObjects(t *testing.T) {
	key := ctrlruntimeclient.ObjectKey{Namespace: "ns", Name: "app"}
	empty := fake.NewClientBuilder().WithScheme(scheme(t)).Build()

	// Nothing rendered yet on the source side.
	require.NoError(t, sync.ReflectStatus(t.Context(), empty, empty, deploymentGVK, key))

	// Rendered, but the owning object is gone.
	rendered := deployment("app", "ns", 1)
	require.NoError(t, unstructured.SetNestedField(rendered.Object, int64(1), "status", "readyReplicas"))
	from := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(rendered).WithStatusSubresource(rendered).Build()
	require.NoError(t, sync.ReflectStatus(t.Context(), from, empty, deploymentGVK, key))
}

func TestReflectStatusWithoutStatusIsNoop(t *testing.T) {
	key := ctrlruntimeclient.ObjectKey{Namespace: "ns", Name: "app"}
	rendered := deployment("app", "ns", 1)
	from := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(rendered).Build()
	to := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(deployment("app", "ns", 1)).Build()

	require.NoError(t, sync.ReflectStatus(t.Context(), from, to, deploymentGVK, key))
}
