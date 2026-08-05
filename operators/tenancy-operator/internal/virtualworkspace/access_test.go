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

func indexClient(t *testing.T, user string, entries ...pmtenancyv1alpha1.MembershipIndexEntry) ctrlruntimeclient.Client {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(pmtenancyv1alpha1.AddToScheme(s))

	objs := []ctrlruntimeclient.Object{}
	if user != "" {
		objs = append(objs, &pmtenancyv1alpha1.UserMembershipIndex{
			ObjectMeta: metav1.ObjectMeta{Name: user},
			Spec:       pmtenancyv1alpha1.UserMembershipIndexSpec{Entries: entries},
		})
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

// Having no memberships is the normal state of a brand-new identity, not a
// failure — an error here would make first login look broken.
func TestResolveAccessOfAnUnknownUserIsEmpty(t *testing.T) {
	got, err := resolveAccess(context.Background(), indexClient(t, ""), "nobody")
	require.NoError(t, err)
	assert.Empty(t, got.Tenants)
}

// A user can hold several Memberships in one Tenant, and the view must
// report the STRONGEST. Compared by rank rather than first-seen, because with
// three tiers a first-seen rule turns index ordering into an access decision.
func TestResolveAccessTakesTheStrongestRole(t *testing.T) {
	for name, tc := range map[string]struct {
		roles []string
		want  string
	}{
		"viewer then member": {[]string{"viewer", "member"}, "member"},
		"member then viewer": {[]string{"member", "viewer"}, "viewer"},
		"viewer then admin":  {[]string{"viewer", "admin"}, "admin"},
		"admin then viewer":  {[]string{"admin", "viewer"}, "admin"},
		"member then admin":  {[]string{"member", "admin"}, "admin"},
	} {
		t.Run(name, func(t *testing.T) {
			var entries []pmtenancyv1alpha1.MembershipIndexEntry
			for i, role := range tc.roles {
				entries = append(entries, pmtenancyv1alpha1.MembershipIndexEntry{
					TenantUUID:      "tenant",
					TenantClusterID: "c1",
					ProjectUUID:     string(rune('a' + i)),
					Role:            role,
				})
			}

			got, err := resolveAccess(context.Background(), indexClient(t, "u", entries...), "u")
			require.NoError(t, err)
			require.Contains(t, got.Tenants, "tenant")

			if name == "member then viewer" {
				// Ordering must not matter: both orderings resolve to member.
				assert.Equal(t, "member", got.Tenants["tenant"].Role)
				return
			}
			assert.Equal(t, tc.want, got.Tenants["tenant"].Role)
		})
	}
}

// A role this build does not understand must never out-rank one it does: an
// older binary meeting a newer role should under-privilege, not over-privilege.
func TestResolveAccessIgnoresAnUnknownRole(t *testing.T) {
	got, err := resolveAccess(context.Background(), indexClient(t, "u",
		pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "tenant", ProjectUUID: "a", Role: "member"},
		pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "tenant", ProjectUUID: "b", Role: "superuser"},
	), "u")
	require.NoError(t, err)
	assert.Equal(t, "member", got.Tenants["tenant"].Role)
}

// A tenant-scope row (no project) carries access to every Project in the tenant —
// the same implication the Membership reconciler materializes as one binding per
// Project.
func TestResolveAccessOrgScopeSeesEveryAccount(t *testing.T) {
	got, err := resolveAccess(context.Background(), indexClient(t, "u",
		pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin"},
	), "u")
	require.NoError(t, err)

	tenant := got.Tenants["tenant"]
	require.NotNil(t, tenant)
	assert.True(t, tenant.AllProjects)
	assert.True(t, tenant.CanSeeProject("anything-at-all"))
}

// A project-scope row grants exactly that Project and nothing else. Access does
// not inherit — that is what makes "one team space but not the whole project"
// expressible.
func TestResolveAccessAccountScopeIsExact(t *testing.T) {
	got, err := resolveAccess(context.Background(), indexClient(t, "u",
		pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "tenant", ProjectUUID: "proj-1", Role: "member"},
	), "u")
	require.NoError(t, err)

	tenant := got.Tenants["tenant"]
	require.NotNil(t, tenant)
	assert.False(t, tenant.AllProjects)
	assert.True(t, tenant.CanSeeProject("proj-1"))
	assert.False(t, tenant.CanSeeProject("proj-2"))
}

// A row with no Tenant is unusable, and silently indexing it under "" would
// create a phantom tenant every caller appears to belong to.
func TestResolveAccessSkipsRowsWithNoOrganization(t *testing.T) {
	got, err := resolveAccess(context.Background(), indexClient(t, "u",
		pmtenancyv1alpha1.MembershipIndexEntry{ProjectUUID: "orphan", Role: "admin"},
	), "u")
	require.NoError(t, err)
	assert.Empty(t, got.Tenants)
}

// The cluster ID is what a client dials, so it has to survive being carried on
// only some of the rows.
func TestResolveAccessKeepsTheClusterID(t *testing.T) {
	got, err := resolveAccess(context.Background(), indexClient(t, "u",
		pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "tenant", ProjectUUID: "a", Role: "member"},
		pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "tenant", TenantClusterID: "c1", ProjectUUID: "b", Role: "member"},
	), "u")
	require.NoError(t, err)
	assert.Equal(t, "c1", got.Tenants["tenant"].ClusterID)
}
