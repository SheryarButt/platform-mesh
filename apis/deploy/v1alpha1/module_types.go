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
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Stage orders a module relative to the kcp deployment.
// +kubebuilder:validation:Enum=pre-topology;post-topology
type Stage string

const (
	// StagePreTopology modules are deployed before the kcp deployed.
	StagePreTopology Stage = "pre-topology"
	// StagePostTopology modules are deployed after kcp is deployed and ready.
	StagePostTopology Stage = "post-topology"
)

// Placement selects the clusters a module component is deployed to.
// +kubebuilder:validation:Enum=root-shard;per-shard;per-front-proxy;all-clusters
type Placement string

const (
	PlacementRootShard     Placement = "root-shard"
	PlacementPerShard      Placement = "per-shard"
	PlacementPerFrontProxy Placement = "per-front-proxy"
	PlacementAllClusters   Placement = "all-clusters"
)

// KubeconfigTarget selects the kcp endpoint a minted kubeconfig points at.
// +kubebuilder:validation:Enum=front-proxy;shard;root-shard
type KubeconfigTarget string

const (
	KubeconfigTargetFrontProxy KubeconfigTarget = "front-proxy"
	KubeconfigTargetShard      KubeconfigTarget = "shard"
	KubeconfigTargetRootShard  KubeconfigTarget = "root-shard"
)

// OCMModuleWorkspace is a kcp workspace that will be created for the module.
type OCMModuleWorkspace struct {
	// Name is the workspace name relative to the module workspace.
	// Empty targets the module workspace itself. Children must not be nested.
	// The empty default is required because the field is the list map key.
	// +optional
	// +kubebuilder:default=""
	// +kubebuilder:validation:Pattern=`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)?$`
	Name string `json:"name,omitempty"`

	// Content references the component version resources holding the
	// manifests that should be applied in this workspace.
	// +optional
	Content []ResourceRef `json:"content,omitempty"`
}

// OCMModuleKubeconfig allows a module to request a kubeconfig for a specific target.
type OCMModuleKubeconfig struct {
	// Name identifies the kubeconfig.
	// Components reference it by this name and the minted secret is named <module>-<name>.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Target selects the kcp component the kubeconfig points at.
	Target KubeconfigTarget `json:"target"`

	// Workspace names the module workspace the kubeconfig is scoped to.
	// Empty targets the module workspace itself.
	// +optional
	Workspace string `json:"workspace,omitempty"`
}

