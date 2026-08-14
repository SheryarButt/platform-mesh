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

package deployer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestControllerEnabled(t *testing.T) {
	cfg := Config{EnabledControllers: []string{ControllerConfig, ControllerModule}}

	assert.True(t, cfg.controllerEnabled(ControllerConfig))
	assert.True(t, cfg.controllerEnabled(ControllerModule))
	assert.False(t, cfg.controllerEnabled(ControllerCopy))
	assert.False(t, cfg.controllerEnabled(ControllerProvisioner))
	assert.False(t, cfg.controllerEnabled(ControllerKcpConfig))
	assert.False(t, cfg.controllerEnabled(ControllerKcpWorkload))
}

func TestControllerEnabledEmpty(t *testing.T) {
	assert.False(t, Config{}.controllerEnabled(ControllerConfig))
}

// The scheme has to carry everything the deployer touches, or the controllers
// fail at runtime with "no kind registered".
func TestNewSchemeKnowsEveryGroup(t *testing.T) {
	s := NewScheme()

	for _, gv := range []struct{ group, version, kind string }{
		{"deploy.platform-mesh.io", "v1alpha1", "PlatformMesh"},
		{"deploy.platform-mesh.io", "v1alpha1", "OCMModule"},
		{"deploy.platform-mesh.io", "v1alpha1", "OCMModuleSetup"},
		{"operator.kcp.io", "v1alpha1", "RootShard"},
		{"operator.kcp.io", "v1alpha1", "Kubeconfig"},
		{"deploy.operator.kcp.io", "v1alpha1", "CompiledRootShard"},
		{"gateway.networking.k8s.io", "v1", "Gateway"},
		{"gateway.networking.k8s.io", "v1alpha2", "TLSRoute"},
		{"tenancy.kcp.io", "v1alpha1", "Workspace"},
		{"core.kcp.io", "v1alpha1", "Shard"},
		{"cert-manager.io", "v1", "Certificate"},
		{"cert-manager.io", "v1", "Issuer"},
		{"", "v1", "Secret"},
	} {
		t.Run(gv.group+"/"+gv.kind, func(t *testing.T) {
			gvk := schema.GroupVersionKind{Group: gv.group, Version: gv.version, Kind: gv.kind}
			require.True(t, s.Recognizes(gvk), "scheme must know %s", gvk)
		})
	}
}
