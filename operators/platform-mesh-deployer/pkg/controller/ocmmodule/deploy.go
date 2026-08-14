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

package ocmmodule

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/celtemplate"
	pmocmmodule "go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// deploy renders every instance and applies it to its cluster, then prunes the
// objects of instances that no longer exist.
func (r *reconciler) deploy(ctx context.Context) error {
	mod := r.mod

	// Objects to keep per cluster, so a component that lost a cluster or a
	// cluster that lost a component is cleaned up.
	keep := map[string]map[sync.ObjectKey]struct{}{}
	kinds := map[string]map[schema.GroupVersionKind]struct{}{}
	status := map[string]*pmdeployv1alpha1.OCMModuleComponentStatus{}

	for _, inst := range r.instances {
		// The kubeconfigs must exist before the payload references them,
		// otherwise its pods block mounting a missing secret.
		if err := r.ensureKubeconfigs(ctx, inst); err != nil {
			return err
		}

		celCtx, err := r.resolved.Context(inst)
		if err != nil {
			return err
		}
		celCtx.Endpoints = r.endpoints

		// A mapped component is fronted by the front proxy, which needs a
		// certificate it trusts before the topology can route to it, and which
		// forwards the caller's identity signed by the requestheader CA.
		var mapping *pmdeployv1alpha1.ResolvedMapping
		if inst.Component.Mapping != nil {
			if err := r.ensureServingCert(ctx, inst, celCtx); err != nil {
				return err
			}
			if err := r.ensureRequestHeaderCA(ctx, inst); err != nil {
				return err
			}
			mapping, err = resolveMapping(inst, celCtx)
			if err != nil {
				return err
			}
		}

		objs, err := r.resolved.Render(ctx, inst, r.endpoints)
		if err != nil {
			return err
		}

		cl := inst.Cluster.Cluster.GetClient()
		if err := sync.EnsureNamespace(ctx, cl, inst.Component.Namespace); err != nil {
			return err
		}
		for _, obj := range objs {
			sync.Strip(obj)
			if err := sync.Apply(ctx, cl, obj); err != nil {
				return err
			}
			id := inst.Cluster.ClusterID
			if keep[id] == nil {
				keep[id] = map[sync.ObjectKey]struct{}{}
				kinds[id] = map[schema.GroupVersionKind]struct{}{}
			}
			keep[id][sync.KeyOf(obj)] = struct{}{}
			kinds[id][obj.GroupVersionKind()] = struct{}{}
		}

		componentStatus(status, inst, pmocmmodule.ConfigMapName(mod.Name, inst.Component.Name), mapping)
	}

	if err := r.prune(ctx, keep, kinds); err != nil {
		return err
	}

	mod.Status.Components = sortedStatus(status)
	mod.Status.AppliedKinds = appliedKindsStatus(kinds)
	return nil
}

// appliedKindsStatus records every kind applied on any cluster, so teardown
// can find the objects again once the payload is gone.
func appliedKindsStatus(perCluster map[string]map[schema.GroupVersionKind]struct{}) []pmdeployv1alpha1.GroupVersionKind {
	all := map[schema.GroupVersionKind]struct{}{}
	for _, kinds := range perCluster {
		for gvk := range kinds {
			all[gvk] = struct{}{}
		}
	}
	out := make([]pmdeployv1alpha1.GroupVersionKind, 0, len(all))
	for _, gvk := range kindsOf(all) {
		out = append(out, pmdeployv1alpha1.GroupVersionKind{
			Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind,
		})
	}
	return out
}

// prune deletes objects the module owns on a cluster that this reconcile did
// not apply.
func (r *reconciler) prune(ctx context.Context, keep map[string]map[sync.ObjectKey]struct{}, kinds map[string]map[schema.GroupVersionKind]struct{}) error {
	mod := r.mod
	for _, c := range r.opts.AllClustersFor(mod.Spec.PlatformMeshRef.Name) {
		gvks := kindsOf(kinds[c.ClusterID])
		if len(gvks) == 0 {
			// Nothing was applied here this round; still prune what a
			// previous round left behind, using the kinds the module
			// declares it can produce.
			gvks = previousKinds(mod)
		}
		if len(gvks) == 0 {
			continue
		}
		if err := sync.Prune(ctx, c.Cluster.GetClient(), gvks,
			pmocmmodule.OCMModuleSelector(mod, c.ClusterID), keep[c.ClusterID]); err != nil {
			return fmt.Errorf("pruning on cluster %q: %w", c.ClusterID, err)
		}
	}
	return nil
}

// previousKinds are the kinds pruned on a cluster the module no longer places
// anything on. The generated ConfigMap is always applied, so it is enough to
// detect and clean up a stale instance.
func previousKinds(*pmdeployv1alpha1.OCMModule) []schema.GroupVersionKind {
	return []schema.GroupVersionKind{{Version: "v1", Kind: "ConfigMap"}}
}

