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
// +kubebuilder:resource:scope=Cluster,shortName=gmi
// +kubebuilder:printcolumn:name="Group",type="string",JSONPath=".spec.group"
// +kubebuilder:printcolumn:name="Entries",type="integer",JSONPath=".status.entryCount"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// GroupMembershipIndex is the group-subject half of the read model: the Tenants
// and Projects one GROUP has been granted. One exists per group that holds at
// least one Membership.
//
// It answers the question a caller's token asks — "I hold these groups, what do
// they reach" — without fanning out over the fleet, which is the same job
// UserMembershipIndex does for a person. What it deliberately does NOT do is
// answer the reverse: there is no list of who is in a group, because the platform
// does not know and cannot be told. Group membership arrives in a token and
// nowhere else.
//
// Why this and not rows on each member's UserMembershipIndex: a group grant would
// then have to be materialized onto every member, which needs a list of members
// the platform does not have, and would leave rows behind for anyone who left the
// group until they next signed in — access that outlives its revocation. Indexing
// BY GROUP means the caller's own live token supplies the membership on every
// request, so removal at the identity provider takes effect on the next token
// with nothing to clean up.
// +k8s:openapi-gen=true
type GroupMembershipIndex struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              GroupMembershipIndexSpec   `json:"spec,omitempty"`
	Status            GroupMembershipIndexStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// GroupMembershipIndexList is a list of GroupMembershipIndex resources.
// +k8s:openapi-gen=true
type GroupMembershipIndexList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GroupMembershipIndex `json:"items"`
}

// GroupMembershipIndexSpec defines the desired state of a GroupMembershipIndex.
// +k8s:openapi-gen=true
type GroupMembershipIndexSpec struct {
	// Group is the group this index is for, as the identity provider emits it.
	//
	// Carried in the spec because metadata.name CANNOT hold it: a group name is
	// whatever the issuer says, and `team-a/admins` — a shape real providers emit —
	// is not a legal object name. The name is a digest of this value, so this field
	// is the only readable statement of which group the object is about.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Group string `json:"group"`

	// Entries carries one row per Membership granted to this group.
	//
	// +optional
	// +listType=atomic
	Entries []MembershipIndexEntry `json:"entries,omitempty"`
}

// GroupMembershipIndexStatus defines the observed state of a
// GroupMembershipIndex.
// +k8s:openapi-gen=true
type GroupMembershipIndexStatus struct {
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
