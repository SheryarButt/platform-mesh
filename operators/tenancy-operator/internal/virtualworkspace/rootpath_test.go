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

package virtualworkspace_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.platform-mesh.io/tenancy-operator/internal/virtualworkspace"
)

const prefix = "/services/tenancy"

// The prefixToStrip must leave exactly the kube API surface behind, or every
// client breaks in a way that looks like a routing bug rather than a config one.
func TestResolveRootPathStripsThroughTheClusterSegment(t *testing.T) {
	r := virtualworkspace.NewRootPathResolver(prefix)

	for name, tc := range map[string]struct {
		url          string
		wantCluster  string
		wantStripped string
	}{
		"one tenant": {
			url:          prefix + "/clusters/2cyb4oxml4sv8o3r/apis/tenancy.platform-mesh.io/v1alpha1/memberships",
			wantCluster:  "2cyb4oxml4sv8o3r",
			wantStripped: prefix + "/clusters/2cyb4oxml4sv8o3r",
		},
		"wildcard across the fleet": {
			url:          prefix + "/clusters/*/apis/tenancy.platform-mesh.io/v1alpha1/users",
			wantCluster:  virtualworkspace.WildcardCluster,
			wantStripped: prefix + "/clusters/*",
		},
		"discovery, no trailing path": {
			url:          prefix + "/clusters/*",
			wantCluster:  virtualworkspace.WildcardCluster,
			wantStripped: prefix + "/clusters/*",
		},
	} {
		t.Run(name, func(t *testing.T) {
			accepted, stripped, ctx := r(tc.url, context.Background())
			require.True(t, accepted)
			assert.Equal(t, tc.wantStripped, stripped)

			cluster, ok := virtualworkspace.ClusterFrom(ctx)
			require.True(t, ok, "the cluster scope must reach the storage layer")
			assert.Equal(t, tc.wantCluster, cluster)
		})
	}
}

// Anything this virtual workspace does not own must be declined, not guessed at:
// the root API server hosts several, and accepting a foreign path would shadow
// whichever one actually owns it.
func TestResolveRootPathDeclinesForeignPaths(t *testing.T) {
	r := virtualworkspace.NewRootPathResolver(prefix)

	for name, url := range map[string]string{
		"another virtual workspace": "/services/apiexport/root/foo/clusters/x/apis",
		"no cluster segment":        prefix + "/apis/tenancy.platform-mesh.io/v1alpha1/users",
		"empty cluster segment":     prefix + "/clusters//apis",
		"prefix only":               prefix,
		"unrelated":                 "/healthz",
	} {
		t.Run(name, func(t *testing.T) {
			accepted, _, _ := r(url, context.Background())
			assert.False(t, accepted)
		})
	}
}

// The mount point is configuration: two installs on one kcp must not both answer
// at the same prefix, or each singleton serves the other's callers.
func TestResolveRootPathHonoursAConfiguredPrefix(t *testing.T) {
	r := virtualworkspace.NewRootPathResolver("/services/tenancy-dev")

	accepted, stripped, _ := r("/services/tenancy-dev/clusters/*/apis", context.Background())
	require.True(t, accepted)
	assert.Equal(t, "/services/tenancy-dev/clusters/*", stripped)

	// The default install's prefix must not be served by this one.
	accepted, _, _ = r(prefix+"/clusters/*/apis", context.Background())
	assert.False(t, accepted)
}
