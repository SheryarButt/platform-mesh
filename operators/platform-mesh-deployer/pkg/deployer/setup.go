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

// Package deployer wires the deployer controllers into a multicluster manager.
package deployer

import (
	"fmt"
	"slices"

	"go.platform-mesh.io/golang-commons/logger"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/controller/ocmmodule"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/controller/ocmmodulesetup"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/controller/platformmesh"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/kcp"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocm"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/templates"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/transfer"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	"sigs.k8s.io/multicluster-runtime/providers/multi"

	operatorclient "github.com/kcp-dev/kcp-operator/pkg/client"
	kcpcontroller "github.com/kcp-dev/kcp-operator/pkg/controller"
)

// Controllers, enabled independently like kcp-operator config/workload.
const (
	// ControllerConfig creates the admin CRs on the config plane.
	ControllerConfig = "config"
	// ControllerCopy copies compiled CRs to their workload cluster.
	ControllerCopy = "copy"
	// ControllerModule deploys OCMModules onto the engaged clusters.
	ControllerModule = "module"
	// ControllerProvisioner performs the kcp side of a PlatformMesh and its
	// modules. It is the only controller that writes inside kcp.
	ControllerProvisioner = "provisioner"
	// ControllerKcpConfig runs kcp-operator's config group, compiling the admin
	// CRs on the config plane into the Compiled* CRs.
	ControllerKcpConfig = "kcp-config"
	// ControllerKcpWorkload runs kcp-operator's workload group, turning the
	// Compiled* CRs on the engaged clusters into Deployments and Services.
	ControllerKcpWorkload = "kcp-workload"
)

// Config contains the necessary configuration to setup the deployer controllers with a manager.
type Config struct {
	Log      *logger.Logger
	Resolver ocm.Resolver

	// EnabledControllers selects which controllers run (ControllerConfig, ControllerCopy, ControllerModule).
	EnabledControllers []string

	// KcpDial overrides how the front proxy is reached. The e2e runs the
	// deployer outside the cluster, where the published hostnames do not
	// resolve; a normal deployment leaves this nil.
	KcpDial kcp.DialFunc

	// KcpAddress resolves the kcp components kcp-operator's controllers reach.
	// It is separate from KcpDial because those controllers address the shards
	// as well as the front proxy. A normal deployment leaves this nil.
	KcpAddress operatorclient.Addresser

	RootShardProvider   multicluster.Provider
	ShardProviders      map[string]multicluster.Provider // keyed by ShardGroup.Name
	FrontProxyProvider  multicluster.Provider
	CacheServerProvider multicluster.Provider
}

// kcpAddress defaults to addressing the components by the URLs the deployer
// published for them, dialled directly.
func (c Config) kcpAddress() operatorclient.Addresser {
	if c.KcpAddress != nil {
		return c.KcpAddress
	}
	return kcp.Addresser{}
}

func (c Config) controllerEnabled(name string) bool {
	return slices.Contains(c.EnabledControllers, name)
}

// Setup registers the deployer controllers on the manager.
func Setup(mgr mcmanager.Manager, cfg Config) error {
	registry := clusters.NewRegistry()
	if err := mgr.Add(registry); err != nil {
		return fmt.Errorf("adding cluster registry: %w", err)
	}

	local := mgr.GetLocalManager()
	var access *kcp.Access
	if cfg.controllerEnabled(ControllerProvisioner) {
		access = kcp.New(local.GetClient(), registry, local.GetScheme(), cfg.KcpDial)
	}

	if cfg.controllerEnabled(ControllerConfig) {
		c, err := platformmesh.NewControllerFor(mgr, registry, access)
		if err != nil {
			return fmt.Errorf("building config controller: %w", err)
		}
		if err := c.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("setting up config controller: %w", err)
		}
		for _, r := range templates.NewReconcilers(mgr) {
			if err := r.SetupWithManager(mgr); err != nil {
				return fmt.Errorf("setting up template controller: %w", err)
			}
		}
	}
	if cfg.controllerEnabled(ControllerCopy) {
		if err := transfer.New(registry).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("setting up copy controller: %w", err)
		}
	}
	if cfg.controllerEnabled(ControllerModule) {
		c, err := ocmmodule.NewControllerFor(mgr, registry, cfg.Resolver)
		if err != nil {
			return fmt.Errorf("building module controller: %w", err)
		}
		if err := c.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("setting up module controller: %w", err)
		}
	}
	if cfg.controllerEnabled(ControllerProvisioner) {
		c, err := ocmmodulesetup.NewControllerFor(mgr, access, cfg.Resolver)
		if err != nil {
			return fmt.Errorf("building provisioner controller: %w", err)
		}
		if err := c.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("setting up provisioner controller: %w", err)
		}
	}
	if cfg.controllerEnabled(ControllerKcpConfig) {
		// The config group belongs to the config plane, which is this manager's
		// local cluster. mcbuilder derives both engage defaults from the
		// manager's provider, and this manager has one, so neither default is
		// what this group needs.
		if err := kcpcontroller.AddConfigControllers(mgr, kcpcontroller.Options{
			Engage: []mcbuilder.EngageOptions{
				mcbuilder.WithEngageWithLocalCluster(true),
				mcbuilder.WithEngageWithProviderClusters(false),
			},
			Address: cfg.kcpAddress(),
		}); err != nil {
			return fmt.Errorf("setting up kcp config controllers: %w", err)
		}
	}
	if cfg.controllerEnabled(ControllerKcpWorkload) {
		// No engage options: the workload group belongs to the engaged clusters,
		// which is already what mcbuilder defaults to when a provider is set.
		// No addresser either: these controllers render workloads and never talk
		// to a running kcp.
		if err := kcpcontroller.AddWorkloadControllers(mgr, kcpcontroller.Options{}); err != nil {
			return fmt.Errorf("setting up kcp workload controllers: %w", err)
		}
	}
	return nil
}

// AddProviders adds the configured [multicluster.Provider] to the [multi.Provider].
func AddProviders(mp *multi.Provider, mgr mcmanager.Manager, cfg Config) error {
	entries := map[string]multicluster.Provider{
		components.RootShard:   cfg.RootShardProvider,
		components.FrontProxy:  cfg.FrontProxyProvider,
		components.CacheServer: cfg.CacheServerProvider,
	}
	for group, provider := range cfg.ShardProviders {
		entries[components.Shard(group)] = provider
	}

	for name, provider := range entries {
		if provider == nil {
			continue
		}
		if err := mp.AddProvider(name, provider); err != nil {
			return fmt.Errorf("adding %s provider: %w", name, err)
		}
	}
	return nil
}
