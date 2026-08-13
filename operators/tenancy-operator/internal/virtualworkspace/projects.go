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
	"sort"
	"time"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"
	"go.platform-mesh.io/tenancy-operator/pkg/naming"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apiserver/pkg/registry/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var accountsResource = pmtenancyv1alpha1.Resource("projects")

// ClusterClientFunc returns a client scoped to one logical cluster.
//
// Projects live in Tenant workspaces, not in the directory, so serving them
// needs reach into a cluster named at request time. The VW holds ONE privileged
// credential and points it at the cluster the caller's own index says they belong
// to — the index is what bounds this, which is why resolveAccess runs first and
// its answer, never the request, decides which clusters are touched.
type ClusterClientFunc func(clusterID string) (ctrlruntimeclient.Client, error)

// ProjectStorage serves `projects`: the places the caller may actually work.
//
// The kcp Workspace behind each Project is never exposed. A client lists Projects
// and gets a cluster ID; it never names a Workspace, which is what keeps kcp
// concepts out of the tenant API.
type ProjectStorage struct {
	// directory reads the caller's membership index.
	directory ctrlruntimeclient.Client

	// clusterClient reaches one Tenant workspace.
	clusterClient ClusterClientFunc

	resolver *identity.Resolver

	// naming mints metadata.name on Create.
	naming naming.Strategy
}

var (
	_ rest.Storage              = &ProjectStorage{}
	_ rest.Scoper               = &ProjectStorage{}
	_ rest.SingularNameProvider = &ProjectStorage{}
	_ rest.Getter               = &ProjectStorage{}
	_ rest.Lister               = &ProjectStorage{}
	_ rest.TableConvertor       = &ProjectStorage{}
	_ rest.Creater              = &ProjectStorage{} //nolint:misspell // rest.Creater is the upstream interface name
)

// NewProjectStorage returns the `projects` REST storage.
func NewProjectStorage(directory ctrlruntimeclient.Client, clusterClient ClusterClientFunc, resolver *identity.Resolver, strategy naming.Strategy) *ProjectStorage {
	return &ProjectStorage{directory: directory, clusterClient: clusterClient, resolver: resolver, naming: strategy}
}

func (s *ProjectStorage) New() runtime.Object     { return &pmtenancyv1alpha1.Project{} }
func (s *ProjectStorage) NewList() runtime.Object { return &pmtenancyv1alpha1.ProjectList{} }
func (s *ProjectStorage) Destroy()                {}
func (s *ProjectStorage) NamespaceScoped() bool   { return false }
func (s *ProjectStorage) GetSingularName() string { return "project" }

// List returns every Project the caller may reach, across every Tenant they
// belong to.
//
// Deliberately cross-tenant: "where can I work" is the question a client
// asks after login, and answering it per-tenant would make the client fan out over
// tenants it had to discover first.
func (s *ProjectStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	_, view, err := resolveCallerAccess(ctx, s.directory)
	if err != nil {
		return nil, err
	}

	out := &pmtenancyv1alpha1.ProjectList{}
	for _, tenant := range view.Tenants {
		if tenant.ClusterID == "" {
			// The Tenant's workspace is not Ready yet; nothing to list there.
			continue
		}
		cl, err := s.clusterClient(tenant.ClusterID)
		if err != nil {
			// One unreachable Tenant must not fail the whole listing — the
			// others are still valid answers.
			continue
		}

		list := &pmtenancyv1alpha1.ProjectList{}
		if err := cl.List(ctx, list); err != nil {
			continue
		}
		for i := range list.Items {
			proj := &list.Items[i]
			if !tenant.CanSeeProject(proj.Name) {
				continue
			}
			stampTenant(proj, tenant.UUID)
			out.Items = append(out.Items, *proj)
		}
	}

	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Name < out.Items[j].Name })
	return out, nil
}

// Get returns one Project the caller may reach.
func (s *ProjectStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	_, view, err := resolveCallerAccess(ctx, s.directory)
	if err != nil {
		return nil, err
	}

	for _, tenant := range view.Tenants {
		if tenant.ClusterID == "" || !tenant.CanSeeProject(name) {
			// CanSeeProject is false for a tenant-scope membership only when this
			// tenant grants nothing; a tenant admin passes and the Get below decides
			// whether the Project is actually in THIS tenant.
			if !tenant.AllProjects {
				continue
			}
		}
		cl, err := s.clusterClient(tenant.ClusterID)
		if err != nil {
			continue
		}
		proj := &pmtenancyv1alpha1.Project{}
		if err := cl.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name}, proj); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		if !tenant.CanSeeProject(proj.Name) {
			continue
		}
		stampTenant(proj, tenant.UUID)
		return proj, nil
	}

	// 404 rather than 403, for the same reason tenants does it: a 403 would
	// confirm the Project exists somewhere the caller cannot see.
	return nil, apierrors.NewNotFound(accountsResource, name)
}

