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

// Package workspace builds per-workspace clients for the write path. Writes go directly to
// the consumer workspace as the operator's own kcp identity, not through the virtual
// workspace. That identity is cluster-admin, so nothing is provisioned per Account.
package workspace

import (
	"context"
	"fmt"
	"strings"
	"sync"

	kroclient "github.com/kubernetes-sigs/kro/pkg/client"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/record"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Clients bundles everything the engine needs to act in one workspace as the
// operator's own kcp identity, retargeted at that workspace.
type Clients struct {
	Config   *rest.Config
	Client   ctrlruntimeclient.Client
	Dynamic  dynamic.Interface
	Metadata metadata.Interface
	Mapper   meta.RESTMapper

	// KroSet is the client bundle kro's instance controller takes. It is scoped to this
	// workspace, which is what lets a single-cluster controller run per workspace.
	KroSet kroclient.SetInterface

	// Recorder writes Events into this workspace, so an instance's own namespace carries
	// the record of what the operator did to it.
	Recorder record.EventRecorder
}

// Provider builds and caches per-workspace Clients.
type Provider struct {
	Base   *rest.Config // operator's kcp config; host is the shard base (no /clusters path)
	Scheme *runtime.Scheme

	mu    sync.Mutex
	cache map[string]*Clients
}

func NewProvider(base *rest.Config, scheme *runtime.Scheme) *Provider {
	return &Provider{Base: base, Scheme: scheme, cache: map[string]*Clients{}}
}

// For returns Clients for a logical cluster: the operator's own identity retargeted there.
func (p *Provider) For(_ context.Context, clusterName string) (*Clients, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.cache[clusterName]; ok {
		return c, nil
	}

	cfg := rest.CopyConfig(p.Base)
	cfg.Host = clusterHost(p.Base.Host, clusterName)
	c, err := p.buildClients(clusterName, cfg)
	if err != nil {
		return nil, err
	}
	p.cache[clusterName] = c
	return c, nil
}

// buildClients constructs the client bundle from a ready-to-use rest.Config.
func (p *Provider) buildClients(clusterName string, cfg *rest.Config) (*Clients, error) {
	cl, err := ctrlruntimeclient.New(cfg, ctrlruntimeclient.Options{Scheme: p.Scheme})
	if err != nil {
		return nil, fmt.Errorf("controller-runtime client for %s: %w", clusterName, err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client for %s: %w", clusterName, err)
	}
	metaCl, err := metadata.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metadata client for %s: %w", clusterName, err)
	}
	mapper, err := restMapper(cfg)
	if err != nil {
		return nil, fmt.Errorf("rest mapper for %s: %w", clusterName, err)
	}
	kroSet, err := kroclient.NewSet(kroclient.Config{RestConfig: cfg})
	if err != nil {
		return nil, fmt.Errorf("kro client set for %s: %w", clusterName, err)
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client for %s: %w", clusterName, err)
	}
	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kube.CoreV1().Events("")})
	recorder := broadcaster.NewRecorder(p.Scheme, corev1.EventSource{Component: "kro-composition-operator"})

	return &Clients{
		Config:   cfg,
		Client:   cl,
		Dynamic:  dyn,
		Metadata: metaCl,
		Mapper:   mapper,
		KroSet:   kroSet,
		Recorder: recorder,
	}, nil
}

func clusterHost(baseHost, clusterName string) string {
	h := baseHost
	if i := strings.Index(h, "/clusters/"); i >= 0 {
		h = h[:i]
	}
	return strings.TrimSuffix(h, "/") + "/clusters/" + clusterName
}

func restMapper(cfg *rest.Config) (meta.RESTMapper, error) {
	dc, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	gr, err := restmapper.GetAPIGroupResources(dc.Discovery())
	if err != nil {
		return nil, err
	}
	return restmapper.NewDiscoveryRESTMapper(gr), nil
}
