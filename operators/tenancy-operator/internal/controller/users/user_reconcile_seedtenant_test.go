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

package users

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/config"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"
	"go.platform-mesh.io/tenancy-operator/pkg/naming"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func seedClient(t *testing.T, objs ...ctrlruntimeclient.Object) ctrlruntimeclient.Client {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(pmtenancyv1alpha1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&pmtenancyv1alpha1.Tenant{}, &pmtenancyv1alpha1.User{}).Build()
}

func seedStep(t *testing.T, strategyName string) *seedTenant {
	t.Helper()
	s, err := naming.Get(strategyName)
	require.NoError(t, err)
	return &seedTenant{
		cfg:    config.TenancyConfig{PersonalTenantsEnabled: true, PersonalTenantDisplayNameFormat: "%s's personal"},
		naming: s,
	}
}

func testUser(name string) *pmtenancyv1alpha1.User {
	return &pmtenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: pmtenancyv1alpha1.UserSpec{
			Email:   "alice@acme.example",
			Name:    "Alice",
			Tenancy: pmtenancyv1alpha1.UserTenancySpec{SeedTenant: true, SeedProject: true},
		},
	}
}

// The seeded name now comes from the configured strategy, so a dev environment
// gets a readable workspace path instead of a UUID.
func TestSeedTenantUsesTheConfiguredStrategy(t *testing.T) {
	cl := seedClient(t)
	user := testUser("u1")

	status, err := seedStep(t, naming.StrategyWords).Reconcile(context.Background(), cl, user)
	require.NoError(t, err)
	assert.Equal(t, chain.Continue, status)

	require.NotEmpty(t, user.Status.DefaultTenant)
	assert.NoError(t, naming.Validate(user.Status.DefaultTenant))
	assert.Contains(t, user.Status.DefaultTenant, "-", "a word-pair name, not a UUID")
	assert.NotContains(t, user.Status.DefaultTenant, "0000")
}

// The property the whole seeded path exists for: creating the Tenant and
// recording the pointer are two writes, so a reconcile that dies in between must
// adopt the object it already made rather than seed a second one.
func TestSeedTenantAdoptsAfterALostStatusWrite(t *testing.T) {
	cl := seedClient(t)
	step := seedStep(t, naming.StrategyWords)

	user := testUser("u1")
	_, err := step.Reconcile(context.Background(), cl, user)
	require.NoError(t, err)
	first := user.Status.DefaultTenant

	// Simulate the crash: the Tenant exists, the pointer was never written.
	user.Status.DefaultTenant = ""

	_, err = step.Reconcile(context.Background(), cl, user)
	require.NoError(t, err)
	assert.Equal(t, first, user.Status.DefaultTenant, "the retry seeded a second Tenant")

	list := &pmtenancyv1alpha1.TenantList{}
	require.NoError(t, cl.List(context.Background(), list))
	assert.Len(t, list.Items, 1, "exactly one personal Tenant may exist per User")
}

// Two users must never land on one Tenant. With `words` the name space is
// small enough that they really do derive the same candidate, so the ownership
// annotation is what separates them.
func TestSeedTenantNeverAdoptsAnotherUsersTenant(t *testing.T) {
	cl := seedClient(t)
	step := seedStep(t, naming.StrategyWords)

	owners := map[string]string{}
	for i := 0; i < 300; i++ {
		user := testUser(string(rune('a'+i%26)) + string(rune('a'+i/26)))
		_, err := step.Reconcile(context.Background(), cl, user)
		require.NoError(t, err)

		name := user.Status.DefaultTenant
		if other, clash := owners[name]; clash {
			t.Fatalf("%s adopted %s's Tenant %q", user.Name, other, name)
		}
		owners[name] = user.Name
	}
}

// The annotation is written in the SAME request that creates the object, which
// is what makes it usable as proof of ownership — status.firstAdmin is a second
// write and is empty in exactly the crash window this has to survive.
func TestSeedTenantStampsOwnershipAtCreation(t *testing.T) {
	cl := seedClient(t)
	user := testUser("u1")

	_, err := seedStep(t, naming.StrategyUUID).Reconcile(context.Background(), cl, user)
	require.NoError(t, err)

	tenant := &pmtenancyv1alpha1.Tenant{}
	require.NoError(t, cl.Get(context.Background(),
		ctrlruntimeclient.ObjectKey{Name: user.Status.DefaultTenant}, tenant))

	assert.Equal(t, user.Name, tenant.Annotations[pmtenancyv1alpha1.AnnotationSeededFor])
	assert.True(t, tenant.Spec.Personal)
	assert.Equal(t, user.Name, tenant.Status.FirstAdmin, "without this the tenant is owned by nobody")
}

// One-shot: a User who deleted their personal Tenant has deleted it.
// Re-creating it would argue with a deliberate act and resurrect a workspace tree.
func TestSeedTenantDoesNotRebuildADeletedOne(t *testing.T) {
	cl := seedClient(t)
	user := testUser("u1")
	user.Status.DefaultTenant = "already-done"

	_, err := seedStep(t, naming.StrategyUUID).Reconcile(context.Background(), cl, user)
	require.NoError(t, err)

	list := &pmtenancyv1alpha1.TenantList{}
	require.NoError(t, cl.List(context.Background(), list))
	assert.Empty(t, list.Items)
}

// Two gates, and the order matters: the operator-level switch can turn seeding
// off fleet-wide, but never on for a User whose spec says no.
func TestSeedTenantRespectsBothGates(t *testing.T) {
	for name, tc := range map[string]struct {
		fleetWide bool
		perUser   bool
	}{
		"disabled fleet-wide": {false, true},
		"disabled per user":   {true, false},
		"disabled both":       {false, false},
	} {
		t.Run(name, func(t *testing.T) {
			cl := seedClient(t)
			step := seedStep(t, naming.StrategyUUID)
			step.cfg.PersonalTenantsEnabled = tc.fleetWide

			user := testUser("u1")
			user.Spec.Tenancy.SeedTenant = tc.perUser

			status, err := step.Reconcile(context.Background(), cl, user)
			require.NoError(t, err)
			assert.Equal(t, chain.Continue, status)
			assert.Empty(t, user.Status.DefaultTenant)

			list := &pmtenancyv1alpha1.TenantList{}
			require.NoError(t, cl.List(context.Background(), list))
			assert.Empty(t, list.Items)
		})
	}
}
