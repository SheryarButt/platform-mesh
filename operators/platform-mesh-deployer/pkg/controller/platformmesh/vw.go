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
	"context"
	"fmt"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/celtemplate"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/names"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/operator/v1alpha1"
)

// reconcileVirtualWorkspaces deploys one standalone virtual workspace per shard.
func (r *reconciler) reconcileVirtualWorkspaces(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh) error {
	desired := map[string]struct{}{}

	root := pm.Spec.Topology.RootShard
	if root.VirtualWorkspaces.Mode == pmdeployv1alpha1.VirtualWorkspaceModeStandalone {
		for _, cl := range r.opts.ClustersFor(pm.Name, components.RootShard) {
			name := names.VirtualWorkspace(pm.Name, root.Name, cl.ClusterID)
			shard := names.RootShard(pm.Name, root.Name, cl.ClusterID)
			target := operatorv1alpha1.VirtualWorkspaceTarget{RootShardRef: &corev1.LocalObjectReference{Name: shard}}
			if err := r.reconcileVirtualWorkspace(ctx, pm, root.VirtualWorkspaces, root.Name, name, cl.ClusterID, target); err != nil {
				return err
			}
			desired[name] = struct{}{}
		}
	}

	for i := range pm.Spec.Topology.ShardGroups {
		group := pm.Spec.Topology.ShardGroups[i]
		if group.VirtualWorkspaces.Mode != pmdeployv1alpha1.VirtualWorkspaceModeStandalone {
			continue
		}
		for _, cl := range r.opts.ClustersFor(pm.Name, components.Shard(group.Name)) {
			name := names.VirtualWorkspace(pm.Name, group.Name, cl.ClusterID)
			shard := names.Shard(pm.Name, group.Name, cl.ClusterID)
			target := operatorv1alpha1.VirtualWorkspaceTarget{ShardRef: &corev1.LocalObjectReference{Name: shard}}
			if err := r.reconcileVirtualWorkspace(ctx, pm, group.VirtualWorkspaces, group.Name, name, cl.ClusterID, target); err != nil {
				return err
			}
			desired[name] = struct{}{}
		}
	}

	return r.opts.Teardown(ctx, pm, components.VirtualWorkspace, &operatorv1alpha1.VirtualWorkspaceList{}, desired)
}

func (r *reconciler) reconcileVirtualWorkspace(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh, vws pmdeployv1alpha1.VirtualWorkspaceSpec, shardGroup, name, clusterID string, target operatorv1alpha1.VirtualWorkspaceTarget) error {
	celCtx := celtemplate.Context{
		PlatformMesh: pm.Name,
		Component:    components.VirtualWorkspace,
		ShardGroup:   shardGroup,
		Cluster:      clusterID,
	}

	var spec operatorv1alpha1.VirtualWorkspaceSpec
	tpl := &pmdeployv1alpha1.VirtualWorkspaceTemplate{}
	if err := r.resolveTemplate(ctx, pm, vws.TemplateRef, tpl, func() any { return tpl.Spec }, &spec); err != nil {
		return err
	}

	spec.Target = target

	if vws.Exposure == nil {
		return fmt.Errorf("virtual workspace %q: a standalone server needs an exposure", name)
	}
	host, err := celtemplate.Eval(vws.Exposure.HostnameTemplate, celCtx)
	if err != nil {
		return fmt.Errorf("virtual workspace %q hostname: %w", name, err)
	}
	spec.External.Hostname = host
	spec.External.Port = uint32(vws.Exposure.Port)

	vw := &operatorv1alpha1.VirtualWorkspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: pm.Namespace}}
	return r.opts.Apply(ctx, pm, vw, func() {
		vw.Labels = labels(pm.Name, components.VirtualWorkspace, clusterID)
		vw.Spec = spec
	})
}