// Mapping exposes a component under a URI path of the front proxy.
//
// The path is the component's only entrypoint: callers always arrive at the
// front proxy, which then dials the backend named below. Where that backend is
// reachable from is what Service and Host distinguish — exactly one is set.
// +kubebuilder:validation:XValidation:rule="has(self.service) != has(self.host)",message="set exactly one of service (a backend in the front proxy's own cluster) or host (a backend reached over the network)"
type Mapping struct {
	// Path is the URI path added to the front proxy's path mappings, e.g.
	// /services/acme/.
	// +kubebuilder:validation:Pattern=`^/.*$`
	Path string `json:"path"`

	// Service is the name of the backend Service, for a backend in the SAME
	// cluster as the front proxy. Resolved as <service>.<component namespace>.svc.
	// Supports ${ ... } templating.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Service string `json:"service,omitempty"`

	// Host is the DNS name of the backend, for a backend the front proxy cannot
	// reach by Service DNS — a component placed on a different cluster, reached
	// over an ingress or gateway.
	//
	// The front proxy verifies the certificate the backend presents, so the
	// backend's serving certificate must carry this name. The deployer adds it to
	// the certificate it issues for this component; a backend serving a
	// certificate of its own has to include it.
	//
	// Terminating the connection between the front proxy and the backend breaks
	// that verification, so whatever routes this name must pass TLS through.
	// Supports ${ ... } templating.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host,omitempty"`

	// Port is the port the backend serves TLS on.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}

// OCMModuleComponent is one deployable unit of a module.
// +kubebuilder:validation:XValidation:rule="!has(self.mapping) || self.placement == 'per-front-proxy'",message="mapping is only supported for per-front-proxy components"
type OCMModuleComponent struct {
	// Name identifies the component within the module.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Resource is the name of the component version resource holding the
	// component's manifests.
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`

	// Placement selects the clusters the component is deployed to.
	Placement Placement `json:"placement"`

	// Namespace is the namespace the component's objects are created in.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// Kubeconfigs references entries of OCMModuleSpec.Kubeconfigs that are minted for this component and synced to its clusters.
	// +optional
	// +listType=set
	Kubeconfigs []string `json:"kubeconfigs,omitempty"`

	// Mapping exposes the component under a URI path of the front proxy.
	// Only supported when Placement is per-front-proxy.
	// +optional
	Mapping *Mapping `json:"mapping,omitempty"`
}

// OCMModuleSpec defines the desired state of a OCMModule deployed on top of a PlatformMesh installation.
// +kubebuilder:validation:XValidation:rule="self.stage != 'pre-topology' || !has(self.workspaces)",message="pre-topology modules cannot declare workspaces"
// +kubebuilder:validation:XValidation:rule="self.stage != 'pre-topology' || !has(self.kubeconfigs)",message="pre-topology modules cannot declare kubeconfigs"
type OCMModuleSpec struct {
	// PlatformMeshRef references the PlatformMesh installation the module is deployed to.
	PlatformMeshRef corev1.LocalObjectReference `json:"platformMeshRef"`

	// Stage orders the module relative to the kcp deployment.
	Stage Stage `json:"stage"`

	// DependsOn references OCMModules of the same PlatformMesh that must be ready before this one is deployed.
	// +optional
	DependsOn []corev1.LocalObjectReference `json:"dependsOn,omitempty"`

	// The OCM repository the module component is resolved from.
	// +optional
	OCM *OCMRepository `json:"ocm,omitempty"`

	// Component is the name of the module's OCM component, e.g. github.com/platform-mesh/platform-mesh/security-operator.
	// +kubebuilder:validation:MinLength=1
	Component string `json:"component"`

	// Version is the pinned version of the module component.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// Values holds module-specific configuration, exposed to the payload templating as ${values.*}.
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`

	// Workspaces are the kcp workspaces the module needs provisioned.
	// +optional
	// +listType=map
	// +listMapKey=name
	Workspaces []OCMModuleWorkspace `json:"workspaces,omitempty"`

	// Kubeconfigs are the kcp kubeconfigs minted for this module and referenced by its components.
	// +optional
	// +listType=map
	// +listMapKey=name
	Kubeconfigs []OCMModuleKubeconfig `json:"kubeconfigs,omitempty"`

	// Components are the deployable units of the module.
	// +optional
	// +listType=map
	// +listMapKey=name
	Components []OCMModuleComponent `json:"components,omitempty"`
}

// OCMModuleWorkspaceStatus records a provisioned workspace.
type OCMModuleWorkspaceStatus struct {
	// Name is the workspace name relative to the module workspace.
	// The empty default is required because the field is the list map key.
	// +optional
	// +kubebuilder:default=""
	Name string `json:"name,omitempty"`

	// Path is the resolved absolute workspace path.
	// +optional
	Path string `json:"path,omitempty"`

	// Ready reports whether the workspace has been provisioned.
	// +optional
	Ready bool `json:"ready,omitempty"`
}

// OCMModuleInstanceStatus records one component deployed to one cluster.
type OCMModuleInstanceStatus struct {
	// Cluster is the ID of the cluster the instance runs on.
	Cluster string `json:"cluster"`

	// Namespace is the namespace the instance's objects were created in.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// ConfigMap is the name of the generated ConfigMap holding the instance's runtime facts.
	// +optional
	ConfigMap string `json:"configMap,omitempty"`

	// Secrets lists the kubeconfig secrets synced to this cluster.
	// +optional
	// +listType=set
	Secrets []string `json:"secrets,omitempty"`

	// Mapping is the front proxy path mapping this instance is served
	// under, once its backend and certificate are resolved.
	// +optional
	Mapping *ResolvedMapping `json:"mapping,omitempty"`

	// Ready reports whether the instance's objects have been applied.
	// +optional
	Ready bool `json:"ready,omitempty"`
}

// ResolvedMapping is a component's path mapping with its templating resolved,
// ready for the topology to merge into the front proxy.
type ResolvedMapping struct {
	// Path is the URI path the front proxy routes to the component.
	Path string `json:"path"`

	// Backend is the URL of the component's Service.
	Backend string `json:"backend"`
}

// OCMModuleComponentStatus records the fan-out of one component.
type OCMModuleComponentStatus struct {
	// Name is the component name.
	Name string `json:"name"`

	// Placement is the resolved placement of the component.
	// +optional
	Placement Placement `json:"placement,omitempty"`

	// Instances are the per-cluster deployments of the component.
	// +optional
	// +listType=map
	// +listMapKey=cluster
	Instances []OCMModuleInstanceStatus `json:"instances,omitempty"`
}

// OCMModuleStatus defines the observed state of a OCMModule.
type OCMModuleStatus struct {
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	NextReconcileTime metav1.Time `json:"nextReconcileTime,omitempty"`

	// ResolvedDigest is the digest of the resolved component version.
	// +optional
	ResolvedDigest string `json:"resolvedDigest,omitempty"`

	// Workspaces are the module's provisioned kcp workspaces.
	// +optional
	// +listType=map
	// +listMapKey=name
	Workspaces []OCMModuleWorkspaceStatus `json:"workspaces,omitempty"`

	// Components are the deployed components and their instances.
	// +optional
	// +listType=map
	// +listMapKey=name
	Components []OCMModuleComponentStatus `json:"components,omitempty"`

	// AppliedKinds are the kinds the module's payloads produced. Teardown
	// needs them because a deleted module can no longer resolve its
	// component version to find out what it once applied.
	// +optional
	AppliedKinds []GroupVersionKind `json:"appliedKinds,omitempty"`
}

// GroupVersionKind identifies a kind the deployer applied.
type GroupVersionKind struct {
	// +optional
	Group string `json:"group,omitempty"`

	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`
}

// OCMModule is the schema for a module deployed on top of a PlatformMesh installation.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Component",type=string,JSONPath=`.spec.component`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Stage",type=string,JSONPath=`.spec.stage`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type OCMModule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OCMModuleSpec   `json:"spec,omitempty"`
	Status OCMModuleStatus `json:"status,omitempty"`
}

// OCMModuleList contains a list of OCMModule.
// +kubebuilder:object:root=true
type OCMModuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OCMModule `json:"items"`
}

func (m *OCMModule) GetObservedGeneration() int64       { return m.Status.ObservedGeneration }
func (m *OCMModule) SetObservedGeneration(g int64)      { m.Status.ObservedGeneration = g }
func (m *OCMModule) GetNextReconcileTime() metav1.Time  { return m.Status.NextReconcileTime }
func (m *OCMModule) SetNextReconcileTime(t metav1.Time) { m.Status.NextReconcileTime = t }
func (m *OCMModule) GetConditions() []metav1.Condition  { return m.Status.Conditions }
func (m *OCMModule) SetConditions(c []metav1.Condition) { m.Status.Conditions = c }
