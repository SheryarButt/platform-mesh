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

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=umi
// +kubebuilder:printcolumn:name="Entries",type="integer",JSONPath=".status.entryCount"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// UserMembershipIndex is a flat list of the Tenants and Projects one User
// belongs to. One exists per User, sharing its metadata.name.
//
// It is a read model, so a client can list its tenants and projects without
// fanning out over the fleet. It is not consulted when a tenant talks to their
// own workspace — kcp RBAC decides that. If the two disagree, RBAC is right and
// the index needs rebuilding.
// +k8s:openapi-gen=true
type UserMembershipIndex struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              UserMembershipIndexSpec   `json:"spec,omitempty"`
	Status            UserMembershipIndexStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// UserMembershipIndexList is a list of UserMembershipIndex resources.
// +k8s:openapi-gen=true
type UserMembershipIndexList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UserMembershipIndex `json:"items"`
}

// UserMembershipIndexSpec defines the desired state of a UserMembershipIndex.
// +k8s:openapi-gen=true
type UserMembershipIndexSpec struct {
	// Entries carries one row per Membership the User holds, across every Tenant
	// and Project.
	//
	// +optional
	// +listType=atomic
	Entries []MembershipIndexEntry `json:"entries,omitempty"`
}

// UserMembershipIndexStatus defines the observed state of a UserMembershipIndex.
// +k8s:openapi-gen=true
type UserMembershipIndexStatus struct {
	// EntryCount mirrors len(spec.entries), so `kubectl get` can print it.
	//
	// +optional
	EntryCount int32 `json:"entryCount,omitempty"`

	// ObservedGeneration is the generation of this object the controller last
	// reconciled.
	//
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// MembershipIndexEntry is one Membership the user holds, with enough Tenant and
// Project metadata for a client to render it without extra lookups.
// +k8s:openapi-gen=true
type MembershipIndexEntry struct {
	// TenantUUID is the Tenant's metadata.name.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	TenantUUID string `json:"tenantUUID"`

	// TenantDisplayName mirrors Tenant.spec.displayName, re-synced when it
	// changes.
	//
	// +optional
	TenantDisplayName string `json:"tenantDisplayName,omitempty"`

	// TenantCreatedAt mirrors the Tenant's creationTimestamp.
	//
	// +optional
	TenantCreatedAt metav1.Time `json:"tenantCreatedAt,omitempty"`

	// TenantFirstAdmin is the User that held the Tenant's first admin
	// Membership.
	//
	// +optional
	TenantFirstAdmin string `json:"tenantFirstAdmin,omitempty"`

	// TenantClusterID is the Tenant workspace's kcp logical cluster.
	//
	// +optional
	TenantClusterID string `json:"tenantClusterID,omitempty"`

	// ProjectUUID is set on project-scope rows and empty on tenant-scope rows.
	//
	// +optional
	ProjectUUID string `json:"projectUUID,omitempty"`

	// ProjectDisplayName mirrors the display name annotation on the Project's
	// kcp Workspace. Empty on tenant-scope rows.
	//
	// +optional
	ProjectDisplayName string `json:"projectDisplayName,omitempty"`

	// ProjectClusterID is the Project workspace's kcp logical cluster, which is
	// what a kubeconfig server URL points at. Empty on tenant-scope rows.
	//
	// +optional
	ProjectClusterID string `json:"projectClusterID,omitempty"`

	// Role is the granted role.
	//
	// +kubebuilder:validation:Enum=admin;member;viewer
	Role string `json:"role"`

	// Personal mirrors Tenant.spec.personal.
	//
	// +optional
	Personal bool `json:"personal,omitempty"`
}
