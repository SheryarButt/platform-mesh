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

package config_test

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.platform-mesh.io/tenancy-operator/internal/config"
)

func TestDefaultsAreValid(t *testing.T) {
	cfg := config.NewOperatorConfig()
	require.NoError(t, cfg.Validate())

	l, err := cfg.Layout()
	require.NoError(t, err)
	assert.Equal(t, "root:tenants", l.TenantFleetRoot)
	assert.Equal(t, "root:system:directory", l.Directory)
	assert.Equal(t, "root:system:controllers", l.Exports)

	// The four exports are split by audience and by capability; each is read
	// through its own APIExportEndpointSlice.
	assert.Equal(t, "tenancy-platform", cfg.Kcp.PlatformEndpointSlice)
	assert.Equal(t, "tenancy", cfg.Kcp.TenancyEndpointSlice)
	assert.Equal(t, "tenancy-provisioner", cfg.Kcp.ProvisionerEndpointSlice)
	assert.Equal(t, "tenancy-access", cfg.Kcp.AccessEndpointSlice)

	assert.Equal(t, int32(10), cfg.Tenancy.TenantQuota)
	assert.Equal(t, int32(50), cfg.Tenancy.ProjectQuota)
}

// A bad root or an unmirrorable username claim must fail at boot, not on the
// first reconcile — a binding pointing at a non-existent export surfaces far from
// its cause.
func TestValidate(t *testing.T) {
	for name, mutate := range map[string]func(*config.OperatorConfig){
		"malformed root":        func(c *config.OperatorConfig) { c.Paths.Root = "root::tenants" },
		"unmirrorable claim":    func(c *config.OperatorConfig) { c.OIDC.UsernameClaim = "preferred_username" },
		"no platform slice":     func(c *config.OperatorConfig) { c.Kcp.PlatformEndpointSlice = "" },
		"negative tenant quota": func(c *config.OperatorConfig) { c.Tenancy.TenantQuota = -1 },
		"bad vw mount prefix":   func(c *config.OperatorConfig) { c.Paths.VirtualWorkspacePrefix = "services/tenancy" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := config.NewOperatorConfig()
			mutate(&cfg)
			assert.Error(t, cfg.Validate())
		})
	}
}

// A sub-tree install is a flag, not a code change.
func TestSubTreeInstallIsOneFlag(t *testing.T) {
	cfg := config.NewOperatorConfig()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	cfg.AddFlags(fs)
	require.NoError(t, fs.Parse([]string{"--paths-root=root:acme:platform"}))
	require.NoError(t, cfg.Validate())

	l, err := cfg.Layout()
	require.NoError(t, err)
	assert.Equal(t, "root:acme:platform:tenants", l.TenantFleetRoot)
	assert.Equal(t, "root:acme:platform:system:directory", l.Directory)
}

func TestIdentityResolverMirrorsFlags(t *testing.T) {
	cfg := config.NewOperatorConfig()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	cfg.AddFlags(fs)
	require.NoError(t, fs.Parse([]string{"--oidc-username-claim=sub", "--oidc-username-prefix=acme:"}))

	r, err := cfg.IdentityResolver()
	require.NoError(t, err)
	assert.False(t, r.Mutable())
}
