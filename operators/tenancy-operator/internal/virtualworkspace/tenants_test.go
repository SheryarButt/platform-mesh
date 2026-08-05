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

package virtualworkspace_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/virtualworkspace"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"
	"go.platform-mesh.io/tenancy-operator/pkg/naming"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testResolver(t *testing.T) *identity.Resolver {
	t.Helper()
	r, err := identity.NewResolver(identity.Config{UsernameClaim: identity.ClaimEmail, UsernamePrefix: "pm:"})
	require.NoError(t, err)
	return r
}

func directoryClient(t *testing.T, objs ...ctrlruntimeclient.Object) ctrlruntimeclient.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&pmtenancyv1alpha1.Tenant{}).Build()
}

// index builds the caller's membership index, which is what every listing here
// is filtered by.
func index(user string, entries ...pmtenancyv1alpha1.MembershipIndexEntry) *pmtenancyv1alpha1.UserMembershipIndex {
	return &pmtenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: user},
		Spec:       pmtenancyv1alpha1.UserMembershipIndexSpec{Entries: entries},
	}
}

func tenantObject(name string) *pmtenancyv1alpha1.Tenant {
	return &pmtenancyv1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       pmtenancyv1alpha1.TenantSpec{DisplayName: name},
	}
}

// metadata.name is server-assigned: a client choosing it would be choosing a
// workspace path. Only the display name is taken from the request.
func TestTenantCreateIgnoresTheClientName(t *testing.T) {
	strategy, err := naming.Get(naming.StrategyWords)
	require.NoError(t, err)

	cl := directoryClient(t)
	s := virtualworkspace.NewTenantStorage(cl, testResolver(t), strategy)

	obj, err := s.Create(authenticated(testIssuer, testSubject, testEmail), &pmtenancyv1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: "i-picked-this"},
		Spec:       pmtenancyv1alpha1.TenantSpec{DisplayName: "Acme"},
	}, nil, &metav1.CreateOptions{})
	require.NoError(t, err)

	created, ok := obj.(*pmtenancyv1alpha1.Tenant)
	require.True(t, ok)
	assert.NotEqual(t, "i-picked-this", created.Name)
	assert.NoError(t, naming.Validate(created.Name), "the name must be usable as a workspace name")
	assert.Equal(t, "Acme", created.Spec.DisplayName)
}

// `personal` marks the Tenant seeded with a User, and personal tenants are
// excluded from the quota — so a client claiming it would misreport their own cap.
func TestTenantCreateRefusesToBeMarkedPersonal(t *testing.T) {
	strategy, err := naming.Get(naming.StrategyUUID)
	require.NoError(t, err)

	s := virtualworkspace.NewTenantStorage(directoryClient(t), testResolver(t), strategy)
	obj, err := s.Create(authenticated(testIssuer, testSubject, testEmail), &pmtenancyv1alpha1.Tenant{
		Spec: pmtenancyv1alpha1.TenantSpec{DisplayName: "Acme", Personal: true},
	}, nil, &metav1.CreateOptions{})
	require.NoError(t, err)

	assert.False(t, obj.(*pmtenancyv1alpha1.Tenant).Spec.Personal)
}

// Without status.firstAdmin the controller refuses to seed a Membership for
// nobody, and the creator is locked out of what they just made. It is stamped
// here because the caller is only knowable from the token.
func TestTenantCreateRecordsTheCallerAsFirstAdmin(t *testing.T) {
	strategy, err := naming.Get(naming.StrategyUUID)
	require.NoError(t, err)

	cl := directoryClient(t)
	s := virtualworkspace.NewTenantStorage(cl, testResolver(t), strategy)

	obj, err := s.Create(authenticated(testIssuer, testSubject, testEmail),
		&pmtenancyv1alpha1.Tenant{Spec: pmtenancyv1alpha1.TenantSpec{DisplayName: "Acme"}},
		nil, &metav1.CreateOptions{})
	require.NoError(t, err)

	stored := &pmtenancyv1alpha1.Tenant{}
	require.NoError(t, cl.Get(context.Background(),
		ctrlruntimeclient.ObjectKey{Name: obj.(*pmtenancyv1alpha1.Tenant).Name}, stored))
	assert.Equal(t, wantName(t), stored.Status.FirstAdmin)
}

