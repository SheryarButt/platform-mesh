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

func (r *reconciler) reconcileShards(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh) error {
	rootRef, err := r.rootShardRef(pm)
	if err != nil {
		return err
	}

	for i := range pm.Spec.Topology.ShardGroups {
		group := pm.Spec.Topology.ShardGroups[i]
		// "shards-<group>": the multi provider prefix carried by the group's engaged cluster names.
		component := components.Shard(group.Name)
		engaged := r.opts.ClustersFor(pm.Name, component)

		desired := map[string]struct{}{}
		for _, cl := range engaged {
			name := names.Shard(pm.Name, group.Name, cl.ClusterID)
			spec, err := r.buildShardSpec(ctx, pm, group, cl.ClusterID, rootRef)
			if err != nil {
				return err
			}
			sh := &operatorv1alpha1.Shard{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: pm.Namespace}}
			if err := r.opts.Apply(ctx, pm, sh, func() {
				sh.Labels = labels(pm.Name, component, cl.ClusterID)
				sh.Spec = spec
			}); err != nil {
				return err
			}
			desired[name] = struct{}{}
		}
		if err := r.opts.Teardown(ctx, pm, component, &operatorv1alpha1.ShardList{}, desired); err != nil {
			return err
		}
	}
	return nil
}

func (r *reconciler) buildShardSpec(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh, group pmdeployv1alpha1.ShardGroup, clusterID, rootRef string) (operatorv1alpha1.ShardSpec, error) {
	name := names.Shard(pm.Name, group.Name, clusterID)
	celCtx := celtemplate.Context{
		PlatformMesh: pm.Name,
		Component:    components.Shard(group.Name),
		ShardGroup:   group.Name,
		Cluster:      clusterID,
	}

	var spec operatorv1alpha1.ShardSpec
	tpl := &pmdeployv1alpha1.ShardTemplate{}
	if err := r.resolveTemplate(ctx, pm, group.TemplateRef, tpl, func() any { return tpl.Spec }, &spec); err != nil {
		return spec, err
	}

	if err := resolveEtcd(&spec.Etcd, celCtx, "shard "+name); err != nil {
		return spec, err
	}

	spec.RootShard.Reference = &corev1.LocalObjectReference{Name: rootRef}

	addr, err := resolveAddress(group.Exposure, celCtx, service{
		adminName: name,
		suffix:    shardServiceSuffix,
		port:      shardServicePort,
	}, pm.Namespace, "shard "+name)
	if err != nil {
		return spec, err
	}
	spec.ShardBaseURL = addr.URL()

	// A standalone virtual workspace server only serves the shard that points at
	// it: without the reference kcp-operator leaves the shard serving virtual
	// workspaces in-process and the standalone deployment takes no traffic.
	if group.VirtualWorkspaces.Mode == pmdeployv1alpha1.VirtualWorkspaceModeStandalone {
		spec.KCPVirtualWorkspace = &corev1.LocalObjectReference{
			Name: names.VirtualWorkspace(pm.Name, group.Name, clusterID),
		}
	}

	if group.CacheServerRef != "" {
		ref, err := r.cacheServerRef(pm, group.CacheServerRef)
		if err != nil {
			return spec, fmt.Errorf("shard %q: %w", name, err)
		}
		spec.Cache = &operatorv1alpha1.ShardCacheConfig{Reference: &corev1.LocalObjectReference{Name: ref}}
	}
	return spec, nil
}
