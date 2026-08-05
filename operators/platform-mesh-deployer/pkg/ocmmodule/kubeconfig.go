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

package ocmmodule

import (
	"strings"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
)

// WorkspaceBase is the tree every module's workspaces live under.
const WorkspaceBase = "root:modules"

// WorkspacePath resolves a module workspace name to its absolute kcp path. An
// empty name is the module's own workspace, a non-empty name a direct child.
func WorkspacePath(module, name string) string {
	base := WorkspaceBase + ":" + module
	if name == "" {
		return base
	}
	return base + ":" + name
}

// KubeconfigSecretName is the name a minted kubeconfig has on the cluster of
// the component that mounts it. It carries no cluster ID: a cluster holds at
// most one instance of a component, so the name cannot collide.
func KubeconfigSecretName(module, kubeconfig string) string {
	return module + "-" + kubeconfig
}

// KubeconfigName is the name of the kcp-operator Kubeconfig object and of the
// secret it mints on the config plane. Those live in one namespace for the
// whole PlatformMesh, so they carry the cluster they were minted for.
func KubeconfigName(module, kubeconfig, clusterID string) string {
	return module + "-" + kubeconfig + "-" + clusterID
}

// Kubeconfigs returns the module kubeconfigs a component references, in the
// order the component lists them.
func (r *Resolved) Kubeconfigs(component pmdeployv1alpha1.OCMModuleComponent) []pmdeployv1alpha1.OCMModuleKubeconfig {
	declared := make(map[string]pmdeployv1alpha1.OCMModuleKubeconfig, len(r.OCMModule.Spec.Kubeconfigs))
	for _, kc := range r.OCMModule.Spec.Kubeconfigs {
		declared[kc.Name] = kc
	}

	out := make([]pmdeployv1alpha1.OCMModuleKubeconfig, 0, len(component.Kubeconfigs))
	for _, name := range component.Kubeconfigs {
		if kc, ok := declared[name]; ok {
			out = append(out, kc)
		}
	}
	return out
}

// WorkspacePaths returns the module's workspaces by name, with the module's own
// workspace separate: an empty map key would be awkward to template with.
func (r *Resolved) WorkspacePaths() (string, map[string]string) {
	own := WorkspacePath(r.OCMModule.Name, "")
	children := map[string]string{}
	for _, ws := range r.OCMModule.Spec.Workspaces {
		if ws.Name == "" {
			continue
		}
		children[ws.Name] = WorkspacePath(r.OCMModule.Name, ws.Name)
	}
	return own, children
}

// envSuffix turns a declared name into an environment variable suffix.
func envSuffix(name string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
}

// ServingCertName is the name of the cert-manager Certificate and of the secret
// it issues on the config plane for a mapped component.
func ServingCertName(module, component, clusterID string) string {
	return module + "-" + component + "-serving-" + clusterID
}

// ServingCertSecretName is the name that certificate has on the cluster of the
// component that serves it.
func ServingCertSecretName(module, component string) string {
	return module + "-" + component + "-serving"
}

// RequestHeaderCASecretName is the name the front proxy's requestheader CA has
// on the cluster of a mapped component.
//
// The CA itself is a kcp-operator secret named after the root shard, whose name
// is an opaque hash. Copying it under a name the component derives from its own
// keeps that convention inside the deployer.
func RequestHeaderCASecretName(module, component string) string {
	return module + "-" + component + "-requestheader-ca"
}
