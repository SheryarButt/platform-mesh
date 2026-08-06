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

// Package v1alpha1 contains API Schema definitions for the tenancy v1alpha1 API
// group.
//
// The group is served from two places:
//
//   - User, Tenant and UserMembershipIndex are platform-global. They live in the
//     directory workspace and are exported by `tenancy-platform`, which is never
//     bound in a tenant tier.
//   - Membership and Project are tenant-facing. They live in the Tenant
//     workspace and are exported by `tenancy`, which every Tenant binds.
//
// Binding one export must not make the other's objects servable.
//
// +kubebuilder:object:generate=true
// +k8s:openapi-gen=true
// +groupName=tenancy.platform-mesh.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// GroupName is the API group used to register these objects.
	GroupName = "tenancy.platform-mesh.io"

	// GroupVersion is the group version used to register these objects.
	GroupVersion = "v1alpha1"
)

const (
	// AnnotationDisplayName carries the human-facing label on objects the
	// platform does not own the schema of, such as kcp Workspaces and
	// ServiceAccounts. Our own types use a spec field instead.
	AnnotationDisplayName = GroupName + "/display-name"

	// AnnotationSeededFor names the User an object was seeded for. An annotation
	// rather than a label because a User's name is a 64-character digest and
	// label values cap at 63 bytes — see LabelUser below.
	//
	// It is written in the same request that creates the object, so a reconciler
	// finding the name already taken can tell its own earlier attempt from
	// somebody else's object.
	AnnotationSeededFor = GroupName + "/seeded-for"

	// AnnotationRole records the role a ServiceAccount was created with.
	AnnotationRole = GroupName + "/role"

	// LabelServiceAccount marks the ServiceAccounts the tenancy VW projects as
	// bot identities, so they can be listed without reading every SA.
	LabelServiceAccount = GroupName + "/service-account"

	// LabelTenant records the owning Tenant UUID on objects that live
	// outside the Tenant workspace.
	LabelTenant = GroupName + "/tenant"

	// LabelUser is reserved and currently unused.
	//
	// It must not be set to a User's name: that is a 64-character sha256 digest
	// and label values cap at 63 bytes, so the API server rejects the object.
	// Derived objects point back with a status field, or are named for their
	// User as UserMembershipIndex is.
	LabelUser = GroupName + "/user"
)

var (
	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme

	// SchemeGroupVersion is group version used to register these objects.
	SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: GroupVersion}
)

// Resource takes an unqualified resource and returns a Group-qualified GroupResource.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

// addKnownTypes adds the list of known types to the scheme.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&Project{},
		&ProjectList{},
		&User{},
		&UserList{},
		&Tenant{},
		&TenantList{},
		&Membership{},
		&MembershipList{},
		&UserMembershipIndex{},
		&UserMembershipIndexList{},
	)

	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
