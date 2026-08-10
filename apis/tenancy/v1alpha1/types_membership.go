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
// +kubebuilder:printcolumn:name="Group",type="string",JSONPath=".spec.group"
// +kubebuilder:printcolumn:name="Scope",type="string",JSONPath=".spec.scope"
// +kubebuilder:printcolumn:name="Project",type="string",JSONPath=".spec.project"
// +kubebuilder:printcolumn:name="Role",type="string",JSONPath=".spec.role"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Membership grants a role in a Tenant or a Project to a subject — a User, or
// everyone in an identity-provider group. The controller turns it into kcp RBAC.
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
//
// Exactly one of user or group is set — the subject of the grant. They are two
// fields rather than one polymorphic one because they are not interchangeable:
// `user` names an object the platform holds and can verify, `group` names a claim
// the identity provider makes and the platform can only take on trust. Code that
// means "the person" should say user and mean it.
//
// +kubebuilder:validation:XValidation:rule="has(self.user) != has(self.group)",message="exactly one of spec.user or spec.group must be set"
// +k8s:openapi-gen=true
type MembershipSpec struct {
	// User is the metadata.name of a User in the directory workspace.
	//
	// Mutually exclusive with Group.
	//
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	User string `json:"user,omitempty"`

	// Group grants to everyone whose token carries this group, present and
	// future. The value is the group as the IDENTITY PROVIDER emits it, without
	// the prefix kcp applies — the prefix is deployment configuration, and a
	// Membership that hardcoded it would stop matching the day it changed.
	//
	// UNVERIFIABLE, and that is the trade. There is no object to check it against,
	// so a typo is a Membership that grants nobody anything while reporting Ready,
	// and there is no way to enumerate who holds it. In exchange it grants people
	// who have never signed in, which no user-subject Membership can do.
	//
	// Revocation belongs to the identity provider. Removing someone from the group
	// there stops their next token carrying it, and kcp stops admitting them.
	// Deleting this Membership revokes it for EVERYONE.
	//
	// Mutually exclusive with User.
	//
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Group string `json:"group,omitempty"`

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

// SubjectKind reports what this Membership grants to, using the kube RBAC
// subject kinds so a caller can hand the answer straight to a RoleRef.
//
// An empty spec reports SubjectKindUser, which is what the CRD's own validation
// already rejects — the zero value should not invent a third kind for a state
// that cannot be stored.
func (in *Membership) SubjectKind() string {
	if in.Spec.Group != "" {
		return SubjectKindGroup
	}
	return SubjectKindUser
}

// SubjectName is the subject's name in whichever kind it is: a User's digest
// name, or the group as the identity provider emits it.
//
// NOT an RBAC subject name. A binding names the group WITH the configured prefix
// and a user by their rbacIdentity, and both of those are resolved from
// deployment configuration this package knows nothing about.
func (in *Membership) SubjectName() string {
	if in.Spec.Group != "" {
		return in.Spec.Group
	}
	return in.Spec.User
}

// Subject kinds, spelled as kube's RBAC subject kinds are.
const (
	SubjectKindUser  = "User"
	SubjectKindGroup = "Group"
)

// GetConditions implements conditions.ConditionAccessor.
func (in *Membership) GetConditions() []metav1.Condition { return in.Status.Conditions }

// SetConditions implements conditions.ConditionAccessor.
func (in *Membership) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}