// Two tenants asking for the same display name must get different objects. With
// `words` the name space is small enough that the retry path runs for real.
func TestTenantCreateResolvesNameCollisions(t *testing.T) {
	strategy, err := naming.Get(naming.StrategyWords)
	require.NoError(t, err)

	cl := directoryClient(t)
	s := virtualworkspace.NewTenantStorage(cl, testResolver(t), strategy)
	ctx := authenticated(testIssuer, testSubject, testEmail)

	seen := map[string]bool{}
	for range 200 {
		obj, err := s.Create(ctx,
			&pmtenancyv1alpha1.Tenant{Spec: pmtenancyv1alpha1.TenantSpec{DisplayName: "Shared"}},
			nil, &metav1.CreateOptions{})
		require.NoError(t, err)

		name := obj.(*pmtenancyv1alpha1.Tenant).Name
		assert.False(t, seen[name], "Create returned %q twice", name)
		seen[name] = true
	}
}

// Filtered, not authorized-or-403: a Tenant the caller has no Membership
// in is invisible rather than forbidden.
func TestTenantListIsFilteredByTheIndex(t *testing.T) {
	strategy, err := naming.Get(naming.StrategyUUID)
	require.NoError(t, err)

	cl := directoryClient(t,
		tenantObject("mine"), tenantObject("someone-elses"),
		index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "mine", Role: "admin"}),
	)
	s := virtualworkspace.NewTenantStorage(cl, testResolver(t), strategy)

	obj, err := s.List(authenticated(testIssuer, testSubject, testEmail), nil)
	require.NoError(t, err)

	list, ok := obj.(*pmtenancyv1alpha1.TenantList)
	require.True(t, ok)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "mine", list.Items[0].Name)
}

// The index can outlive the object it points at. RBAC and the objects are the
// truth, so a stale row is skipped rather than failing the whole listing.
func TestTenantListSurvivesAStaleIndexRow(t *testing.T) {
	strategy, err := naming.Get(naming.StrategyUUID)
	require.NoError(t, err)

	cl := directoryClient(t,
		tenantObject("mine"),
		index(wantName(t),
			pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "mine", Role: "admin"},
			pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "deleted", Role: "admin"},
		),
	)
	s := virtualworkspace.NewTenantStorage(cl, testResolver(t), strategy)

	obj, err := s.List(authenticated(testIssuer, testSubject, testEmail), nil)
	require.NoError(t, err)
	assert.Len(t, obj.(*pmtenancyv1alpha1.TenantList).Items, 1)
}

// 404, not 403: a 403 would confirm the Tenant exists and make this API an
// oracle for other tenants' tenant names.
func TestTenantGetOfSomebodyElsesIs404(t *testing.T) {
	strategy, err := naming.Get(naming.StrategyUUID)
	require.NoError(t, err)

	cl := directoryClient(t, tenantObject("someone-elses"), index(wantName(t)))
	s := virtualworkspace.NewTenantStorage(cl, testResolver(t), strategy)

	_, err = s.Get(authenticated(testIssuer, testSubject, testEmail), "someone-elses", &metav1.GetOptions{})
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err), "existence must not leak as a 403")
}

// Failure closed: no verified identity, no listing.
func TestTenantStorageRejectsAnUnauthenticatedCaller(t *testing.T) {
	strategy, err := naming.Get(naming.StrategyUUID)
	require.NoError(t, err)

	s := virtualworkspace.NewTenantStorage(directoryClient(t), testResolver(t), strategy)

	_, err = s.List(context.Background(), nil)
	assert.Error(t, err)
	_, err = s.Create(context.Background(), &pmtenancyv1alpha1.Tenant{}, nil, &metav1.CreateOptions{})
	assert.Error(t, err)
}
