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
	// MembershipScopeTenant grants access to a whole Tenant.
	MembershipScopeTenant = "tenant"

	// MembershipScopeFolder is reserved for a future Folder tier between Tenant
	// and Project. Not implemented.
	//
	// MembershipScopeFolder = "folder"

	// MembershipScopeProject grants access to a single Project. Access does not
	// inherit to other Projects.
	MembershipScopeProject = "project"

	// MembershipRoleAdmin grants full administrative access in the target.
	MembershipRoleAdmin = "admin"

	// MembershipRoleMember grants ordinary access. Members cannot manage
	// Memberships.
	MembershipRoleMember = "member"

	// MembershipRoleViewer grants read-only access.
	MembershipRoleViewer = "viewer"
)

// ClusterRoles the controller binds a Membership to inside a child Workspace.
// Bootstrapped by the platform.
const (
	// ClusterRoleProjectAdmin is the role bound for MembershipRoleAdmin.
	ClusterRoleProjectAdmin = "platform:project:admin"

	// ClusterRoleProjectMember is the role bound for MembershipRoleMember.
	ClusterRoleProjectMember = "platform:project:member"

	// ClusterRoleProjectViewer is the role bound for MembershipRoleViewer.
	ClusterRoleProjectViewer = "platform:project:viewer"
)

// MembershipRoleRank orders roles from least to most privileged, so two grants
// can be resolved to the stronger one. Unknown roles rank below viewer.
func MembershipRoleRank(role string) int {
	switch role {
	case MembershipRoleAdmin:
		return 3
	case MembershipRoleMember:
		return 2
	case MembershipRoleViewer:
		return 1
	default:
		return 0
	}
}

const (
	// MembershipConditionReady is True once RBAC is written and the index is
	// updated.
	MembershipConditionReady = "Ready"

	// MembershipConditionRBACApplied reports whether the role binding for this
	// Membership has been written in the target workspace.
	MembershipConditionRBACApplied = "RBACApplied"

	// MembershipConditionIndexSynced reports whether the subject User's
	// UserMembershipIndex carries a row for this Membership.
	MembershipConditionIndexSynced = "IndexSynced"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="User",type="string",JSONPath=".spec.user"
// +kubebuilder:printcolumn:name="Scope",type="string",JSONPath=".spec.scope"
// +kubebuilder:printcolumn:name="Project",type="string",JSONPath=".spec.project"
// +kubebuilder:printcolumn:name="Role",type="string",JSONPath=".spec.role"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Membership grants a User a role in a Tenant or a Project. The controller
// turns it into kcp RBAC.
//
// Memberships always live in the Tenant workspace, whatever their scope. Because
// of that, deleting a Project does not remove its Memberships — the Project
// reconciler prunes them explicitly. Deleting the Tenant cascades everything.
// +k8s:openapi-gen=true
type Membership struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MembershipSpec   `json:"spec,omitempty"`
	Status            MembershipStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MembershipList is a list of Membership resources.
// +k8s:openapi-gen=true
type MembershipList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Membership `json:"items"`
}

// MembershipSpec defines the desired state of a Membership.
// +k8s:openapi-gen=true
type MembershipSpec struct {
	// User is the metadata.name of a User in the directory workspace.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	User string `json:"user"`

	// Scope selects what this Membership grants access to.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=tenant;project
	Scope string `json:"scope"`

	// Project is the UUID of the target Project. Required when scope=project, and
	// must be empty otherwise.
	//
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Project string `json:"project,omitempty"`

	// Role is the privilege level granted in the target.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=admin;member;viewer
	Role string `json:"role"`
}

// MembershipStatus defines the observed state of a Membership.
// +k8s:openapi-gen=true
type MembershipStatus struct {
	// ClusterID is the kcp logical cluster the RBAC for this Membership was
	// written into.
	//
	// +optional
	ClusterID string `json:"clusterID,omitempty"`

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
func (in *Membership) GetConditions() []metav1.Condition { return in.Status.Conditions }

// SetConditions implements conditions.ConditionAccessor.
func (in *Membership) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}
