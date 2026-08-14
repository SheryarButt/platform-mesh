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

package paths_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.platform-mesh.io/tenancy-operator/pkg/paths"
)

func TestNewDefaultLayout(t *testing.T) {
	l, err := paths.New(paths.Options{})
	require.NoError(t, err)

	assert.Equal(t, "root", l.Root)
	assert.Equal(t, "root:tenants", l.TenantFleetRoot)
	assert.Equal(t, "root:system:directory", l.Directory)
	assert.Equal(t, "root:system:controllers", l.Exports)
	assert.Equal(t, "root:system:providers", l.Providers)
	assert.Equal(t, "/services/tenancy", l.VirtualWorkspacePrefix)
}

// The platform must not claim root:. Installing under a sub-tree has to be a
// configuration change, so every derived path must move with the root.
func TestNewSubTreeInstall(t *testing.T) {
	l, err := paths.New(paths.Options{Root: "root:acme:platform"})
	require.NoError(t, err)

	assert.Equal(t, "root:acme:platform:tenants", l.TenantFleetRoot)
	assert.Equal(t, "root:acme:platform:system:directory", l.Directory)
	assert.Equal(t, "root:acme:platform:system:controllers", l.Exports)
}

// Two installs on one kcp need disjoint subtrees AND a disjoint VW mount point,
// or each singleton answers for both.
func TestNewOverrides(t *testing.T) {
	l, err := paths.New(paths.Options{
		Root:                   "root:dev",
		TenantFleetRoot:        "root:dev:fleet",
		Directory:              "root:dev:dir",
		Exports:                "root:dev:api",
		Providers:              "root:dev:prov",
		VirtualWorkspacePrefix: "/services/tenancy-dev",
	})
	require.NoError(t, err)

	assert.Equal(t, "root:dev:fleet", l.TenantFleetRoot)
	assert.Equal(t, "root:dev:dir", l.Directory)
	assert.Equal(t, "root:dev:api", l.Exports)
	assert.Equal(t, "root:dev:prov", l.Providers)
	assert.Equal(t, "/services/tenancy-dev", l.VirtualWorkspacePrefix)
}

func TestNewRejectsMalformed(t *testing.T) {
	for name, opts := range map[string]paths.Options{
		"empty segment in root":     {Root: "root::tenants"},
		"whitespace in root":        {Root: "root:my tenant"},
		"slash in fleet root":       {Root: "root", TenantFleetRoot: "root/tenants"},
		"vw prefix without a slash": {Root: "root", VirtualWorkspacePrefix: "services/tenancy"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := paths.New(opts)
			assert.Error(t, err)
		})
	}
}

// A blank root falls back to the default rather than producing ":tenants".
func TestNewBlankRootFallsBack(t *testing.T) {
	l, err := paths.New(paths.Options{Root: "  :  "})
	require.NoError(t, err)
	assert.Equal(t, "root", l.Root)
	assert.Equal(t, "root:tenants", l.TenantFleetRoot)
}

// The shape is not configurable: a Tenant is always a direct child of the
// fleet root, and a Workspace always a direct child of a Tenant.
func TestLayoutShape(t *testing.T) {
	l, err := paths.New(paths.Options{Root: "root"})
	require.NoError(t, err)

	tenant := l.Tenant("7f3a91d2")
	assert.Equal(t, "root:tenants:7f3a91d2", tenant)
	assert.Equal(t, "root:tenants:7f3a91d2:9c4b8e1f", l.Workspace("7f3a91d2", "9c4b8e1f"))

	parent, ok := paths.Parent(tenant)
	require.True(t, ok)
	assert.Equal(t, l.TenantFleetRoot, parent)
	assert.Equal(t, "7f3a91d2", paths.Base(tenant))
}

func TestJoin(t *testing.T) {
	assert.Equal(t, "root:system:directory", paths.Join("root", "system", "directory"))
	assert.Equal(t, "root:tenants", paths.Join("root", "", "  ", "tenants"))
	assert.Equal(t, "root:tenants", paths.Join(":root:", ":tenants:"))
	assert.Empty(t, paths.Join("", " "))
}

func TestParentAndBase(t *testing.T) {
	_, ok := paths.Parent("root")
	assert.False(t, ok, "the install root has no parent")

	assert.Equal(t, "root", paths.Base("root"))
	assert.Equal(t, "directory", paths.Base("root:system:directory"))
}
