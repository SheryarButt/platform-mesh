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

package composition

import (
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func obj(gvk schema.GroupVersionKind, ns, name string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetGroupVersionKind(gvk)
	o.SetName(name)
	if ns != "" {
		o.SetNamespace(ns)
	}
	o.SetUID(types.UID("uid-" + name))
	return o
}

var (
	widgetGVK    = schema.GroupVersionKind{Group: "apps.example.com", Version: "v1alpha1", Kind: "Widget"}
	configMapGVK = schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	namespaceGVK = schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}
)

func TestSetOwnerRef_CoLocated(t *testing.T) {
	t.Parallel()
	inst := obj(widgetGVK, "team", "w1")
	child := obj(configMapGVK, "team", "w1-cm")

	setOwnerRef(child, inst)

	refs := child.GetOwnerReferences()
	require.Len(t, refs, 1)
	r := refs[0]
	require.Equal(t, "Widget", r.Kind)
	require.Equal(t, "w1", r.Name)
	require.Equal(t, inst.GetUID(), r.UID)
	require.NotNil(t, r.Controller)
	require.True(t, *r.Controller, "should be a controller ref")
	require.NotNil(t, r.BlockOwnerDeletion)
	require.True(t, *r.BlockOwnerDeletion, "should block owner deletion")
}

func TestSetOwnerRef_SkipsWhenNotCoLocated(t *testing.T) {
	t.Parallel()
	inst := obj(widgetGVK, "team", "w1")

	// child in a different namespace
	crossNS := obj(configMapGVK, "other", "x")
	setOwnerRef(crossNS, inst)
	require.Empty(t, crossNS.GetOwnerReferences(), "cross-namespace child must not get an owner ref")

	// cluster-scoped child (no namespace)
	clusterChild := obj(namespaceGVK, "", "ns1")
	setOwnerRef(clusterChild, inst)
	require.Empty(t, clusterChild.GetOwnerReferences(), "cluster-scoped child cannot be owned by a namespaced instance")

	// cluster-scoped instance (no namespace) cannot own a namespaced child
	clusterInst := obj(schema.GroupVersionKind{Group: "apps.example.com", Version: "v1alpha1", Kind: "ClusterWidget"}, "", "cw1")
	child := obj(configMapGVK, "team", "c")
	setOwnerRef(child, clusterInst)
	require.Empty(t, child.GetOwnerReferences(), "cluster-scoped instance cannot own a namespaced child")
}
