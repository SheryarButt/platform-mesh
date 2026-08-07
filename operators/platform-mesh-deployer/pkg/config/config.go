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
	"github.com/spf13/pflag"

	"go.platform-mesh.io/platform-mesh-deployer/pkg/deployer"
)

// Default per-component labels selecting kubeconfig secrets.
const (
	DefaultRootShardLabel   = "deploy.platform-mesh.io/rootshard"
	DefaultShardLabel       = "deploy.platform-mesh.io/shards-default"
	DefaultFrontProxyLabel  = "deploy.platform-mesh.io/frontproxy"
	DefaultCacheServerLabel = "deploy.platform-mesh.io/cacheserver"
)

type ProviderConfig struct {
	Namespace           string
	KubeconfigSecretKey string

	// ControllerNamePrefix is added to each kubeconfig providers'
	// controller to disambiguate when running multiple deployer
	// operators in one process.
	ControllerNamePrefix string

	RootShardLabel   string
	ShardGroups      map[string]string // shard group name -> secret label
	FrontProxyLabel  string
	CacheServerLabel string
}

type OperatorConfig struct {
	Provider ProviderConfig

	// EnabledControllers selects which controllers run.
	EnabledControllers []string
}

func NewOperatorConfig() OperatorConfig {
	return OperatorConfig{
		EnabledControllers: []string{
			deployer.ControllerConfig,
			deployer.ControllerCopy,
			deployer.ControllerModule,
			deployer.ControllerProvisioner,
			deployer.ControllerKcpConfig,
			deployer.ControllerKcpWorkload,
		},
		Provider: ProviderConfig{
			Namespace:           "platform-mesh-system",
			KubeconfigSecretKey: "kubeconfig",
			RootShardLabel:      DefaultRootShardLabel,
			ShardGroups:         map[string]string{"default": DefaultShardLabel},
			FrontProxyLabel:     DefaultFrontProxyLabel,
			CacheServerLabel:    DefaultCacheServerLabel,
		},
	}
}

func (c *OperatorConfig) AddFlags(fs *pflag.FlagSet) {
	fs.StringSliceVar(&c.EnabledControllers, "enabled-controllers", c.EnabledControllers, "Controllers to run (config, copy, module, provisioner, kcp-config, kcp-workload)")
	fs.StringVar(&c.Provider.Namespace, "provider-namespace", c.Provider.Namespace, "Namespace to watch for kubeconfig secrets")
	fs.StringVar(&c.Provider.KubeconfigSecretKey, "provider-kubeconfig-secret-key", c.Provider.KubeconfigSecretKey, "Key within the secret containing the kubeconfig")
	fs.StringVar(&c.Provider.RootShardLabel, "provider-rootshard-label", c.Provider.RootShardLabel, "Label selecting root shard kubeconfig secrets")
	fs.StringToStringVar(&c.Provider.ShardGroups, "provider-shard-groups", c.Provider.ShardGroups, "Shard group name to secret label mapping")
	fs.StringVar(&c.Provider.FrontProxyLabel, "provider-frontproxy-label", c.Provider.FrontProxyLabel, "Label selecting front proxy kubeconfig secrets")
	fs.StringVar(&c.Provider.CacheServerLabel, "provider-cacheserver-label", c.Provider.CacheServerLabel, "Label selecting cache server kubeconfig secrets")
}
