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

package celtemplate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ocmContext() Context {
	return Context{
		OCMModule: "marketplace",
		OCM: map[string]any{
			"name":    "github.com/ntnn/kcp-marketplace",
			"version": "0.1.0",
			"resources": []any{
				map[string]any{
					"name":          "marketplace-vws",
					"extraIdentity": map[string]any{},
					"access":        map[string]any{"imageReference": "ghcr.io/ntnn/vws:0.1.0"},
				},
				map[string]any{
					"name":          "cli",
					"extraIdentity": map[string]any{"architecture": "arm64"},
					"access":        map[string]any{"imageReference": "ghcr.io/acme/cli:arm64"},
				},
				map[string]any{
					"name":          "cli",
					"extraIdentity": map[string]any{"architecture": "amd64"},
					"access":        map[string]any{"imageReference": "ghcr.io/acme/cli:amd64"},
				},
			},
		},
	}
}

// A payload reads the component's own resources rather than have them passed
// through spec.values.
func TestOCMInterpolate(t *testing.T) {
	ctx := ocmContext()

	tests := []struct {
		name string
		expr string
		want any
	}{
		{
			name: "component version",
			expr: `${ocm.version}`,
			want: "0.1.0",
		},
		{
			name: "image reference of a uniquely named resource",
			expr: `${ocm.resources.byName("marketplace-vws").access.imageReference}`,
			want: "ghcr.io/ntnn/vws:0.1.0",
		},
		{
			name: "an ambiguous name is selected on extraIdentity",
			expr: `${ocm.resources.filter(r, "architecture" in r.extraIdentity && r.extraIdentity.architecture == "amd64")[0].access.imageReference}`,
			want: "ghcr.io/acme/cli:amd64",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Interpolate(tc.expr, ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A name is not an identity, so byName reports an ambiguous one rather than
// silently returning whichever variant comes first.
func TestByNameErrors(t *testing.T) {
	ctx := ocmContext()

	_, err := Interpolate(`${ocm.resources.byName("cli").access.imageReference}`, ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 elements share this name")

	_, err = Interpolate(`${ocm.resources.byName("nope").access.imageReference}`, ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// The topology path templates without a component version, so ocm is an empty
// map rather than absent.
func TestOCMEmpty(t *testing.T) {
	_, err := Interpolate(`${ocm.version}`, Context{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such key")
}
