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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/operator/v1alpha1"
)

// TemplateFinalizer keeps a template that a PlatformMesh still references from
// being deleted.
const TemplateFinalizer = "deploy.platform-mesh.io/in-use"

// TemplateReference references a template by name. Templates are shared, so a
// template outside the referencing PlatformMesh's namespace can be addressed
// by setting Namespace.
type TemplateReference struct {
	// Name of the template.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the template. Defaults to the PlatformMesh's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// RootShardTemplate is merged onto the RootShard resources the deployer
// renders. Deployer-owned fields win over template values.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=rst
type RootShardTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// external and cache are overwritten from the engaged clusters, but the
	// shared spec type requires them; any placeholder does.
	Spec operatorv1alpha1.RootShardSpec `json:"spec,omitempty"`
}

// RootShardTemplateList contains a list of RootShardTemplate.
// +kubebuilder:object:root=true
type RootShardTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RootShardTemplate `json:"items"`
}

// ShardTemplate is merged onto the Shard resources the deployer renders.
// Deployer-owned fields win over template values.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=st
type ShardTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// rootShard is overwritten with the RootShard rendered for the engaged
	// cluster, but the shared spec type requires it; any placeholder does.
	Spec operatorv1alpha1.ShardSpec `json:"spec,omitempty"`
}

// ShardTemplateList contains a list of ShardTemplate.
// +kubebuilder:object:root=true
type ShardTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ShardTemplate `json:"items"`
}

// FrontProxyTemplate is merged onto the FrontProxy resources the deployer
// renders. Deployer-owned fields win over template values.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=fpt
type FrontProxyTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// rootShard is overwritten with the RootShard rendered for the engaged
	// cluster, but the shared spec type requires it; any placeholder does.
	Spec operatorv1alpha1.FrontProxySpec `json:"spec,omitempty"`
}

// FrontProxyTemplateList contains a list of FrontProxyTemplate.
// +kubebuilder:object:root=true
type FrontProxyTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrontProxyTemplate `json:"items"`
}

// CacheServerTemplate is merged onto the CacheServer resources the deployer
// renders. Deployer-owned fields win over template values.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=cst
type CacheServerTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec operatorv1alpha1.CacheServerSpec `json:"spec,omitempty"`
}

// CacheServerTemplateList contains a list of CacheServerTemplate.
// +kubebuilder:object:root=true
type CacheServerTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CacheServerTemplate `json:"items"`
}

// VirtualWorkspaceTemplate is merged onto the VirtualWorkspace resources the
// deployer renders. Deployer-owned fields win over template values.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=vwt
type VirtualWorkspaceTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// external and target are overwritten from the engaged clusters, but the
	// shared spec type requires them; any placeholder does.
	Spec operatorv1alpha1.VirtualWorkspaceSpec `json:"spec,omitempty"`
}

// VirtualWorkspaceTemplateList contains a list of VirtualWorkspaceTemplate.
// +kubebuilder:object:root=true
type VirtualWorkspaceTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualWorkspaceTemplate `json:"items"`
}