func kindsOf(set map[schema.GroupVersionKind]struct{}) []schema.GroupVersionKind {
	out := make([]schema.GroupVersionKind, 0, len(set))
	for gvk := range set {
		out = append(out, gvk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// resolveMapping interpolates a component's mapping into the concrete path and
// backend URL the front proxy routes with.
func resolveMapping(inst pmocmmodule.Instance, celCtx celtemplate.Context) (*pmdeployv1alpha1.ResolvedMapping, error) {
	m := inst.Component.Mapping

	authority, err := mappingAuthority(inst, celCtx)
	if err != nil {
		return nil, err
	}

	path, err := celtemplate.Interpolate(m.Path, celCtx)
	if err != nil {
		return nil, fmt.Errorf("component %q: mapping path: %w", inst.Component.Name, err)
	}
	uri, ok := path.(string)
	if !ok {
		return nil, fmt.Errorf("component %q: mapping path evaluated to %T, want string", inst.Component.Name, path)
	}

	// JoinHostPort rather than "%s:%d": a `host` mapping may name an IPv6
	// literal, which has to be bracketed or the port is read as part of the
	// address.
	return &pmdeployv1alpha1.ResolvedMapping{
		Path:    uri,
		Backend: "https://" + net.JoinHostPort(authority, strconv.Itoa(int(m.Port))),
	}, nil
}

// mappingAuthority is the host the front proxy dials for a mapped component:
// the in-cluster Service name, or the DNS name of a backend it cannot reach that
// way.
//
// It is also what the backend's certificate has to be valid for, which is why
// ensureServingCert derives its dnsNames from the same function rather than
// rebuilding the name — the proxy verifies exactly the name it dialled, and two
// derivations of it would eventually disagree.
func mappingAuthority(inst pmocmmodule.Instance, celCtx celtemplate.Context) (string, error) {
	m := inst.Component.Mapping

	// "Exactly one" is an admission rule on the type, and this checks it again
	// rather than trusting it. A CEL rule is evaluated on WRITE: an object stored
	// before the rule existed is never revalidated, and a binary can legitimately
	// run against a CRD that predates it during a rollout.
	//
	// The cost of assuming is not a crash, which is why it is worth spelling out:
	// with both set, one of them silently wins and the front proxy routes to a
	// backend the module author did not name. With neither, the backend is
	// "https://.svc:6443" — accepted, never dialable.
	switch {
	case m.Service != "" && m.Host != "":
		return "", fmt.Errorf(
			"component %q: mapping sets both service (%s) and host (%s); exactly one names the backend",
			inst.Component.Name, m.Service, m.Host)
	case m.Service == "" && m.Host == "":
		return "", fmt.Errorf(
			"component %q: mapping sets neither service nor host; one of them names the backend",
			inst.Component.Name)
	}

	field, expr := "service", m.Service
	if m.Host != "" {
		field, expr = "host", m.Host
	}

	value, err := celtemplate.Interpolate(expr, celCtx)
	if err != nil {
		return "", fmt.Errorf("component %q: mapping %s: %w", inst.Component.Name, field, err)
	}
	name, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("component %q: mapping %s evaluated to %T, want string", inst.Component.Name, field, value)
	}
	if name == "" {
		return "", fmt.Errorf("component %q: mapping %s evaluated to an empty string", inst.Component.Name, field)
	}

	if m.Host != "" {
		return name, nil
	}
	return fmt.Sprintf("%s.%s.svc", name, inst.Component.Namespace), nil
}

// componentStatus records one applied instance.
func componentStatus(status map[string]*pmdeployv1alpha1.OCMModuleComponentStatus, inst pmocmmodule.Instance, configMap string, mapping *pmdeployv1alpha1.ResolvedMapping) {
	cs, ok := status[inst.Component.Name]
	if !ok {
		cs = &pmdeployv1alpha1.OCMModuleComponentStatus{
			Name:      inst.Component.Name,
			Placement: inst.Component.Placement,
		}
		status[inst.Component.Name] = cs
	}
	cs.Instances = append(cs.Instances, pmdeployv1alpha1.OCMModuleInstanceStatus{
		Cluster:   inst.Cluster.ClusterID,
		Namespace: inst.Component.Namespace,
		ConfigMap: configMap,
		Mapping:   mapping,
		Ready:     true,
	})
}

func sortedStatus(status map[string]*pmdeployv1alpha1.OCMModuleComponentStatus) []pmdeployv1alpha1.OCMModuleComponentStatus {
	out := make([]pmdeployv1alpha1.OCMModuleComponentStatus, 0, len(status))
	for _, cs := range status {
		sort.Slice(cs.Instances, func(i, j int) bool { return cs.Instances[i].Cluster < cs.Instances[j].Cluster })
		out = append(out, *cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
