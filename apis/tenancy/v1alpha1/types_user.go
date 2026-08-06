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
	// UserConditionReady is True once the User has been bootstrapped: a personal
	// Tenant exists (unless personal tenants are disabled) and the membership
	// index has been reconciled.
	UserConditionReady = "Ready"

	// UserConditionSeedTenantReady reports whether the personal Tenant has been
	// seeded for this User. Also True when spec.tenancy.seedTenant is false.
	// One-shot: it goes True once and deleting the Tenant does not re-seed.
	UserConditionSeedTenantReady = "SeedTenantReady"

	// UserConditionSeedProjectReady reports whether the first Project has been
	// seeded inside the personal Tenant. Also True when
	// spec.tenancy.seedProject is false. One-shot, like SeedTenantReady.
	UserConditionSeedProjectReady = "SeedProjectReady"

	// UserConditionRBACIdentityCurrent reports whether spec.rbacIdentity still
	// matches the username convention kcp is configured with. It can go False
	// when the platform's OIDC settings change.
	UserConditionRBACIdentityCurrent = "RBACIdentityCurrent"

	// UserConditionIndexSynced reports whether this User's UserMembershipIndex
	// reflects their current Memberships.
	UserConditionIndexSynced = "IndexSynced"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Email",type="string",JSONPath=".spec.email"
// +kubebuilder:printcolumn:name="Name",type="string",JSONPath=".spec.name"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// User is the record of an identity that has authenticated at least once.
//
// metadata.name is sha256(issuer + "\n" + sub), so self-provision is idempotent:
// two concurrent creates for one identity collide on AlreadyExists.
//
// A new identity provisions itself by creating its own User with an empty spec;
// the server fills every field from the verified token.
//
// A User is a record, not a credential and not an authorization input. Deleting
// it does not stop the token authenticating — kcp RBAC decides that.
// +k8s:openapi-gen=true
type User struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              UserSpec   `json:"spec,omitempty"`
	Status            UserStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// UserList is a list of User resources.
// +k8s:openapi-gen=true
type UserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []User `json:"items"`
}

// UserSpec defines the desired state of a User.
// +k8s:openapi-gen=true
type UserSpec struct {
	// Email is taken from the verified id_token. Informational only; RBACIdentity
	// is what kcp RBAC joins on.
	//
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Email string `json:"email,omitempty"`

	// Name is the human-facing display name from the id_token.
	//
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`

	// Issuer is the OIDC issuer that authenticated this identity.
	//
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Issuer string `json:"issuer,omitempty"`

	// Subject is the raw `sub` claim. Together with Issuer it is what
	// metadata.name hashes.
	//
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Subject string `json:"subject,omitempty"`

	// RBACIdentity is the username kcp sees after OIDC extraction, and the
	// subject every ClusterRoleBinding the controller writes will name.
	//
	// It mirrors kcp's own username configuration and must be computed from it,
	// never hardcoded. If the two disagree, bindings name a subject that never
	// authenticates and the user is silently denied.
	//
	// +optional
	// +kubebuilder:validation:MaxLength=253
	RBACIdentity string `json:"rbacIdentity,omitempty"`

	// TenantQuota overrides the platform default cap on how many Tenants this
	// User may create. 0 means use the platform default. Personal Tenants
	// do not count against it. Settable only by a platform admin.
	//
	// +optional
	// +kubebuilder:validation:Minimum=0
	TenantQuota int32 `json:"tenantQuota,omitempty"`

	// Tenancy says what the platform should provision for this identity.
	//
	// The whole object carries a default, not just the fields inside it:
	// structural defaulting only descends into objects that are present, so a
	// client omitting `tenancy` would otherwise get everything as false.
	//
	// +optional
	// +kubebuilder:default={seedTenant:true,seedProject:true}
	Tenancy UserTenancySpec `json:"tenancy,omitempty"`
}

// UserTenancySpec says what to bootstrap for one User. The operator's
// --tenancy-personal-tenants-enabled flag can turn seeding off fleet-wide, but
// never on for a User that asked for it to be off.
// +k8s:openapi-gen=true
type UserTenancySpec struct {
	// SeedTenant creates a personal Tenant for this User. When false, the User is
	// expected to be invited into someone else's Tenant instead.
	//
	// One-shot: it runs only while status.defaultTenant is empty. Deleting the
	// Tenant, or toggling this field, does not re-run it.
	//
	// +optional
	// +kubebuilder:default=true
	SeedTenant bool `json:"seedTenant,omitempty"`

	// SeedProject creates a first Project inside the personal Tenant, so a new
	// identity lands somewhere it can work.
	//
	// Ignored when SeedTenant is false — there is nowhere to put the Project.
	// One-shot on status.defaultProject, the same way SeedTenant is.
	//
	// This does not make that Project a default: nothing scopes requests to it.
	//
	// +optional
	// +kubebuilder:default=true
	SeedProject bool `json:"seedProject,omitempty"`
}

// UserStatus defines the observed state of a User.
// +k8s:openapi-gen=true
type UserStatus struct {
	// Active reports whether this identity may still use the platform's own
	// surfaces. Not an RBAC input.
	//
	// +optional
	Active bool `json:"active,omitempty"`

	// LastLogin is stamped by the tenancy virtual workspace on an authenticated
	// request.
	//
	// +optional
	LastLogin *metav1.Time `json:"lastLogin,omitempty"`

	// DefaultTenant is the UUID of the Tenant seeded for this User at bootstrap.
	//
	// It records that seeding happened; it is never cleared and never re-seeded
	// if the User deletes that Tenant. "Default" means what was seeded, not
	// somewhere requests go — it is not an access-control or request-scoping
	// input.
	//
	// +optional
	DefaultTenant string `json:"defaultTenant,omitempty"`

	// DefaultProject is the UUID of the first Project seeded inside
	// DefaultTenant. Same meaning as DefaultTenant: a one-time record.
	//
	// +optional
	DefaultProject string `json:"defaultProject,omitempty"`

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
func (in *User) GetConditions() []metav1.Condition { return in.Status.Conditions }

// SetConditions implements conditions.ConditionAccessor.
func (in *User) SetConditions(conditions []metav1.Condition) { in.Status.Conditions = conditions }
