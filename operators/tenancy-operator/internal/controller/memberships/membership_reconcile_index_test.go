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

package memberships

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func indexScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(pmtenancyv1alpha1.AddToScheme(s))
	return s
}

func directoryClient(t *testing.T, objs ...ctrlruntimeclient.Object) ctrlruntimeclient.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(indexScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&pmtenancyv1alpha1.UserMembershipIndex{}).Build()
}

func tenantClusterClient(t *testing.T, objs ...ctrlruntimeclient.Object) ctrlruntimeclient.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(indexScheme(t)).WithObjects(objs...).Build()
}

func indexedTenant() *pmtenancyv1alpha1.Tenant {
	return &pmtenancyv1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-uuid"},
		Spec:       pmtenancyv1alpha1.TenantSpec{DisplayName: "ACME", Personal: true},
		Status: pmtenancyv1alpha1.TenantStatus{
			ClusterID: "tenant-cluster", FirstAdmin: "owner-digest",
		},
	}
}

// entryFor is the whole of the row-building logic; the surrounding Reconcile is
// a CreateOrUpdate against a client a unit test cannot reach through the
// multicluster manager.
func buildEntry(t *testing.T, m *pmtenancyv1alpha1.Membership, tenantObjs []ctrlruntimeclient.Object) (*pmtenancyv1alpha1.MembershipIndexEntry, error) {
	t.Helper()
	return (&membershipIndex{}).entryFor(
		context.Background(),
		tenantClusterClient(t, tenantObjs...),
		directoryClient(t, indexedTenant()),
		m,
	)
}

// The bug this closes: a project-scope grant produced working RBAC and no index
// row, so kcp let the user in and the tenancy API told them they had nothing.
func TestIndexWritesAProjectScopeRow(t *testing.T) {
	proj := &pmtenancyv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "proj-uuid"},
		Spec:       pmtenancyv1alpha1.ProjectSpec{DisplayName: "platform"},
		Status:     pmtenancyv1alpha1.ProjectStatus{ClusterID: "proj-cluster"},
	}

	entry, err := buildEntry(t,
		membership(pmtenancyv1alpha1.MembershipScopeProject, "proj-uuid", "tenant-cluster"),
		[]ctrlruntimeclient.Object{proj})

	require.NoError(t, err)
	assert.Equal(t, "tenant-uuid", entry.TenantUUID)
	assert.Equal(t, "proj-uuid", entry.ProjectUUID)
	assert.Equal(t, "platform", entry.ProjectDisplayName)
	assert.Equal(t, "proj-cluster", entry.ProjectClusterID,
		"the cluster a kubeconfig points at — without it the row cannot be acted on")
}

// A tenant-scope row carries no project and reaches every Project in the tenant, which
// is the same implication applyRBAC materializes as one binding per Project.
func TestIndexWritesATenantScopeRowWithNoAccount(t *testing.T) {
	entry, err := buildEntry(t,
		membership(pmtenancyv1alpha1.MembershipScopeTenant, "", "tenant-cluster"), nil)

	require.NoError(t, err)
	assert.Equal(t, "tenant-uuid", entry.TenantUUID)
	assert.Empty(t, entry.ProjectUUID)
	assert.Equal(t, "tenant-cluster", entry.TenantClusterID)
	assert.True(t, entry.Personal)
	assert.Equal(t, "owner-digest", entry.TenantFirstAdmin)
}

// The row reports the MEMBERSHIP's role, not the Tenant's idea of one. This
// is what makes `memberships set-role` visible to a client, and it is what the
// old two-writer arrangement could not express.
func TestIndexRowCarriesTheMembershipRole(t *testing.T) {
	for _, role := range []string{
		pmtenancyv1alpha1.MembershipRoleAdmin,
		pmtenancyv1alpha1.MembershipRoleMember,
		pmtenancyv1alpha1.MembershipRoleViewer,
	} {
		t.Run(role, func(t *testing.T) {
			m := membership(pmtenancyv1alpha1.MembershipScopeTenant, "", "tenant-cluster")
			m.Spec.Role = role

			entry, err := buildEntry(t, m, nil)
			require.NoError(t, err)
			assert.Equal(t, role, entry.Role)
		})
	}
}

// A Membership names no Tenant UUID — it is identified by the workspace it
// lives in — so the row cannot be built until a Tenant claims that cluster.
func TestIndexWaitsForATenantToClaimTheCluster(t *testing.T) {
	_, err := (&membershipIndex{}).entryFor(
		context.Background(),
		tenantClusterClient(t),
		directoryClient(t), // no Tenants at all
		membership(pmtenancyv1alpha1.MembershipScopeTenant, "", "tenant-cluster"),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tenant resolves to cluster")
}

// A project-scope grant naming a Project that is gone must not produce a row:
// the index is what a client lists workspaces from, and a row here is a workspace
// that appears and then 403s.
func TestIndexRefusesAProjectThatDoesNotExist(t *testing.T) {
	_, err := buildEntry(t,
		membership(pmtenancyv1alpha1.MembershipScopeProject, "ghost", "tenant-cluster"), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

// The key is (tenant, project): the same pair replaces, a different pair
// appends, and the order is stable so an unchanged reconcile produces an unchanged
// object — otherwise every pass writes, and every write wakes every watcher.
func TestUpsertEntry(t *testing.T) {
	entries := []pmtenancyv1alpha1.MembershipIndexEntry{
		{TenantUUID: "b", ProjectUUID: "2", Role: "member"},
		{TenantUUID: "a", ProjectUUID: "1", Role: "member"},
	}

	entries = upsertEntry(entries, pmtenancyv1alpha1.MembershipIndexEntry{
		TenantUUID: "a", ProjectUUID: "1", Role: "admin",
	})
	require.Len(t, entries, 2, "the same (tenant, project) must replace, not append")
	assert.Equal(t, "admin", entries[0].Role)
	assert.Equal(t, "a", entries[0].TenantUUID, "entries must stay sorted")

	entries = upsertEntry(entries, pmtenancyv1alpha1.MembershipIndexEntry{
		TenantUUID: "a", ProjectUUID: "2", Role: "viewer",
	})
	assert.Len(t, entries, 3, "a different project in the same tenant is a new row")

	// A tenant-scope row and a project-scope row in the same tenant are DIFFERENT rows:
	// the empty projectUUID is a key, not a wildcard.
	entries = upsertEntry(entries, pmtenancyv1alpha1.MembershipIndexEntry{
		TenantUUID: "a", Role: "admin",
	})
	assert.Len(t, entries, 4)

	before := append([]pmtenancyv1alpha1.MembershipIndexEntry(nil), entries...)
	entries = upsertEntry(entries, entries[0])
	assert.Equal(t, before, entries, "re-upserting an identical row must change nothing")
}
