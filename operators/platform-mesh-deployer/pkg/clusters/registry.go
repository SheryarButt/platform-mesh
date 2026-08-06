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

// Package clusters holds the deployer's cluster registry.
package clusters

import (
	"context"
	"sort"
	"strings"
	"sync"

	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// Cluster names are `<component>#<platformMesh>--<clusterName>`
// `<component>#` comes from the multi provider
// `<platformMesh>--` is convention just for the default kubconfig providers
// `<clusterName>` is the actual clusterName
const (
	componentSeparator = "#"
	platformMeshDelim  = "--"
)

// Registry is a coal-copy of the multicluster-runtime clusters.Registry.
// It keeps track of all engaged clusters and enqueues events for the PlatformMesh a cluster was engaged/disengaged for.
type Registry struct {
	lock     sync.RWMutex
	clusters map[multicluster.ClusterName]cluster.Cluster
	pending  map[string]struct{}

	wake   chan struct{}
	events chan event.GenericEvent
}

var _ mcmanager.Runnable = (*Registry)(nil)

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		clusters: make(map[multicluster.ClusterName]cluster.Cluster),
		pending:  make(map[string]struct{}),
		wake:     make(chan struct{}, 1),
		events:   make(chan event.GenericEvent, 16),
	}
}

// Start drains pending PlatformMesh changes into the event channel until the context is cancelled.
func (r *Registry) Start(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
		}

		r.lock.Lock()
		names := make([]string, 0, len(r.pending))
		for name := range r.pending {
			names = append(names, name)
		}
		r.pending = make(map[string]struct{})
		r.lock.Unlock()

		for _, name := range names {
			evt := event.GenericEvent{Object: &metav1.PartialObjectMetadata{
				ObjectMeta: metav1.ObjectMeta{Name: name},
			}}
			select {
			case <-ctx.Done():
				return nil
			case r.events <- evt:
			}
		}
	}
}

func (r *Registry) Engage(ctx context.Context, name multicluster.ClusterName, cl cluster.Cluster) error {
	platformMesh := platformMeshName(name)

	r.lock.Lock()
	r.clusters[name] = cl
	r.lock.Unlock()
	r.notify(platformMesh)

	go func() {
		<-ctx.Done()
		r.lock.Lock()
		if r.clusters[name] == cl {
			delete(r.clusters, name)
		}
		r.lock.Unlock()
		r.notify(platformMesh)
	}()
	return nil
}

// notify marks the PlatformMesh dirty and wakes the drain loop without blocking;
// the dirty set coalesces repeats but never drops a distinct PlatformMesh.
func (r *Registry) notify(platformMesh string) {
	r.lock.Lock()
	r.pending[platformMesh] = struct{}{}
	r.lock.Unlock()

	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// platformMeshName extracts the PlatformMesh name from a cluster name of the
// form "<component>#<platformMesh>--<clusterID>".
func platformMeshName(name multicluster.ClusterName) string {
	_, rest, ok := strings.Cut(name.String(), componentSeparator)
	if !ok {
		return ""
	}
	platformMesh, _, ok := strings.Cut(rest, platformMeshDelim)
	if !ok {
		return ""
	}
	return platformMesh
}

// Events returns the channel signalled on every engage/disengage.
func (r *Registry) Events() <-chan event.GenericEvent {
	return r.events
}

// Get returns the engaged cluster with the given full name.
func (r *Registry) Get(name multicluster.ClusterName) (cluster.Cluster, bool) {
	r.lock.RLock()
	defer r.lock.RUnlock()
	cl, ok := r.clusters[name]
	return cl, ok
}

// Cluster is an engaged cluster attributed to a component and PlatformMesh.
type Cluster struct {
	Name      multicluster.ClusterName // full "<component>#<platformMesh>--<clusterID>"
	ClusterID string
	Cluster   cluster.Cluster
}

// ClustersFor returns, sorted by name, the engaged clusters of the given component that belong to the named PlatformMesh.
func (r *Registry) ClustersFor(platformMesh, component string) []Cluster {
	prefix := component + componentSeparator + platformMesh + platformMeshDelim

	r.lock.RLock()
	defer r.lock.RUnlock()

	var out []Cluster
	for name, cl := range r.clusters {
		if id, ok := strings.CutPrefix(name.String(), prefix); ok {
			out = append(out, Cluster{Name: name, ClusterID: id, Cluster: cl})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AllClustersFor returns the engaged clusters of the named PlatformMesh across every component.
func (r *Registry) AllClustersFor(platformMesh string) []Cluster {
	infix := componentSeparator + platformMesh + platformMeshDelim

	r.lock.RLock()
	defer r.lock.RUnlock()

	seen := make(map[string]struct{})
	var out []Cluster
	for name, cl := range r.clusters {
		_, rest, ok := strings.Cut(name.String(), infix)
		if !ok {
			continue
		}
		if _, dup := seen[rest]; dup {
			continue
		}
		seen[rest] = struct{}{}
		out = append(out, Cluster{Name: name, ClusterID: rest, Cluster: cl})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ClusterID < out[j].ClusterID
	})
	return out
}

// ShardGroups returns the names of the shard groups the PlatformMesh has engaged clusters for.
func (r *Registry) ShardGroups(platformMesh string) []string {
	suffix := componentSeparator + platformMesh + platformMeshDelim

	r.lock.RLock()
	defer r.lock.RUnlock()

	seen := make(map[string]struct{})
	var out []string
	for name := range r.clusters {
		component, _, ok := strings.Cut(name.String(), suffix)
		if !ok || !strings.HasPrefix(component, components.ShardPrefix) {
			continue
		}
		group := strings.TrimPrefix(component, components.ShardPrefix)
		if _, dup := seen[group]; dup {
			continue
		}
		seen[group] = struct{}{}
		out = append(out, group)
	}
	sort.Strings(out)
	return out
}
