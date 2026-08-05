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

package ocmmodule_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	descriptorruntime "ocm.software/open-component-model/bindings/go/descriptor/runtime"
	ocmruntime "ocm.software/open-component-model/bindings/go/runtime"

	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"
)

func ociResource(name, imageRef string, extra ocmruntime.Identity) descriptorruntime.Resource {
	return descriptorruntime.Resource{
		ElementMeta: descriptorruntime.ElementMeta{
			ObjectMeta:    descriptorruntime.ObjectMeta{Name: name, Version: "0.1.0"},
			ExtraIdentity: extra,
		},
		Type:     "ociImage",
		Relation: descriptorruntime.LocalRelation,
		Access: &ocmruntime.Raw{
			Type: ocmruntime.Type{Name: "ociArtifact", Version: "v1"},
			Data: []byte(`{"type":"ociArtifact/v1","imageReference":"` + imageRef + `"}`),
		},
	}
}

// A nil descriptor renders as an empty map so a payload templated without a
// component version fails on the missing key rather than on a nil map.
func TestDescriptorNil(t *testing.T) {
	out, err := ocmmodule.Descriptor(nil)
	require.NoError(t, err)
	assert.Empty(t, out)
}

// The access specification is carried through as the attribute set it was
// written as, which is what a payload reads an image reference out of.
func TestDescriptorAccess(t *testing.T) {
	desc := &descriptorruntime.Descriptor{}
	desc.Component.Name = "github.com/ntnn/kcp-marketplace"
	desc.Component.Version = "0.1.0"
	desc.Component.Provider = descriptorruntime.Provider{Name: "ntnn"}
	desc.Component.Resources = []descriptorruntime.Resource{
		ociResource("marketplace-vws", "ghcr.io/ntnn/vws:0.1.0", nil),
	}

	out, err := ocmmodule.Descriptor(desc)
	require.NoError(t, err)

	assert.Equal(t, "github.com/ntnn/kcp-marketplace", out["name"])
	assert.Equal(t, "0.1.0", out["version"])
	assert.Equal(t, "ntnn", out["provider"])

	resources, ok := out["resources"].([]any)
	require.True(t, ok)
	require.Len(t, resources, 1)

	res := resources[0].(map[string]any)
	assert.Equal(t, "marketplace-vws", res["name"])
	assert.Equal(t, "ociImage", res["type"])
	assert.Equal(t, "local", res["relation"])

	access := res["access"].(map[string]any)
	assert.Equal(t, "ghcr.io/ntnn/vws:0.1.0", access["imageReference"])
}

// Resources stay a list because a name is not an identity: a component may
// carry one resource per platform under a single name.
func TestDescriptorKeepsSameNamedResources(t *testing.T) {
	desc := &descriptorruntime.Descriptor{}
	desc.Component.Resources = []descriptorruntime.Resource{
		ociResource("cli", "ghcr.io/acme/cli:0.1.0-arm64", ocmruntime.Identity{"architecture": "arm64"}),
		ociResource("cli", "ghcr.io/acme/cli:0.1.0-amd64", ocmruntime.Identity{"architecture": "amd64"}),
	}

	out, err := ocmmodule.Descriptor(desc)
	require.NoError(t, err)

	resources := out["resources"].([]any)
	require.Len(t, resources, 2)
	assert.Equal(t, map[string]any{"architecture": "arm64"}, resources[0].(map[string]any)["extraIdentity"])
	assert.Equal(t, map[string]any{"architecture": "amd64"}, resources[1].(map[string]any)["extraIdentity"])
}

// Sources and references are rendered alongside the resources.
func TestDescriptorSourcesAndReferences(t *testing.T) {
	desc := &descriptorruntime.Descriptor{}
	desc.Component.Sources = []descriptorruntime.Source{{
		ElementMeta: descriptorruntime.ElementMeta{
			ObjectMeta: descriptorruntime.ObjectMeta{Name: "source", Version: "0.1.0"},
		},
		Type: "git",
	}}
	desc.Component.References = []descriptorruntime.Reference{{
		ElementMeta: descriptorruntime.ElementMeta{
			ObjectMeta: descriptorruntime.ObjectMeta{Name: "installer", Version: "0.2.0"},
		},
		Component: "github.com/acme/installer",
	}}

	out, err := ocmmodule.Descriptor(desc)
	require.NoError(t, err)

	sources := out["sources"].([]any)
	require.Len(t, sources, 1)
	assert.Equal(t, "git", sources[0].(map[string]any)["type"])

	refs := out["references"].([]any)
	require.Len(t, refs, 1)
	assert.Equal(t, "github.com/acme/installer", refs[0].(map[string]any)["component"])
}

// Labels are keyed by name and carry their decoded JSON value.
func TestDescriptorLabels(t *testing.T) {
	desc := &descriptorruntime.Descriptor{}
	desc.Component.Labels = []descriptorruntime.Label{
		{Name: "acme.org/tier", Value: []byte(`"gold"`)},
		{Name: "acme.org/replicas", Value: []byte(`3`)},
	}

	out, err := ocmmodule.Descriptor(desc)
	require.NoError(t, err)

	labels := out["labels"].(map[string]any)
	assert.Equal(t, "gold", labels["acme.org/tier"])
	// Kubernetes objects may only hold int64, so numbers are normalized.
	assert.Equal(t, int64(3), labels["acme.org/replicas"])
}
