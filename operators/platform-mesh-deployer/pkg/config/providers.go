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

package config

import (
	"go.platform-mesh.io/golang-commons/logger"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/deployer"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocm"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	"sigs.k8s.io/multicluster-runtime/providers/kubeconfig"
	"sigs.k8s.io/multicluster-runtime/providers/multi"
)

// DeployerConfig builds a deployer.Config with the default per-component kubeconfig providers wired against mgr.
func (c OperatorConfig) DeployerConfig(mgr mcmanager.Manager, log *logger.Logger, resolver ocm.Resolver) deployer.Config {
	scheme := deployer.NewScheme()
	newProvider := func(component, label string) multicluster.Provider {
		return multi.AsRunnable(kubeconfig.New(kubeconfig.Options{
			Namespace:             c.Provider.Namespace,
			KubeconfigSecretLabel: label,
			KubeconfigSecretKey:   c.Provider.KubeconfigSecretKey,
			ControllerName:        c.Provider.ControllerNamePrefix + "kubeconfig-" + component,
			ClusterOptions:        []cluster.Option{func(o *cluster.Options) { o.Scheme = scheme }},
		}), mgr)
	}

	shardProviders := make(map[string]multicluster.Provider, len(c.Provider.ShardGroups))
	for group, label := range c.Provider.ShardGroups {
		shardProviders[group] = newProvider(components.Shard(group), label)
	}

	return deployer.Config{
		Log:                 log,
		Resolver:            resolver,
		EnabledControllers:  c.EnabledControllers,
		RootShardProvider:   newProvider(components.RootShard, c.Provider.RootShardLabel),
		ShardProviders:      shardProviders,
		FrontProxyProvider:  newProvider(components.FrontProxy, c.Provider.FrontProxyLabel),
		CacheServerProvider: newProvider(components.CacheServer, c.Provider.CacheServerLabel),
	}
}
