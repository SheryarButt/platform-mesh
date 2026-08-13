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

	"k8s.io/apimachinery/pkg/api/meta"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// routeParams is the stack-agnostic input a renderer turns into its own route object.
type routeParams struct {
	pmName    string
	namespace string
	name      string
	host      string // SNI hostname
	service   string // backend Service name
	port      int32  // backend Service port
	component string
	clusterID string
}

// stackRenderer reconciles the route objects of one ingress stack. It is built
// once per stack with its typed configuration already resolved.
type stackRenderer interface {
	ensure(ctx context.Context, workload ctrlruntimeclient.Client, p routeParams) error
	// teardown removes this stack type's routes of the PlatformMesh not in desired.
	teardown(ctx context.Context, workload ctrlruntimeclient.Client, pmName, namespace string, desired map[string]struct{}) error
}

// newRenderer builds the renderer for one ingress stack, or nil for an
// unsupported type. Each supported type reads its own typed field.
func newRenderer(stack pmdeployv1alpha1.IngressStack) (stackRenderer, error) {
	switch stack.Type {
	case pmdeployv1alpha1.IngressTypeGatewayAPI:
		return newGatewayAPIRenderer(stack.GatewayAPI)
	default:
		return nil, nil
	}
}

// endpoint is one exposed kcp component reachable through the ingress stacks.
type endpoint struct {
	component  string
	shardGroup string
	// adminName names the admin CR on a cluster, under that kind's name budget.
	adminName func(platformMesh, clusterID string) string
	svcSuffix string
	// backendPort is the Service port; 0 means use the exposure port (front proxy).
	backendPort int32
	exposure    pmdeployv1alpha1.Exposure
}

// reconcileExposure publishes the topology through the configured ingress
// stacks, writing routes on the workload clusters.
func (r *reconciler) reconcileExposure(ctx context.Context) (bool, error) {
	pm := r.pm

	byName, byType, err := renderers(pm)
	if err != nil {
		return r.exposureFailed(err)
	}
	if len(byName) == 0 {
		meta.SetStatusCondition(&pm.Status.Conditions, exposureReady(pm.Generation, "no ingress stack is configured"))
		return true, nil
	}

	desired := map[string]struct{}{}
	clients := map[string]ctrlruntimeclient.Client{}

	for _, ep := range endpoints(pm) {
		for _, cl := range r.opts.ClustersFor(pm.Name, ep.component) {
			celCtx := celtemplate.Context{
				PlatformMesh: pm.Name,
				Component:    ep.component,
				ShardGroup:   ep.shardGroup,
				Cluster:      cl.ClusterID,
			}
			host, err := celtemplate.Eval(ep.exposure.HostnameTemplate, celCtx)
			if err != nil {
				return r.exposureFailed(fmt.Errorf("%s hostname: %w", ep.component, err))
			}
			adminName := ep.adminName(pm.Name, cl.ClusterID)
			port := ep.backendPort
			if port == 0 {
				port = ep.exposure.Port
			}

			refs := ep.exposure.IngressRefs
			if len(refs) == 0 {
				refs = keys(byName)
			}
			workload := cl.Cluster.GetClient()
			clients[cl.Name.String()] = workload
			for _, ref := range refs {
				rend, ok := byName[ref]
				if !ok {
					continue
				}
				name := adminName + "-" + ref
				if err := rend.ensure(ctx, workload, routeParams{
					pmName:    pm.Name,
					namespace: pm.Namespace,
					name:      name,
					host:      host,
					service:   adminName + ep.svcSuffix,
					port:      port,
					component: ep.component,
					clusterID: cl.ClusterID,
				}); err != nil {
					return r.exposureFailed(err)
				}
				desired[name] = struct{}{}
			}
		}
	}

	for _, workload := range clients {
		for _, rend := range byType {
			if err := rend.teardown(ctx, workload, pm.Name, pm.Namespace, desired); err != nil {
				return r.exposureFailed(err)
			}
		}
	}
	meta.SetStatusCondition(&pm.Status.Conditions, exposureReady(pm.Generation, "ingress routes applied"))
	return true, nil
}

func (r *reconciler) exposureFailed(err error) (bool, error) {
	meta.SetStatusCondition(&r.pm.Status.Conditions, exposureFailed(r.pm.Generation, err))
	return false, err
}

// endpoints lists the exposed components of a PlatformMesh.
func endpoints(pm *pmdeployv1alpha1.PlatformMesh) []endpoint {
	t := pm.Spec.Topology
	var eps []endpoint
	if t.RootShard.Exposure != nil {
		eps = append(eps, endpoint{
			component: components.RootShard,
			adminName: func(pm, clusterID string) string {
				return names.RootShard(pm, t.RootShard.Name, clusterID)
			},
			svcSuffix:   rootShardServiceSuffix,
			backendPort: shardServicePort,
			exposure:    *t.RootShard.Exposure,
		})
	}
	if t.FrontProxy.Exposure != nil {
		eps = append(eps, endpoint{
			component: components.FrontProxy,
			adminName: func(pm, clusterID string) string {
				return names.FrontProxy(pm, t.FrontProxy.Name, clusterID)
			},
			svcSuffix:   frontProxyServiceSuffix,
			backendPort: 0, // front proxy service port == external port
			exposure:    *t.FrontProxy.Exposure,
		})
	}
	for i := range t.ShardGroups {
		g := t.ShardGroups[i]
		if g.Exposure == nil {
			continue
		}
		eps = append(eps, endpoint{
			component:  components.Shard(g.Name),
			shardGroup: g.Name,
			adminName: func(pm, clusterID string) string {
				return names.Shard(pm, g.Name, clusterID)
			},
			svcSuffix:   shardServiceSuffix,
			backendPort: shardServicePort,
			exposure:    *g.Exposure,
		})
	}
	return eps
}

// renderers builds a renderer per ingress stack, keyed by stack name for lookup
// and by type for teardown.
func renderers(pm *pmdeployv1alpha1.PlatformMesh) (map[string]stackRenderer, map[pmdeployv1alpha1.IngressType]stackRenderer, error) {
	byName := map[string]stackRenderer{}
	byType := map[pmdeployv1alpha1.IngressType]stackRenderer{}
	for i := range pm.Spec.Ingress {
		stack := pm.Spec.Ingress[i]
		rend, err := newRenderer(stack)
		if err != nil {
			return nil, nil, fmt.Errorf("ingress stack %q: %w", stack.Name, err)
		}
		if rend == nil {
			continue
		}
		byName[stack.Name] = rend
		byType[stack.Type] = rend
	}
	return byName, byType, nil
}

func keys(m map[string]stackRenderer) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
