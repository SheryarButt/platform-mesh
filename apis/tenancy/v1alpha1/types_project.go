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
)

const (
	// ProjectConditionReady is True once the Project is usable: its workspace
	// exists and is Ready.
	ProjectConditionReady = "Ready"

	// ProjectConditionWorkspaceReady reports whether the kcp Workspace backing
	// this Project exists and has finished initializing.
	ProjectConditionWorkspaceReady = "WorkspaceReady"

	// ProjectConditionMembershipsPruned reports whether the project-scope
	// Memberships naming this Project have been removed. Only set while the
	// Project is being deleted.
	ProjectConditionMembershipsPruned = "MembershipsPruned"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="DisplayName",type="string",JSONPath=".spec.displayName"
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".status.clusterID"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Project is where tenant work happens. It is the tenant-facing handle for a
// kcp Workspace; the Workspace object itself is never exposed.
//
// Projects are a flat list under their Tenant — there are no sub-projects.
// Access to one Project grants nothing anywhere else. Grouping is left to a
// future Folder kind between Tenant and Project, so Project stays a leaf.
//
// metadata.name is a server-assigned UUID and is also the workspace name, so
// renaming (spec.displayName) never moves a path.
// +k8s:openapi-gen=true
type Project struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ProjectSpec   `json:"spec,omitempty"`
	Status            ProjectStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ProjectList is a list of Project resources.
// +k8s:openapi-gen=true
type ProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Project `json:"items"`
}

// ProjectSpec defines the desired state of a Project.
// +k8s:openapi-gen=true
type ProjectSpec struct {
	// DisplayName is the human-facing label. Not unique, and changing it never
	// moves the workspace path.
	//
	// +optional
	// +kubebuilder:validation:MaxLength=253
	DisplayName string `json:"displayName,omitempty"`
}

// ProjectStatus defines the observed state of a Project.
// +k8s:openapi-gen=true
type ProjectStatus struct {
	// ClusterID is the kcp logical cluster of this Project's workspace. Clients
	// address the workspace by cluster ID, not by path.
	//
	// +optional
	ClusterID string `json:"clusterID,omitempty"`

	// WorkspacePath is the full kcp path of this Project's workspace.
	//
	// +optional
	WorkspacePath string `json:"workspacePath,omitempty"`

	// Conditions describe the current state.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// GetConditions implements conditions.ConditionAccessor.
func (in *Project) GetConditions() []metav1.Condition { return in.Status.Conditions }

// SetConditions implements conditions.ConditionAccessor.
func (in *Project) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}
