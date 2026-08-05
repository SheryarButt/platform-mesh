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

// Package templates resolves which PlatformMeshes reference a topology
// template and holds an in-use finalizer on the ones that are referenced.
// Templates are shared, so deleting one can break several installations.
package templates

import (
	"context"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Key identifies one template.
type Key struct {
	Kind      string
	Namespace string
	Name      string
}

// Kind is one topology template kind, with a constructor for its empty object.
type Kind struct {
	Kind   string
	Object func() ctrlruntimeclient.Object
}

// Kinds are the topology template kinds a PlatformMesh references.
var Kinds = []Kind{
	{"RootShardTemplate", func() ctrlruntimeclient.Object { return &pmdeployv1alpha1.RootShardTemplate{} }},
	{"ShardTemplate", func() ctrlruntimeclient.Object { return &pmdeployv1alpha1.ShardTemplate{} }},
	{"FrontProxyTemplate", func() ctrlruntimeclient.Object { return &pmdeployv1alpha1.FrontProxyTemplate{} }},
	{"CacheServerTemplate", func() ctrlruntimeclient.Object { return &pmdeployv1alpha1.CacheServerTemplate{} }},
	{"VirtualWorkspaceTemplate", func() ctrlruntimeclient.Object { return &pmdeployv1alpha1.VirtualWorkspaceTemplate{} }},
}

// Refs are the templates a PlatformMesh references, with a nil namespace on a
// reference defaulted to the PlatformMesh's own.
func Refs(pm *pmdeployv1alpha1.PlatformMesh) map[Key]struct{} {
	out := map[Key]struct{}{}
	add := func(kind string, ref *pmdeployv1alpha1.TemplateReference) {
		if ref == nil {
			return
		}
		namespace := ref.Namespace
		if namespace == "" {
			namespace = pm.Namespace
		}
		out[Key{Kind: kind, Namespace: namespace, Name: ref.Name}] = struct{}{}
	}

	t := pm.Spec.Topology
	add("RootShardTemplate", t.RootShard.TemplateRef)
	add("VirtualWorkspaceTemplate", t.RootShard.VirtualWorkspaces.TemplateRef)
	add("FrontProxyTemplate", t.FrontProxy.TemplateRef)
	if t.CacheServer != nil {
		add("CacheServerTemplate", t.CacheServer.TemplateRef)
	}
	for i := range t.ShardGroups {
		add("ShardTemplate", t.ShardGroups[i].TemplateRef)
		add("VirtualWorkspaceTemplate", t.ShardGroups[i].VirtualWorkspaces.TemplateRef)
	}
	return out
}

// PlatformMeshesUsing lists the PlatformMeshes referencing a template. A
// template is shared, so this is not limited to one.
func PlatformMeshesUsing(ctx context.Context, c ctrlruntimeclient.Client, key Key) ([]pmdeployv1alpha1.PlatformMesh, error) {
	list := &pmdeployv1alpha1.PlatformMeshList{}
	if err := c.List(ctx, list); err != nil {
		return nil, err
	}
	var out []pmdeployv1alpha1.PlatformMesh
	for i := range list.Items {
		if _, ok := Refs(&list.Items[i])[key]; ok {
			out = append(out, list.Items[i])
		}
	}
	return out, nil
}

// enqueueTemplatesOfPlatformMesh maps a PlatformMesh to the templates of the
// given kind it references, so they can pick up or drop the in-use finalizer.
func enqueueTemplatesOfPlatformMesh(kind string) handler.MapFunc {
	return func(_ context.Context, obj ctrlruntimeclient.Object) []reconcile.Request {
		pm, ok := obj.(*pmdeployv1alpha1.PlatformMesh)
		if !ok {
			return nil
		}
		var reqs []reconcile.Request
		for key := range Refs(pm) {
			if key.Kind != kind {
				continue
			}
			reqs = append(reqs, reconcile.Request{NamespacedName: ctrlruntimeclient.ObjectKey{
				Namespace: key.Namespace, Name: key.Name,
			}})
		}
		return reqs
	}
}
