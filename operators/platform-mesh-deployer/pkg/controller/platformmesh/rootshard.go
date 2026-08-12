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

func (r *reconciler) reconcileRootShard(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh) error {
	group := pm.Spec.Topology.RootShard

	engaged := r.opts.ClustersFor(pm.Name, components.RootShard)
	if len(engaged) > 1 {
		return fmt.Errorf("root shard must be a single cluster, got %d engaged", len(engaged))
	}
	if len(engaged) == 0 {
		return fmt.Errorf("no root shard cluster engaged")
	}
	clusterID := engaged[0].ClusterID
	name := names.RootShard(pm.Name, group.Name, clusterID)

	spec, err := r.buildRootShardSpec(ctx, pm, group, clusterID)
	if err != nil {
		return err
	}
	rs := &operatorv1alpha1.RootShard{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: pm.Namespace}}
	if err := r.opts.Apply(ctx, pm, rs, func() {
		rs.Labels = labels(pm.Name, components.RootShard, clusterID)
		rs.Spec = spec
	}); err != nil {
		return err
	}

	return r.opts.Teardown(ctx, pm, components.RootShard, &operatorv1alpha1.RootShardList{}, map[string]struct{}{name: {}})
}

// rootShardRef is the name of the single root shard admin CR that shards reference.
func (r *reconciler) rootShardRef(pm *pmdeployv1alpha1.PlatformMesh) (string, error) {
	engaged := r.opts.ClustersFor(pm.Name, components.RootShard)
	if len(engaged) != 1 {
		return "", fmt.Errorf("root shard not ready")
	}
	return names.RootShard(pm.Name, pm.Spec.Topology.RootShard.Name, engaged[0].ClusterID), nil
}

// frontProxyExternal returns the front-proxy's hostname and port.
func (r *reconciler) frontProxyExternal(pm *pmdeployv1alpha1.PlatformMesh) (string, uint32, error) {
	fp := pm.Spec.Topology.FrontProxy
	engaged := r.opts.ClustersFor(pm.Name, components.FrontProxy)
	if len(engaged) == 0 {
		return "", 0, fmt.Errorf("front proxy not ready")
	}
	addr, err := resolveAddress(fp.Exposure, celtemplate.Context{
		PlatformMesh: pm.Name,
		Component:    components.FrontProxy,
		Cluster:      engaged[0].ClusterID,
	}, service{
		adminName: names.FrontProxy(pm.Name, fp.Name, engaged[0].ClusterID),
		suffix:    frontProxyServiceSuffix,
		port:      shardServicePort,
	}, pm.Namespace, "front proxy")
	if err != nil {
		return "", 0, err
	}
	return addr.host, addr.port, nil
}

func (r *reconciler) buildRootShardSpec(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh, group pmdeployv1alpha1.RootShard, clusterID string) (operatorv1alpha1.RootShardSpec, error) {
	name := names.RootShard(pm.Name, group.Name, clusterID)
	celCtx := celtemplate.Context{
		PlatformMesh: pm.Name,
		Component:    components.RootShard,
		ShardGroup:   group.Name,
		Cluster:      clusterID,
	}

	var spec operatorv1alpha1.RootShardSpec
	tpl := &pmdeployv1alpha1.RootShardTemplate{}
	if err := r.resolveTemplate(ctx, pm, group.TemplateRef, tpl, func() any { return tpl.Spec }, &spec); err != nil {
		return spec, err
	}

	if err := resolveEtcd(&spec.Etcd, celCtx, "root shard "+name); err != nil {
		return spec, err
	}

	fpHost, fpPort, err := r.frontProxyExternal(pm)
	if err != nil {
		return spec, fmt.Errorf("root shard %q: %w", name, err)
	}
	spec.External.Hostname = fpHost
	spec.External.Port = fpPort

	addr, err := resolveAddress(group.Exposure, celCtx, service{
		adminName: name,
		suffix:    rootShardServiceSuffix,
		port:      shardServicePort,
	}, pm.Namespace, "root shard "+name)
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
			return spec, fmt.Errorf("root shard %q: %w", name, err)
		}
		spec.Cache.Reference = &corev1.LocalObjectReference{Name: ref}
	}
	return spec, nil
}
