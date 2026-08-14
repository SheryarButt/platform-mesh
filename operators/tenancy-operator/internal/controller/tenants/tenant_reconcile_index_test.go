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

package tenants

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func indexTestClient(t *testing.T, objs ...ctrlruntimeclient.Object) ctrlruntimeclient.Client {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(pmtenancyv1alpha1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&pmtenancyv1alpha1.UserMembershipIndex{}).Build()
}

func testOrg(name, admin string) *pmtenancyv1alpha1.Tenant {
	return &pmtenancyv1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       pmtenancyv1alpha1.TenantSpec{DisplayName: name},
		Status:     pmtenancyv1alpha1.TenantStatus{FirstAdmin: admin, ClusterID: "c1"},
	}
}

// This step writes NO rows. Every row belongs to the Membership that causes it,
// including the first admin's — two writers on one row would fight the moment a
// role changed, because this step could only ever say `admin`.
func TestTenantIndexWritesNoRows(t *testing.T) {
	cl := indexTestClient(t)
	tenant := testOrg("tenant", "user-digest")

	status, err := (&tenantIndex{}).Reconcile(context.Background(), cl, tenant)
	require.NoError(t, err)
	assert.Equal(t, chain.Continue, status)

	umi := &pmtenancyv1alpha1.UserMembershipIndex{}
	err = cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: "user-digest"}, umi)
	assert.True(t, apierrors.IsNotFound(err), "the owner's row is the Membership reconciler's to write")

	c := meta.FindStatusCondition(tenant.Status.Conditions, pmtenancyv1alpha1.TenantConditionIndexSynced)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionTrue, c.Status,
		"the rows ARE accounted for — by the Memberships that own them — so a healthy tenant must not look stuck")
}

// Deleting a Tenant must prune it from EVERY index that carries it, or
// members keep seeing something they can no longer reach.
func TestTenantIndexFinalizePrunesEveryIndex(t *testing.T) {
	mine := pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "tenant", Role: "admin"}
	other := pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "keep", Role: "member"}

	cl := indexTestClient(t,
		&pmtenancyv1alpha1.UserMembershipIndex{
			ObjectMeta: metav1.ObjectMeta{Name: "a"},
			Spec:       pmtenancyv1alpha1.UserMembershipIndexSpec{Entries: []pmtenancyv1alpha1.MembershipIndexEntry{mine, other}},
		},
		&pmtenancyv1alpha1.UserMembershipIndex{
			ObjectMeta: metav1.ObjectMeta{Name: "b"},
			Spec:       pmtenancyv1alpha1.UserMembershipIndexSpec{Entries: []pmtenancyv1alpha1.MembershipIndexEntry{mine}},
		},
	)

	status, err := (&tenantIndex{}).Finalize(context.Background(), cl, testOrg("tenant", "a"))
	require.NoError(t, err)
	assert.Equal(t, chain.Continue, status)

	a := &pmtenancyv1alpha1.UserMembershipIndex{}
	require.NoError(t, cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: "a"}, a))
	require.Len(t, a.Spec.Entries, 1)
	assert.Equal(t, "keep", a.Spec.Entries[0].TenantUUID)
	assert.Equal(t, int32(1), a.Status.EntryCount, "entryCount is a separate write and must not be left stale")

	b := &pmtenancyv1alpha1.UserMembershipIndex{}
	require.NoError(t, cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: "b"}, b))
	assert.Empty(t, b.Spec.Entries)
	assert.Zero(t, b.Status.EntryCount)
}
