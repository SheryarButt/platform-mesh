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

	moduleMappings, err := r.moduleMappings(ctx, pm)
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
		// The template's own mappings and the modules' are reconciled together —
		// appending one to the other leaves the shorter path first, where it
		// shadows every longer one.
		spec.AdditionalPathMappings, err = mergePathMappings(spec.AdditionalPathMappings, moduleMappings)
		if err != nil {
			return err
		}
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

	addr, err := resolveAddress(frontProxy.Exposure, celCtx, service{
		adminName: name,
		suffix:    frontProxyServiceSuffix,
		port:      shardServicePort,
	}, pm.Namespace, "front proxy "+name)
	if err != nil {
		return spec, err
	}
	spec.External.Hostname = addr.host
	spec.External.Port = addr.port

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

// ownedMapping is a path mapping and who claimed it, so a collision can name
// both sides instead of reporting that a path is taken twice.
type ownedMapping struct {
	entry operatorv1alpha1.PathMappingEntry
	owner string
}

// templateOwner labels the mappings that came from the FrontProxyTemplate, for
// the same reason a module's owner is its `<module>/<component>`.
const templateOwner = "the FrontProxyTemplate"

// mergePathMappings combines what the FrontProxyTemplate declared with what the
// modules claim, into the list the FrontProxy is written with.
//
// Both sources are real: a module declares its own path with a component
// `mapping`, and a component installed some other way (Helm) can only get one
// from the template, because the deployer owns this list and force-applies it.
// They therefore have to be reconciled against each other rather than
// concatenated.
//
// Sorted longest path first, ACROSS both sources. kcp's matcher precedence is
// not verified here, and a short path is a prefix of every longer one —
// "/services/" shadows "/services/<module>/" — so an unsorted merge routes by
// whichever source happened to be appended first. Sorting each source alone is
// not enough, which is what appending template entries to sorted module entries
// used to do.
func mergePathMappings(template []operatorv1alpha1.PathMappingEntry, modules []ownedMapping) ([]operatorv1alpha1.PathMappingEntry, error) {
	all := make([]ownedMapping, 0, len(template)+len(modules))
	for _, e := range template {
		all = append(all, ownedMapping{entry: e, owner: templateOwner})
	}
	all = append(all, modules...)

	// Two owners claiming one path would both be written and the front proxy
	// would route by whichever won the sort, so refuse instead. Checked across
	// the merged list: a template path colliding with a module path is the same
	// failure as two modules colliding, and used to pass unnoticed.
	claimed := map[string]string{}
	out := make([]operatorv1alpha1.PathMappingEntry, 0, len(all))
	for _, m := range all {
		if previous, taken := claimed[m.entry.Path]; taken && previous != m.owner {
			return nil, fmt.Errorf("path %q is claimed by both %s and %s", m.entry.Path, previous, m.owner)
		}
		claimed[m.entry.Path] = m.owner
		out = append(out, m.entry)
	}

	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Path) != len(out[j].Path) {
			return len(out[i].Path) > len(out[j].Path)
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// moduleMappings collects the path mappings the PlatformMesh's modules have
// resolved. Only modules own mappings, but only the topology owns the
// FrontProxy object, so they meet here rather than both writing the same list.
//
// Returned unordered and unchecked: mergePathMappings settles precedence and
// collisions once, over these and the template's together.
func (r *reconciler) moduleMappings(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh) ([]ownedMapping, error) {
	modules, err := r.opts.ListModules(ctx, pm.Namespace)
	if err != nil {
		return nil, fmt.Errorf("listing modules: %w", err)
	}

	var out []ownedMapping
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
				out = append(out, ownedMapping{
					owner: mod.Name + "/" + component.Name,
					entry: operatorv1alpha1.PathMappingEntry{
						Path:            inst.Mapping.Path,
						Backend:         inst.Mapping.Backend,
						BackendServerCA: backendServerCAPath,
						ProxyClientCert: proxyClientCertPath,
						ProxyClientKey:  proxyClientKeyPath,
					},
				})
			}
		}
	}
	return out, nil
}
