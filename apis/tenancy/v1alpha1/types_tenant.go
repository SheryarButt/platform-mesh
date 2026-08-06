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

// Who may create things inside a Tenant.
const (
	// CreationMembers lets any Tenant member create.
	CreationMembers = "members"
	// CreationAdmin restricts creation to Tenant admins.
	CreationAdmin = "admin"
)

// Conditions on a Tenant. Together they track bootstrap progress.
const (
	// TenantConditionReady is True once every step below has succeeded.
	TenantConditionReady = "Ready"

	// TenantConditionWorkspaceReady reports whether the kcp Workspace at
	// status.workspacePath has been provisioned.
	TenantConditionWorkspaceReady = "WorkspaceReady"

	// TenantConditionMembershipReady reports whether the admin Membership
	// for the Tenant's first admin has been written into the Tenant
	// workspace.
	TenantConditionMembershipReady = "MembershipReady"

	// TenantConditionWorkspaceCreated reports whether the first child
	// Workspace has been created under this Tenant.
	TenantConditionWorkspaceCreated = "WorkspaceCreated"

	// TenantConditionNamespaceReady reports whether the `default` namespace
	// exists inside that child Workspace. The controller creates it, since the
	// `project` WorkspaceType does not extend root:universal.
	TenantConditionNamespaceReady = "NamespaceReady"

	// TenantConditionWorkspaceAdminReady reports whether the first admin's
	// ClusterRoleBinding has been written inside the child Workspace.
	TenantConditionWorkspaceAdminReady = "WorkspaceAdminReady"

	// TenantConditionProfileReady reports whether the deployment profile's
	// bootstrap objects (extra bindings, a starter provider) have been applied.
	// Absent when the profile defines none.
	TenantConditionProfileReady = "ProfileReady"

	// TenantConditionOwnerMembershipReady reports whether the first admin's
	// tenant-scope Membership exists. Without it nothing writes a role binding
	// and the owner cannot reach their own Tenant.
	TenantConditionOwnerMembershipReady = "OwnerMembershipReady"

	// TenantConditionIndexSynced reports whether the owning User's
	// UserMembershipIndex carries a row for this Tenant.
	TenantConditionIndexSynced = "IndexSynced"
)

// Condition reasons.
const (
	// ReasonAwaitingWorkspaceType marks a Tenant whose kcp Workspace has
	// not been created because the `tenant` WorkspaceType is not registered
	// or not Ready yet.
	ReasonAwaitingWorkspaceType = "AwaitingWorkspaceType"

	// ReasonAwaitingWorkspace marks a step that is waiting on the Tenant's
	// own kcp Workspace to become Ready before it can write into it.
	ReasonAwaitingWorkspace = "AwaitingWorkspace"

	// ReasonProvisioning marks a step that is still in progress.
	ReasonProvisioning = "Provisioning"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=tenant
// +kubebuilder:printcolumn:name="DisplayName",type="string",JSONPath=".spec.displayName"
// +kubebuilder:printcolumn:name="Personal",type="boolean",JSONPath=".spec.personal"
// +kubebuilder:printcolumn:name="Workspace",type="string",JSONPath=".status.workspacePath"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Tenant is the membership and billing boundary. It owns a kcp workspace of
// type `tenant` holding this Tenant's Memberships; actual work happens in the
// Projects beneath it.
//
// metadata.name is server-assigned and is also the workspace name;
// spec.displayName is metadata only and is not unique.
// +k8s:openapi-gen=true
type Tenant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TenantSpec   `json:"spec,omitempty"`
	Status            TenantStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// TenantList is a list of Tenant resources.
// +k8s:openapi-gen=true
type TenantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Tenant `json:"items"`
}

// TenantSpec defines the desired state of a Tenant.
// +k8s:openapi-gen=true
type TenantSpec struct {
	// DisplayName is the human-facing label. Not unique, and changing it never
	// moves the workspace path.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName"`

	// Personal marks the Tenant auto-created for a single User at bootstrap.
	// Immutable, and not counted against the per-User tenant quota.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="personal is immutable"
	Personal bool `json:"personal,omitempty"`

	// ProjectCreation controls who may create Projects.
	//
	// +optional
	// +kubebuilder:default=members
	// +kubebuilder:validation:Enum=members;admin
	ProjectCreation string `json:"projectCreation,omitempty"`

	// ProvidersMetadataCreation controls who may publish Tenant-private
	// provider metadata. Same enum and default as ProjectCreation.
	//
	// +optional
	// +kubebuilder:default=members
	// +kubebuilder:validation:Enum=members;admin
	ProvidersMetadataCreation string `json:"providersMetadataCreation,omitempty"`

	// ProjectQuota caps the number of Projects. 0 means use the platform default.
	//
	// +optional
	// +kubebuilder:validation:Minimum=0
	ProjectQuota int32 `json:"projectQuota,omitempty"`
}

// TenantStatus defines the observed state of a Tenant.
// +k8s:openapi-gen=true
type TenantStatus struct {
	// WorkspacePath is the path of the materialized kcp Workspace —
	// <tenant-fleet-root>:<metadata.name>. Set once the workspace exists.
	//
	// +optional
	WorkspacePath string `json:"workspacePath,omitempty"`

	// ClusterID is the kcp logical cluster behind WorkspacePath.
	//
	// +optional
	ClusterID string `json:"clusterID,omitempty"`

	// FirstAdmin is the User that was given the first admin Membership.
	//
	// +optional
	FirstAdmin string `json:"firstAdmin,omitempty"`

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
func (in *Tenant) GetConditions() []metav1.Condition { return in.Status.Conditions }

// SetConditions implements conditions.ConditionAccessor.
func (in *Tenant) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}