// ConvertToTable renders `kubectl get` output.
func (s *ProjectStorage) ConvertToTable(_ context.Context, object runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Display Name", Type: "string"},
			{Name: "Cluster", Type: "string"},
			{Name: "Ready", Type: "string"},
			{Name: "Age", Type: "date"},
		},
	}

	add := func(a *pmtenancyv1alpha1.Project) {
		ready := "Unknown"
		for _, c := range a.Status.Conditions {
			if c.Type == pmtenancyv1alpha1.ProjectConditionReady {
				ready = string(c.Status)
			}
		}
		table.Rows = append(table.Rows, metav1.TableRow{
			Cells: []any{a.Name, a.Spec.DisplayName, a.Status.ClusterID, ready,
				duration.HumanDuration(timeSince(a.CreationTimestamp))},
			Object: runtime.RawExtension{Object: a},
		})
	}

	switch t := object.(type) {
	case *pmtenancyv1alpha1.Project:
		add(t)
	case *pmtenancyv1alpha1.ProjectList:
		table.ResourceVersion = t.ResourceVersion
		for i := range t.Items {
			add(&t.Items[i])
		}
	default:
		return nil, fmt.Errorf("cannot render %T as a table", object)
	}
	return table, nil
}

// timeSince is split out so both storages format ages the same way.
func timeSince(t metav1.Time) time.Duration {
	if t.IsZero() {
		return 0
	}
	return time.Since(t.Time)
}

// stampTenant records which Tenant a Project came from.
//
// The Project does not carry this: inside its own workspace the answer is
// implicit. But this API lists across every Tenant the caller belongs to,
// and a flat list with no owner cannot be grouped or filtered.
//
// Stamped by the projection rather than stored, because it is derived — the
// authoritative answer is which workspace the object is in.
func stampTenant(proj *pmtenancyv1alpha1.Project, tenantUUID string) {
	if proj.Labels == nil {
		proj.Labels = map[string]string{}
	}
	proj.Labels[pmtenancyv1alpha1.LabelTenant] = tenantUUID
}

// Create makes a new Project inside a Tenant the caller belongs to.
//
// Which Tenant is named by the tenancy.platform-mesh.io/tenant label
// — the same one List stamps on every Project it returns, so what a client reads
// is what it writes back.
//
// metadata.name is server-assigned for the same reason it is on Tenants:
// the name is the workspace name, so a client choosing it would be choosing a
// path.
func (s *ProjectStorage) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	submitted, ok := obj.(*pmtenancyv1alpha1.Project)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Project, got %T", obj))
	}

	tenantUUID := submitted.Labels[pmtenancyv1alpha1.LabelTenant]
	if tenantUUID == "" {
		return nil, apierrors.NewBadRequest(fmt.Sprintf(
			"a Project must say which Tenant it belongs to: set the %s label",
			pmtenancyv1alpha1.LabelTenant))
	}

	_, view, err := resolveCallerAccess(ctx, s.directory)
	if err != nil {
		return nil, err
	}

	tenant, ok := view.Tenants[tenantUUID]
	if !ok {
		// Not a member. 404 rather than 403, so this cannot be used to discover
		// which Tenant UUIDs exist.
		return nil, apierrors.NewNotFound(organizationsResource, tenantUUID)
	}
	if tenant.ClusterID == "" {
		return nil, apierrors.NewServiceUnavailable(fmt.Sprintf(
			"tenant %s has no workspace yet; retry once it is Ready", tenantUUID))
	}

	// Who may create is the Tenant's own policy, not a platform-wide rule
	// (spec.projectCreation). Read the real object rather than trusting the
	// index, which carries the caller's role but not the tenant's settings.
	tenantObj := &pmtenancyv1alpha1.Tenant{}
	if err := s.directory.Get(ctx, ctrlruntimeclient.ObjectKey{Name: tenantUUID}, tenantObj); err != nil {
		return nil, err
	}
	// `members` means member-and-above, not "anyone holding a Membership": a
	// viewer must not create under either policy. Expressed as a floor on the rank
	// rather than a not-equal on admin, because the not-equal form only ever
	// rejected non-admins under the admin policy — leaving the default `members`
	// policy with nothing to reject, and a read-only grant able to create Projects.
	required := pmtenancyv1alpha1.MembershipRoleMember
	if tenantObj.Spec.ProjectCreation == pmtenancyv1alpha1.CreationAdmin {
		required = pmtenancyv1alpha1.MembershipRoleAdmin
	}
	if pmtenancyv1alpha1.MembershipRoleRank(tenant.Role) < pmtenancyv1alpha1.MembershipRoleRank(required) {
		return nil, apierrors.NewForbidden(accountsResource, "",
			fmt.Errorf("tenant %s requires the %s role or above to create projects", tenantUUID, required))
	}

	cl, err := s.clusterClient(tenant.ClusterID)
	if err != nil {
		return nil, err
	}

	proj := &pmtenancyv1alpha1.Project{
		Spec: pmtenancyv1alpha1.ProjectSpec{
			DisplayName: submitted.Spec.DisplayName,
		},
	}

	// Projects are named inside ONE Tenant's workspace, so a collision is
	// between two Projects of the same tenant — which is exactly the case where a
	// display-name strategy is defensible, and where the disambiguating suffix
	// lands in front of someone who can make sense of it.
	if _, err := naming.Apply(s.naming,
		naming.Request{Kind: naming.KindProject, DisplayName: submitted.Spec.DisplayName},
		func(name string) error {
			proj.Name = name
			return cl.Create(ctx, proj)
		},
		apierrors.IsAlreadyExists,
	); err != nil {
		return nil, err
	}

	stampTenant(proj, tenantUUID)
	return proj, nil
}
