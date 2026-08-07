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
	"sort"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/celtemplate"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/names"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/operator/v1alpha1"
)

func (r *reconciler) reconcileFrontProxy(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh) error {
	frontProxy := pm.Spec.Topology.FrontProxy
	rootRef, err := r.rootShardRef(pm)
	if err != nil {
		return err
	}

	mappings, err := r.moduleMappings(ctx, pm)
	if err != nil {
		return err
	}

	engaged := r.opts.ClustersFor(pm.Name, components.FrontProxy)
	desired := map[string]struct{}{}
	for _, cl := range engaged {
		name := names.FrontProxy(pm.Name, frontProxy.Name, cl.ClusterID)
		spec, err := r.buildFrontProxySpec(ctx, pm, frontProxy, cl.ClusterID, rootRef)
		if err != nil {
			return err
		}
		spec.AdditionalPathMappings = append(spec.AdditionalPathMappings, mappings...)
		fp := &operatorv1alpha1.FrontProxy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: pm.Namespace}}
		if err := r.opts.Apply(ctx, pm, fp, func() {
			fp.Labels = labels(pm.Name, components.FrontProxy, cl.ClusterID)
			fp.Spec = spec
		}); err != nil {
			return err
		}
		desired[name] = struct{}{}
	}
	return r.opts.Teardown(ctx, pm, components.FrontProxy, &operatorv1alpha1.FrontProxyList{}, desired)
}

func (r *reconciler) buildFrontProxySpec(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh, frontProxy pmdeployv1alpha1.FrontProxy, clusterID, rootRef string) (operatorv1alpha1.FrontProxySpec, error) {
	name := names.FrontProxy(pm.Name, frontProxy.Name, clusterID)
	celCtx := celtemplate.Context{
		PlatformMesh: pm.Name,
		Component:    components.FrontProxy,
		Cluster:      clusterID,
	}

	var spec operatorv1alpha1.FrontProxySpec
	tpl := &pmdeployv1alpha1.FrontProxyTemplate{}
	if err := r.resolveTemplate(ctx, pm, frontProxy.TemplateRef, tpl, func() any { return tpl.Spec }, &spec); err != nil {
		return spec, err
	}

	// kcp's --authentication-drop-groups defaults to a security list that
	// includes system:masters, and kcp-operator passes the field verbatim, so
	// setting it replaces that list rather than adding to it. It would also
	// strip the system:kcp:admin the deployer authenticates with.
	if spec.Auth != nil && spec.Auth.DropGroups != nil {
		return spec, fmt.Errorf("front proxy %q template sets auth.dropGroups, which is not supported", name)
	}

	spec.RootShard.Reference = &corev1.LocalObjectReference{Name: rootRef}

	host, err := celtemplate.Eval(frontProxy.Exposure.HostnameTemplate, celCtx)
	if err != nil {
		return spec, fmt.Errorf("front proxy %q hostname: %w", name, err)
	}
	spec.External.Hostname = host
	spec.External.Port = uint32(frontProxy.Exposure.Port)

	return spec, nil
}

// The paths the front proxy mounts its own CAs and client certificate at.
// A module's backend is validated against the CA the front proxy already
// trusts, so a mapping needs no extra mounts.
const (
	backendServerCAPath = "/etc/kcp/tls/ca/tls.crt"
	proxyClientCertPath = "/etc/kcp-front-proxy/requestheader-client/tls.crt"
	proxyClientKeyPath  = "/etc/kcp-front-proxy/requestheader-client/tls.key"
)

// moduleMappings collects the path mappings the PlatformMesh's modules have
// resolved. Only modules own mappings, but only the topology owns the
// FrontProxy object, so they meet here rather than both writing the same list.
//
// Entries are sorted longest path first: the default "/services/" mapping is a
// prefix of every module path, and kcp's matcher precedence is not verified.
func (r *reconciler) moduleMappings(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh) ([]operatorv1alpha1.PathMappingEntry, error) {
	modules, err := r.opts.ListModules(ctx, pm.Namespace)
	if err != nil {
		return nil, fmt.Errorf("listing modules: %w", err)
	}

	var out []operatorv1alpha1.PathMappingEntry
	// Two modules claiming the same path would both be written and the
	// front proxy would route by whichever won the sort, so refuse instead.
	claimed := map[string]string{}
	for i := range modules {
		mod := &modules[i]
		if mod.Spec.PlatformMeshRef.Name != pm.Name {
			continue
		}
		for _, component := range mod.Status.Components {
			for _, inst := range component.Instances {
				if inst.Mapping == nil {
					continue
				}
				owner := mod.Name + "/" + component.Name
				if previous, taken := claimed[inst.Mapping.Path]; taken && previous != owner {
					return nil, fmt.Errorf("path %q is claimed by both %s and %s",
						inst.Mapping.Path, previous, owner)
				}
				claimed[inst.Mapping.Path] = owner

				out = append(out, operatorv1alpha1.PathMappingEntry{
					Path:            inst.Mapping.Path,
					Backend:         inst.Mapping.Backend,
					BackendServerCA: backendServerCAPath,
					ProxyClientCert: proxyClientCertPath,
					ProxyClientKey:  proxyClientKeyPath,
				})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Path) != len(out[j].Path) {
			return len(out[i].Path) > len(out[j].Path)
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}
