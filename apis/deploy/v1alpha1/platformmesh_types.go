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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OCMRepository describes an OCM repository and the component to resolve from it.
type OCMRepository struct {
	// URL is the OCM repository (registry) URL.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Component is the name of the OCM component to resolve.
	// +optional
	Component string `json:"component,omitempty"`

	// SecretRef references a secret containing credentials for the repository.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// Exposure describes how an endpoint is exposed through the defined ingress stacks.
type Exposure struct {
	// HostnameTemplate is the hostname the endpoint is exposed under.
	// It supports the template variables {{ .Name }} and {{ .Cluster }}.
	// +kubebuilder:validation:MinLength=1
	HostnameTemplate string `json:"hostnameTemplate"`

	// Port is the port the endpoint is exposed on.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// IngressRefs references ingress stacks by name.
	// If empty, the endpoint is exposed through all defined ingress stacks.
	// +optional
	// +listType=set
	IngressRefs []string `json:"ingressRefs,omitempty"`
}

// IngressType is the type of an ingress stack.
// +kubebuilder:validation:Enum=gatewayapi;istio;traefik
type IngressType string

const (
	IngressTypeGatewayAPI IngressType = "gatewayapi"
	IngressTypeIstio      IngressType = "istio"
	IngressTypeTraefik    IngressType = "traefik"
)

// IngressStack describes one ingress technology deployment endpoints are exposed through.
// Multiple stacks of different types can coexist.
// +kubebuilder:validation:XValidation:rule="self.type != 'gatewayapi' || has(self.gatewayAPI)",message="gatewayAPI must be set when type is gatewayapi"
type IngressStack struct {
	// Name identifies the ingress stack. Referenced by Exposure.IngressRefs.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Type is the ingress technology of this stack.
	Type IngressType `json:"type"`

	// GatewayAPI configures the stack when Type is gatewayapi.
	// +optional
	GatewayAPI *GatewayAPIValues `json:"gatewayAPI,omitempty"`
}

// GatewayAPIValues is the configuration of a gatewayapi IngressStack.
// This is only configuration for the TLSRoutes.
type GatewayAPIValues struct {
	// GatewayName is the name of the shared Gateway the TLSRoutes attach to.
	// +kubebuilder:validation:MinLength=1
	GatewayName string `json:"gatewayName"`

	// GatewayNamespace is the namespace of the Gateway. Optional; defaults to the route's namespace.
	// +optional
	GatewayNamespace string `json:"gatewayNamespace,omitempty"`

	// SectionName optionally pins a specific Gateway listener.
	// +optional
	SectionName string `json:"sectionName,omitempty"`
}

// VirtualWorkspaceMode is the deployment mode of the virtual workspaces server.
// +kubebuilder:validation:Enum=Embedded;Standalone
type VirtualWorkspaceMode string

const (
	VirtualWorkspaceModeEmbedded   VirtualWorkspaceMode = "Embedded"
	VirtualWorkspaceModeStandalone VirtualWorkspaceMode = "Standalone"
)

// VirtualWorkspaceSpec configures the virtual workspaces server of a shard group.
type VirtualWorkspaceSpec struct {
	// Mode selects whether virtual workspaces are served embedded in the
	// shard or by a standalone deployment.
	// +optional
	// +kubebuilder:default=Embedded
	Mode VirtualWorkspaceMode `json:"mode,omitempty"`

	// TemplateRef references a VirtualWorkspaceTemplate merged onto the
	// rendered VirtualWorkspace resource.
	// Deployer-owned fields (target, external) win over template values.
	// +optional
	TemplateRef *TemplateReference `json:"templateRef,omitempty"`

	// Exposure describes how the virtual workspaces endpoint is exposed.
	// +optional
	Exposure *Exposure `json:"exposure,omitempty"`
}

// ShardGroup describes a group of kcp shards. One shard is deployed per
// matched cluster.
type ShardGroup struct {
	// Name identifies the shard group.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// TemplateRef references a ShardTemplate merged onto the rendered Shard
	// resources.
	// +optional
	TemplateRef *TemplateReference `json:"templateRef,omitempty"`

	// CacheServerRef references a cache server by name.
	// Defaults to the sole defined cache server.
	// +optional
	CacheServerRef string `json:"cacheServerRef,omitempty"`

	// Exposure describes how the shard endpoint is exposed.
	// +optional
	Exposure *Exposure `json:"exposure,omitempty"`

	// VirtualWorkspaces configures the virtual workspaces server for this
	// shard group.
	VirtualWorkspaces VirtualWorkspaceSpec `json:"virtualWorkspaces"`
}

// FrontProxy describes a kcp front-proxy deployment.
type FrontProxy struct {
	// Name identifies the front proxy.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// TemplateRef references a FrontProxyTemplate merged onto the rendered
	// FrontProxy resource.
	// Deployer-owned fields (rootShard ref, external/exposure) win over
	// template values.
	// +optional
	TemplateRef *TemplateReference `json:"templateRef,omitempty"`

	// Exposure describes how the front proxy is exposed.
	// +optional
	Exposure *Exposure `json:"exposure,omitempty"`
}

// CacheServer describes a kcp cache server deployment.
// +kubebuilder:validation:XValidation:rule="!has(self.seedRef) || self.seedRef.size() == 0",message="seedRef is not supported in v1alpha1 (see kcp-dev/kcp#4055)"
type CacheServer struct {
	// Name identifies the cache server, e.g. "global", "eu", "us".
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// TemplateRef references a CacheServerTemplate merged onto the rendered
	// CacheServer resource.
	// Deployer-owned fields (certificates) win over template values.
	// +optional
	TemplateRef *TemplateReference `json:"templateRef,omitempty"`

	// SeedRef references another cache server to seed from, enabling a
	// future federation upgrade path. Must be empty in v1alpha1.
	// +optional
	SeedRef string `json:"seedRef,omitempty"`
}

// RootShard describes the kcp root shard.
// One RootShard is deployed per engaged root-shard cluster.
type RootShard struct {
	// Name identifies the root shard group.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// TemplateRef references a RootShardTemplate merged onto the rendered
	// RootShard resources.
	// +optional
	TemplateRef *TemplateReference `json:"templateRef,omitempty"`

	// CacheServerRef references a cache server by name.
	// +optional
	CacheServerRef string `json:"cacheServerRef,omitempty"`

	// Exposure describes how the root shard endpoint is exposed.
	// +optional
	Exposure *Exposure `json:"exposure,omitempty"`

	// VirtualWorkspaces configures the virtual workspaces server for the root shard.
	VirtualWorkspaces VirtualWorkspaceSpec `json:"virtualWorkspaces"`
}

// Topology describes the kcp topology of a PlatformMesh installation.
type Topology struct {
	// RootShard is the kcp root shard.
	RootShard RootShard `json:"rootShard"`

	// ShardGroups are additional kcp shard groups.
	// +optional
	// +listType=map
	// +listMapKey=name
	ShardGroups []ShardGroup `json:"shardGroups,omitempty"`

	// FrontProxy is the kcp front proxy.
	FrontProxy FrontProxy `json:"frontProxy"`

	// CacheServer is the kcp cache server.
	// If unset, the root shard's embedded cache is used.
	// +optional
	CacheServer *CacheServer `json:"cacheServer,omitempty"`
}

// PlatformMeshSpec defines the desired state of a PlatformMesh installation.
type PlatformMeshSpec struct {
	// Version is the pinned version of the aggregate OCM component version.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// OCM describes the OCM repository and aggregate component to deploy
	// from.
	OCM OCMRepository `json:"ocm"`

	// Topology describes the kcp topology.
	Topology Topology `json:"topology"`

	// Ingress defines the ingress stacks endpoints are exposed through. All
	// types can coexist.
	// +optional
	// +listType=map
	// +listMapKey=name
	Ingress []IngressStack `json:"ingress,omitempty"`
}

// ShardAssignment records the cluster a shard has been assigned to.
type ShardAssignment struct {
	// ShardGroup is the name of the shard group.
	ShardGroup string `json:"shardGroup"`

	// Shard is the name of the shard.
	Shard string `json:"shard"`

	// Cluster is the name of the cluster the shard is assigned to.
	Cluster string `json:"cluster"`
}

// PlatformMeshStatus defines the observed state of a PlatformMesh
// installation.
type PlatformMeshStatus struct {
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	NextReconcileTime metav1.Time `json:"nextReconcileTime,omitempty"`

	// ResolvedVersion is the resolved component version of the aggregate
	// component.
	// +optional
	ResolvedVersion string `json:"resolvedVersion,omitempty"`

	// ResolvedDigest is the digest of the resolved component version.
	// +optional
	ResolvedDigest string `json:"resolvedDigest,omitempty"`

	// ShardAssignments is the shard to cluster assignment table.
	// +optional
	ShardAssignments []ShardAssignment `json:"shardAssignments,omitempty"`

	// RootShardCluster is the cluster the root shard is deployed to.
	// This field is only used to ensure that the root cluster provider does not yield more than one cluster.
	// +optional
	RootShardCluster string `json:"rootShardCluster,omitempty"`
}

// PlatformMesh is the schema for a Platform Mesh installation managed by the
// deployer.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Resolved",type=string,JSONPath=`.status.resolvedVersion`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type PlatformMesh struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PlatformMeshSpec   `json:"spec,omitempty"`
	Status PlatformMeshStatus `json:"status,omitempty"`
}

// PlatformMeshList contains a list of PlatformMesh.
// +kubebuilder:object:root=true
type PlatformMeshList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PlatformMesh `json:"items"`
}

func (pm *PlatformMesh) GetObservedGeneration() int64       { return pm.Status.ObservedGeneration }
func (pm *PlatformMesh) SetObservedGeneration(g int64)      { pm.Status.ObservedGeneration = g }
func (pm *PlatformMesh) GetNextReconcileTime() metav1.Time  { return pm.Status.NextReconcileTime }
func (pm *PlatformMesh) SetNextReconcileTime(t metav1.Time) { pm.Status.NextReconcileTime = t }
func (pm *PlatformMesh) GetConditions() []metav1.Condition  { return pm.Status.Conditions }
func (pm *PlatformMesh) SetConditions(c []metav1.Condition) { pm.Status.Conditions = c }
