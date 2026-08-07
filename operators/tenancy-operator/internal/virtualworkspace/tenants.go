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

var organizationsResource = pmtenancyv1alpha1.Resource("tenants")

// TenantStorage serves `tenants`: the ones the caller belongs to.
//
// A projection over the real Tenant objects in the directory workspace,
// filtered by the caller's membership index. Never a directory of the platform's
// tenants: a Tenant the caller has no Membership in is invisible, and a
// GET of it is a 404 rather than a 403 — a 403 would confirm it exists.
type TenantStorage struct {
	client   ctrlruntimeclient.Client
	resolver *identity.Resolver

	// naming mints metadata.name on Create. Held rather than called globally so a
	// test can pin a strategy, and so the choice is visible in the constructor
	// signature instead of hidden in package state.
	naming naming.Strategy
}

var (
	_ rest.Storage              = &TenantStorage{}
	_ rest.Scoper               = &TenantStorage{}
	_ rest.SingularNameProvider = &TenantStorage{}
	_ rest.Getter               = &TenantStorage{}
	_ rest.Lister               = &TenantStorage{}
	_ rest.TableConvertor       = &TenantStorage{}
	_ rest.Creater              = &TenantStorage{} //nolint:misspell // rest.Creater is the upstream interface name
)

// NewTenantStorage returns the `tenants` REST storage.
func NewTenantStorage(client ctrlruntimeclient.Client, resolver *identity.Resolver, strategy naming.Strategy) *TenantStorage {
	return &TenantStorage{client: client, resolver: resolver, naming: strategy}
}

func (s *TenantStorage) New() runtime.Object     { return &pmtenancyv1alpha1.Tenant{} }
func (s *TenantStorage) NewList() runtime.Object { return &pmtenancyv1alpha1.TenantList{} }
func (s *TenantStorage) Destroy()                {}
func (s *TenantStorage) NamespaceScoped() bool   { return false }
func (s *TenantStorage) GetSingularName() string { return "tenant" }

// List returns the Tenants the caller belongs to.
//
// Filtered rather than authorized-or-403: a caller with no memberships gets an
// empty list, which is the normal first-run state and not an error.
func (s *TenantStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	_, view, err := resolveCallerAccess(ctx, s.client)
	if err != nil {
		return nil, err
	}

	out := &pmtenancyv1alpha1.TenantList{}
	for uuid := range view.Tenants {
		tenant := &pmtenancyv1alpha1.Tenant{}
		if err := s.client.Get(ctx, ctrlruntimeclient.ObjectKey{Name: uuid}, tenant); err != nil {
			if apierrors.IsNotFound(err) {
				// The index outlived the Tenant. RBAC and the objects are the
				// truth; the index is what is stale, so skip rather than fail the
				// whole list for one row.
				continue
			}
			return nil, err
		}
		out.Items = append(out.Items, *tenant)
	}

	// Stable order: an unstable list makes a client's diff meaningless.
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Name < out.Items[j].Name })
	return out, nil
}

// Get returns one Tenant the caller belongs to.
func (s *TenantStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	_, view, err := resolveCallerAccess(ctx, s.client)
	if err != nil {
		return nil, err
	}
	if _, ok := view.Tenants[name]; !ok {
		// 404, not 403: see the type comment.
		return nil, apierrors.NewNotFound(organizationsResource, name)
	}

	tenant := &pmtenancyv1alpha1.Tenant{}
	if err := s.client.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name}, tenant); err != nil {
		return nil, err
	}
	return tenant, nil
}

// ConvertToTable renders `kubectl get` output.
func (s *TenantStorage) ConvertToTable(_ context.Context, object runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name"},
			{Name: "Display Name", Type: "string"},
			{Name: "Personal", Type: "boolean"},
			{Name: "Cluster", Type: "string"},
			{Name: "Age", Type: "date"},
		},
	}

	add := func(o *pmtenancyv1alpha1.Tenant) {
		table.Rows = append(table.Rows, metav1.TableRow{
			Cells: []any{o.Name, o.Spec.DisplayName, o.Spec.Personal, o.Status.ClusterID,
				duration.HumanDuration(timeSince(o.CreationTimestamp))},
			Object: runtime.RawExtension{Object: o},
		})
	}

	switch t := object.(type) {
	case *pmtenancyv1alpha1.Tenant:
		add(t)
	case *pmtenancyv1alpha1.TenantList:
		table.ResourceVersion = t.ResourceVersion
		for i := range t.Items {
			add(&t.Items[i])
		}
	default:
		return nil, fmt.Errorf("cannot render %T as a table", object)
	}
	return table, nil
}

// callerName resolves the authenticated caller to their User name.
func (s *TenantStorage) callerName(ctx context.Context) (string, error) {
	claims, err := claimsFrom(ctx)
	if err != nil {
		return "", err
	}
	return identity.UserName(claims.Issuer, claims.Subject)
}

// Create makes a new Tenant owned by the caller.
//
// metadata.name is server-assigned and any client-supplied name is ignored: the
// name is also the workspace name, so choosing it is choosing a path. Only
// spec.displayName is taken from the request.
//
// status.firstAdmin is stamped here rather than left to a controller, because the
// caller is only knowable from the token — a reconciler seeing the object later
// has no way to tell who asked for it.
func (s *TenantStorage) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	submitted, ok := obj.(*pmtenancyv1alpha1.Tenant)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Tenant, got %T", obj))
	}

	self, err := s.callerName(ctx)
	if err != nil {
		return nil, err
	}

	tenant := &pmtenancyv1alpha1.Tenant{
		Spec: pmtenancyv1alpha1.TenantSpec{
			DisplayName: submitted.Spec.DisplayName,
			// Personal is server-owned: a personal Tenant is the one seeded
			// with a User, and claiming to be one would misreport quota, which
			// excludes personal tenants from the cap.
			Personal:                  false,
			ProjectCreation:           orDefault(submitted.Spec.ProjectCreation, pmtenancyv1alpha1.CreationMembers),
			ProvidersMetadataCreation: orDefault(submitted.Spec.ProvidersMetadataCreation, pmtenancyv1alpha1.CreationMembers),
		},
	}

	// Tenants share one namespace — the directory workspace — so a name
	// collision here is between two unrelated tenants, and the loser must simply
	// get another name rather than an error.
	if _, err := naming.Apply(s.naming,
		naming.Request{Kind: naming.KindTenant, DisplayName: submitted.Spec.DisplayName},
		func(name string) error {
			tenant.Name = name
			return s.client.Create(ctx, tenant)
		},
		apierrors.IsAlreadyExists,
	); err != nil {
		return nil, err
	}

	// Status is a subresource, so it needs its own write. Without it the
	// Tenant has no owner and the controller correctly refuses to seed a
	// Membership for nobody — leaving the creator locked out of what they made.
	tenant.Status.FirstAdmin = self
	if err := s.client.Status().Update(ctx, tenant); err != nil {
		return nil, err
	}
	return tenant, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
