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

package virtualworkspace

import (
	"context"
	"fmt"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"
	"go.platform-mesh.io/tenancy-operator/pkg/naming"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	reststorage "k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/fixedgvs"
	virtualrootapiserver "github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"
)

// tenancyVirtualWorkspace adds admission to the fixed-GVs base, which supplies
// only routing, authorization and readiness.
type tenancyVirtualWorkspace struct {
	*fixedgvs.FixedGroupVersionsVirtualWorkspace
}

// Handles implements admission.Interface.
func (v *tenancyVirtualWorkspace) Handles(admission.Operation) bool { return true }

// Admit implements admission.Mutator.
//
// Empty, and it stays empty: server-owned fields are stamped by each storage's
// Create, because they come from the caller's credential (which admission cannot
// see) and from a naming strategy that retries against the API server (which
// admission cannot do).
func (v *tenancyVirtualWorkspace) Admit(context.Context, admission.Attributes, admission.ObjectInterfaces) error {
	return nil
}

// Validate implements admission.Validator.
//
// Empty for now. What belongs here is policy that is not per-row: the quota caps
// (configured and carried on objects, but enforced nowhere today), the sole-admin
// block and the tenant-membership-removal block. Here rather than in a storage
// method, so a `kubectl delete` cannot bypass a check written into Create.
func (v *tenancyVirtualWorkspace) Validate(context.Context, admission.Attributes, admission.ObjectInterfaces) error {
	return nil
}

// Name identifies this virtual workspace to the framework.
const Name = "tenancy"

// Options configures the virtual workspace.
type Options struct {
	// Prefix is the mount point, e.g. /services/tenancy. Configurable so two
	// installs on one kcp do not collide.
	Prefix string

	// DirectoryClient writes User / Tenant / UserMembershipIndex in the
	// directory workspace. The only identity with access to the tenant tier, which
	// is why the authorizer is the thing worth auditing.
	DirectoryClient ctrlruntimeclient.Client

	// ClusterClient reaches one Tenant workspace, where Projects live.
	// Which cluster is decided by the caller's membership index, never by the
	// request — see ProjectStorage.
	ClusterClient ClusterClientFunc

	// Resolver mirrors kcp's username convention.
	Resolver *identity.Resolver

	// Naming mints the server-assigned names of Tenants and Projects.
	// Required: defaulting silently would hand UUIDs to an operator who configured
	// something else, with no way to tell.
	Naming naming.Strategy
}

// New builds the tenancy virtual workspace.
//
// fixedgvs rather than a dynamic API set: the resources are fixed, and each
// storage is a projection over real objects rather than a new database. Which
// export a resource comes from is invisible to the client.
func New(opts Options) (virtualrootapiserver.NamedVirtualWorkspace, error) {
	if opts.DirectoryClient == nil {
		return virtualrootapiserver.NamedVirtualWorkspace{}, fmt.Errorf("a directory client is required")
	}
	if opts.ClusterClient == nil {
		return virtualrootapiserver.NamedVirtualWorkspace{}, fmt.Errorf("a cluster client factory is required: `projects` live in Tenant workspaces")
	}
	if opts.Resolver == nil {
		return virtualrootapiserver.NamedVirtualWorkspace{}, fmt.Errorf("an identity resolver is required")
	}
	if opts.Naming == nil {
		return virtualrootapiserver.NamedVirtualWorkspace{}, fmt.Errorf("a naming strategy is required")
	}

	return virtualrootapiserver.NamedVirtualWorkspace{
		Name: Name,
		VirtualWorkspace: &tenancyVirtualWorkspace{FixedGroupVersionsVirtualWorkspace: &fixedgvs.FixedGroupVersionsVirtualWorkspace{
			RootPathResolver: NewRootPathResolver(opts.Prefix),
			Authorizer:       NewAuthorizer(),
			ReadyChecker:     framework.ReadyFunc(func() error { return nil }),
			GroupVersionAPISets: []fixedgvs.GroupVersionAPISet{
				{
					GroupVersion: pmtenancyv1alpha1.SchemeGroupVersion,
					AddToScheme:  pmtenancyv1alpha1.AddToScheme,
					// The delegated API server resolves a model for every type it
					// serves and refuses to start without one. This field — not the
					// root config's — is what it reads, so the definitions
					// generated by `task generate-apis` have to be handed over here.
					OpenAPIDefinitions: pmtenancyv1alpha1.GetOpenAPIDefinitions,
					BootstrapRestResources: func(genericapiserver.CompletedConfig) (map[string]fixedgvs.RestStorageBuilder, error) {
						return map[string]fixedgvs.RestStorageBuilder{
							// Four resources, deliberately: who I am, which
							// Tenants I belong to, which Projects I can work
							// in, and who else belongs. None of it exposes a kcp
							// Workspace.
							//
							// Each is filtered by the caller's membership index
							// rather than authorized-or-403, so a caller with no
							// memberships gets empty lists rather than an error.
							"users": func(genericapiserver.CompletedConfig) (reststorage.Storage, error) {
								return NewUserStorage(opts.DirectoryClient, opts.Resolver), nil
							},
							"tenants": func(genericapiserver.CompletedConfig) (reststorage.Storage, error) {
								return NewTenantStorage(opts.DirectoryClient, opts.Resolver, opts.Naming), nil
							},
							"projects": func(genericapiserver.CompletedConfig) (reststorage.Storage, error) {
								return NewProjectStorage(opts.DirectoryClient, opts.ClusterClient, opts.Resolver, opts.Naming), nil
							},
							// The ONLY path by which a tenant manages access: no
							// identity of theirs has a role binding in the
							// Tenant workspace where Memberships live, so
							// there is no kubectl fallback and the guards in this
							// storage are enforceable rather than advisory.
							"memberships": func(genericapiserver.CompletedConfig) (reststorage.Storage, error) {
								return NewMembershipStorage(opts.DirectoryClient, opts.ClusterClient, opts.Resolver), nil
							},
						}, nil
					},
				},
			},
		}},
	}, nil
}

// NewAuthorizer admits any authenticated caller and leaves the rest to the
// storage.
//
// That division is deliberate, not an unfinished authorizer: every resource here
// is scoped by the caller's own identity, and none can be widened by naming
// something in the request. The storage holds the membership index and can answer
// per row; an authorizer cannot.
//
// Failure closed: an unresolvable identity is denied, never left unfiltered.
func NewAuthorizer() authorizer.Authorizer {
	return authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
		u := a.GetUser()
		if u == nil {
			return authorizer.DecisionDeny, "no authenticated user", nil
		}
		for _, g := range u.GetGroups() {
			if g == "system:authenticated" {
				return authorizer.DecisionAllow, "authenticated caller acting on itself", nil
			}
		}
		return authorizer.DecisionDeny, "caller is not authenticated", nil
	})
}

var _ runtime.Object = &pmtenancyv1alpha1.User{}
