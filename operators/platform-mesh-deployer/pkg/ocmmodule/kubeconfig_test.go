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

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"
)

// An empty name is the module's own workspace; anything else is a direct child.
func TestWorkspacePath(t *testing.T) {
	assert.Equal(t, "root:modules:acme", ocmmodule.WorkspacePath("acme", ""))
	assert.Equal(t, "root:modules:acme:validation", ocmmodule.WorkspacePath("acme", "validation"))
}

// Derived names carry no cluster ID: a cluster holds at most one instance of a
// component, so they cannot collide there.
func TestDerivedNames(t *testing.T) {
	assert.Equal(t, "acme-kcp", ocmmodule.KubeconfigSecretName("acme", "kcp"))
	assert.Equal(t, "acme-kcp-s1", ocmmodule.KubeconfigName("acme", "kcp", "s1"))
	assert.Equal(t, "acme-app-serving", ocmmodule.ServingCertSecretName("acme", "app"))
	assert.Equal(t, "acme-app-serving-s1", ocmmodule.ServingCertName("acme", "app", "s1"))
	assert.Equal(t, "acme-app-requestheader-ca", ocmmodule.RequestHeaderCASecretName("acme", "app"))
	assert.Equal(t, "acme-app", ocmmodule.ConfigMapName("acme", "app"))
}

// A component only gets the kubeconfigs it references, in the order it lists
// them, and unknown names are ignored rather than failing the render.
func TestKubeconfigsOfComponent(t *testing.T) {
	mod := moduleWith(component("app", pmdeployv1alpha1.PlacementRootShard))
	mod.Spec.Kubeconfigs = []pmdeployv1alpha1.OCMModuleKubeconfig{
		{Name: "kcp", Target: pmdeployv1alpha1.KubeconfigTargetFrontProxy},
		{Name: "shardadmin", Target: pmdeployv1alpha1.KubeconfigTargetShard},
	}
	resolved := &ocmmodule.Resolved{OCMModule: mod}

	got := resolved.Kubeconfigs(pmdeployv1alpha1.OCMModuleComponent{
		Kubeconfigs: []string{"shardadmin", "unknown", "kcp"},
	})
	names := make([]string, 0, len(got))
	for _, kc := range got {
		names = append(names, kc.Name)
	}
	assert.Equal(t, []string{"shardadmin", "kcp"}, names)
}

// The module's own workspace is separate so templating does not need an empty
// map key for it.
func TestWorkspacePaths(t *testing.T) {
	mod := moduleWith(component("app", pmdeployv1alpha1.PlacementRootShard))
	mod.Spec.Workspaces = []pmdeployv1alpha1.OCMModuleWorkspace{
		{Name: ""},
		{Name: "validation"},
	}
	resolved := &ocmmodule.Resolved{OCMModule: mod}

	own, children := resolved.WorkspacePaths()
	assert.Equal(t, "root:modules:acme", own)
	assert.Equal(t, map[string]string{"validation": "root:modules:acme:validation"}, children)
}
