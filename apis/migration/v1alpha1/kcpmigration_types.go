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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase represents the current phase of the migration
type Phase string

const (
	// PhasePending indicates the migration is created, waiting for child operator
	PhasePending Phase = "Pending"
	// PhaseRunning indicates the child operator is running and syncing
	PhaseRunning Phase = "Running"
	// PhaseFailed indicates the child operator failed to start or is in error state
	PhaseFailed Phase = "Failed"
	// PhaseStopped indicates the migration was manually stopped
	PhaseStopped Phase = "Stopped"
)

// KCPMigrationSpec defines the desired state of KCPMigration
type KCPMigrationSpec struct {
	// Source defines the source resources to watch
	// +kubebuilder:validation:Required
	Source SourceSpec `json:"source"`

	// Transform defines how source resources are transformed and where they are placed in kcp
	// +kubebuilder:validation:Required
	Transform TransformSpec `json:"transform"`

	// SyncOptions provides optional configuration for sync behavior
	// +optional
	SyncOptions *SyncOptions `json:"syncOptions,omitempty"`
}

// SourceSpec defines the source resources to watch
type SourceSpec struct {
	// APIVersion of the source resource (e.g., apps.example.com/v1)
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*\/v[a-z0-9]+(alpha[0-9]+|beta[0-9]+)?$`
	APIVersion string `json:"apiVersion"`

	// Kind of the source resource (e.g., MyApp, TenantConfig)
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// Namespace to filter source resources (optional, empty = all namespaces)
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// LabelSelectors is a list of label selectors to filter source resources.
	// Resources must match ALL selectors (AND logic).
	// Supports standard Kubernetes label selector syntax:
	// - Equality: "app=myapp", "env!=dev"
	// - Set-based: "env in (prod,staging)", "tier notin (frontend)"
	// - Existence: "app" (key exists), "!temp" (key doesn't exist)
	// +optional
	LabelSelectors []string `json:"labelSelectors,omitempty"`
}

// TransformSpec defines how source resources are transformed and where they are placed
type TransformSpec struct {
	// TargetWorkspace defines how to derive the target kcp workspace
	// +kubebuilder:validation:Required
	TargetWorkspace WorkspaceExpression `json:"targetWorkspace"`

	// TargetNamespace specifies the namespace in the target workspace
	// Supports Go template syntax. If not specified, uses source namespace or cluster-scoped
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`

	// Template defines the target resource structure
	// If not specified, resource is synced as-is (pass-through mode)
	// +optional
	Template *TemplateSpec `json:"template,omitempty"`
}

// WorkspaceExpression defines how to derive the target kcp workspace
type WorkspaceExpression struct {
	// Expression is a Go template expression to derive the workspace path
	// Has access to .Source (full resource), .Source.metadata.namespace, .Source.metadata.name, etc.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`
}

// TemplateSpec defines the target resource structure
type TemplateSpec struct {
	// Inline template for target resource as raw JSON/YAML
	// +optional
	Inline *apiextensionsv1.JSON `json:"inline,omitempty"`

	// ConfigMapRef references a ConfigMap containing the template
	// +optional
	ConfigMapRef *ConfigMapReference `json:"configMapRef,omitempty"`
}

// ConfigMapReference references a ConfigMap containing a template
type ConfigMapReference struct {
	// Name of the ConfigMap
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Key in the ConfigMap. Defaults to "template.yaml"
	// +kubebuilder:default="template.yaml"
	// +optional
	Key string `json:"key,omitempty"`
}

// SyncOptions provides optional configuration for sync behavior
type SyncOptions struct {
	// RateLimit controls how fast resources are synced
	// +optional
	RateLimit *RateLimitConfig `json:"rateLimit,omitempty"`
}

// RateLimitConfig controls sync rate limiting
type RateLimitConfig struct {
	// ResourcesPerSecond is the maximum resources to sync per second
	// +kubebuilder:default=50
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	// +optional
	ResourcesPerSecond int `json:"resourcesPerSecond,omitempty"`

	// Burst is the maximum burst size for the rate limiter
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2000
	// +optional
	Burst int `json:"burst,omitempty"`
}

// KCPMigrationStatus defines the observed state of KCPMigration
type KCPMigrationStatus struct {
	// Phase represents the current phase of the migration
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// ChildOperator contains information about the child operator deployment
	// +optional
	ChildOperator *ChildOperatorStatus `json:"childOperator,omitempty"`

	// Statistics contains sync statistics
	// +optional
	Statistics *SyncStatistics `json:"statistics,omitempty"`

	// Conditions represent the latest available observations of the migration's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the last observed generation of the KCPMigration
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NextReconcileTime is the next scheduled reconciliation time
	// +optional
	NextReconcileTime *metav1.Time `json:"nextReconcileTime,omitempty"`
}

// ChildOperatorStatus contains information about the child operator
type ChildOperatorStatus struct {
	// Name of the child operator Deployment
	Name string `json:"name,omitempty"`

	// Namespace of the child operator
	Namespace string `json:"namespace,omitempty"`

	// Ready indicates whether the child operator is ready
	Ready bool `json:"ready,omitempty"`
}

// SyncStatistics contains statistics about the sync process
type SyncStatistics struct {
	// LastSyncTime is the last successful sync time
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// ResourcesSynced is the total number of resources successfully synced
	// +optional
	ResourcesSynced int64 `json:"resourcesSynced,omitempty"`

	// ResourcesFailed is the number of resources that failed to sync
	// +optional
	ResourcesFailed int64 `json:"resourcesFailed,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=kcpmig
// +kubebuilder:printcolumn:name="Source Kind",type=string,JSONPath=`.spec.source.kind`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Synced",type=integer,JSONPath=`.status.statistics.resourcesSynced`
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.statistics.resourcesFailed`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KCPMigration is the Schema for the kcpmigrations API
type KCPMigration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KCPMigrationSpec   `json:"spec,omitempty"`
	Status KCPMigrationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KCPMigrationList contains a list of KCPMigration
type KCPMigrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KCPMigration `json:"items"`
}

// RuntimeObject interface implementation for golang-commons lifecycle framework

func (m *KCPMigration) GetObservedGeneration() int64 {
	return m.Status.ObservedGeneration
}

func (m *KCPMigration) SetObservedGeneration(g int64) {
	m.Status.ObservedGeneration = g
}

func (m *KCPMigration) GetNextReconcileTime() metav1.Time {
	if m.Status.NextReconcileTime == nil {
		return metav1.Time{}
	}
	return *m.Status.NextReconcileTime
}

func (m *KCPMigration) SetNextReconcileTime(time metav1.Time) {
	m.Status.NextReconcileTime = &time
}

func (m *KCPMigration) GetConditions() []metav1.Condition {
	return m.Status.Conditions
}

func (m *KCPMigration) SetConditions(conditions []metav1.Condition) {
	m.Status.Conditions = conditions
}
