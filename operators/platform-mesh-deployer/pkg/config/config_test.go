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
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.platform-mesh.io/platform-mesh-deployer/pkg/deployer"
)

func TestNewOperatorConfig(t *testing.T) {
	cfg := NewOperatorConfig()

	assert.Equal(t, "platform-mesh-system", cfg.Provider.Namespace)
	assert.Equal(t, "kubeconfig", cfg.Provider.KubeconfigSecretKey)
	assert.Equal(t, DefaultShardLabel, cfg.Provider.ShardGroups["default"])
	// The kcp-operator groups run in this process; leaving them off by default
	// would deploy a PlatformMesh that never compiles into anything.
	assert.Contains(t, cfg.EnabledControllers, deployer.ControllerKcpConfig)
	assert.Contains(t, cfg.EnabledControllers, deployer.ControllerKcpWorkload)
}

func TestAddFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want OperatorConfig
	}{
		{
			name: "defaults",
			args: nil,
			want: NewOperatorConfig(),
		},
		{
			name: "overrides",
			args: []string{
				"--provider-namespace=other-ns",
				"--provider-kubeconfig-secret-key=value",
				"--provider-shard-groups=eu=example.com/shard-eu",
			},
			want: func() OperatorConfig {
				c := NewOperatorConfig()
				c.Provider.Namespace = "other-ns"
				c.Provider.KubeconfigSecretKey = "value"
				c.Provider.ShardGroups = map[string]string{"eu": "example.com/shard-eu"}
				return c
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewOperatorConfig()
			fs := pflag.NewFlagSet(tt.name, pflag.ContinueOnError)
			cfg.AddFlags(fs)

			require.NoError(t, fs.Parse(tt.args))
			assert.Equal(t, tt.want, cfg)
		})
	}
}
