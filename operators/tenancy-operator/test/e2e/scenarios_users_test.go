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

package e2e

// User scenarios: a person signs in, works, is let into somebody else's tenant,
// and is shut out of it again.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"

	rbacv1 "k8s.io/api/rbac/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Scenario: a new identity signs in and is given somewhere to work.
func TestScenarioUserOnboardingGrantAndRevoke(t *testing.T) {
	f := newFleet(t)

	t.Log("alice signs in")
	alice := f.createUser(t, "alice@acme.example")
	aliceTenant := f.awaitSeededTenant(t, alice)
	aliceProject := f.awaitSeededProject(t, aliceTenant)

	f.awaitAdmitted(t, aliceProject.Status.ClusterID, rbacName("alice@acme.example"), nil,
		"a new identity must end up able to work in the project seeded for it")

	assert.False(t,
		allowed(t, f.tenantPath(aliceTenant), rbacName("alice@acme.example"), nil, "get", "", "configmaps"),
		"nothing may be bound in the Tenant workspace, or the tier stops being a boundary")

	t.Log("bob signs in, and must not reach alice's project")
	bob := f.createUser(t, "bob@acme.example")
	bobTenant := f.awaitSeededTenant(t, bob)
	require.NotEqual(t, aliceTenant.Name, bobTenant.Name, "each identity gets its own tenant")

	assert.False(t,
		allowed(t, aliceProject.Status.ClusterID, rbacName("bob@acme.example"), nil, "get", "", "configmaps"),
		"a provisioned user must reach nothing in a tenant they were never granted")

	t.Log("alice grants bob member access to her tenant")
	grant := f.grantUser(t, aliceTenant, bob, pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleMember)

	f.awaitAdmitted(t, aliceProject.Status.ClusterID, rbacName("bob@acme.example"), nil,
		"a tenant-scope grant must reach every project in the tenant")

	assert.False(t,
		allowed(t, aliceProject.Status.ClusterID, rbacName("bob@acme.example"), nil,
			"escalate", "rbac.authorization.k8s.io", "clusterroles"),
		"a member must not be able to escalate their own privileges")

	t.Log("alice revokes bob's access")
	f.revoke(t, aliceTenant, grant)

	f.awaitDenied(t, aliceProject.Status.ClusterID, rbacName("bob@acme.example"), nil,
		"revoking the record must revoke the ACCESS")

	assert.True(t,
		allowed(t, aliceProject.Status.ClusterID, rbacName("alice@acme.example"), nil, "get", "", "configmaps"),
		"revoking one grant must not disturb another")
}

// Scenario: somebody deletes a binding by hand.
func TestScenarioTamperedBindingIsRepaired(t *testing.T) {
	f := newFleet(t)

	user := f.createUser(t, "carol@acme.example")
	tenant := f.awaitSeededTenant(t, user)
	project := f.awaitSeededProject(t, tenant)
	cluster := project.Status.ClusterID

	f.awaitAdmitted(t, cluster, rbacName("carol@acme.example"), nil, "carol starts admitted")

	t.Log("deleting the platform-owned binding by hand")
	bindings := &rbacv1.ClusterRoleBindingList{}
	require.NoError(t, clusterClient(cluster).List(context.Background(), bindings))
	var removed int
	for i := range bindings.Items {
		b := &bindings.Items[i]
		if b.Labels["tenancy.platform-mesh.io/membership"] == "" {
			continue
		}
		require.NoError(t, releaseAndDelete(t, cluster, b.Name))
		removed++
	}
	require.NotZero(t, removed, "there should be a platform-owned binding to remove")

	f.awaitAdmitted(t, cluster, rbacName("carol@acme.example"), nil,
		"a binding deleted out from under the operator must be rebuilt")
}

// rbacName is the subject kcp sees for a dev identity: the configured username
// prefix and the email claim.
func rbacName(email string) string { return "pm:" + email }

// assertNoPersonalTenant pins the half of --tenancy-personal-tenants-enabled=false
// that is easy to lose: not merely that seeding is skipped, but that the User is
// still provisioned and Ready without one.
func assertNoPersonalTenant(t *testing.T, f *fleet, user *pmtenancyv1alpha1.User) {
	t.Helper()

	got := &pmtenancyv1alpha1.User{}
	require.NoError(t, clusterClient(f.layout.Directory).Get(context.Background(),
		ctrlruntimeclient.ObjectKey{Name: user.Name}, got))
	assert.Empty(t, got.Status.DefaultTenant,
		"personal tenants are disabled, so nothing should have been seeded")
}

// releaseAndDelete drops the operator's finalizer and deletes a binding, which is
// what a determined `kubectl delete` amounts to.
func releaseAndDelete(t *testing.T, cluster, name string) error {
	t.Helper()
	cl := clusterClient(cluster)

	b := &rbacv1.ClusterRoleBinding{}
	if err := cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: name}, b); err != nil {
		return err
	}
	b.Finalizers = nil
	if err := cl.Update(context.Background(), b); err != nil {
		return err
	}
	return ctrlruntimeclient.IgnoreNotFound(cl.Delete(context.Background(), b))
}
