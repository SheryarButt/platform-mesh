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

// clusterAnnotation is what kcp stamps on everything it stores, and how an object
// knows which logical cluster it lives in.
const clusterAnnotation = "kcp.io/cluster"

func rbacClient(t *testing.T, objs ...ctrlruntimeclient.Object) ctrlruntimeclient.Client {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(pmtenancyv1alpha1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func membership(scope, proj, cluster string) *pmtenancyv1alpha1.Membership {
	m := &pmtenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{Name: "m"},
		Spec:       pmtenancyv1alpha1.MembershipSpec{User: "u", Scope: scope, Project: proj, Role: "admin"},
	}
	if cluster != "" {
		m.Annotations = map[string]string{clusterAnnotation: cluster}
	}
	return m
}

func readyAccount(name, cluster string) *pmtenancyv1alpha1.Project {
	return &pmtenancyv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     pmtenancyv1alpha1.ProjectStatus{ClusterID: cluster},
	}
}

// A project-scope Membership binds in exactly one place: the Project it names.
func TestTargetClustersForAccountScopeIsExact(t *testing.T) {
	cl := rbacClient(t, readyAccount("proj-1", "c1"), readyAccount("proj-2", "c2"))

	got, err := (&applyRBAC{}).targetClusters(context.Background(), cl, membership(
		pmtenancyv1alpha1.MembershipScopeProject, "proj-1", "tenant-cluster"))

	require.NoError(t, err)
	assert.Equal(t, []string{"c1"}, got)
}

// Tenant admin means admin everywhere in the tenant, and kcp only asks "is there a
// binding here" — so the implication has to be materialized, one per Project.
func TestTargetClustersForTenantScopeFansOut(t *testing.T) {
	cl := rbacClient(t, readyAccount("proj-1", "c1"), readyAccount("proj-2", "c2"))

	got, err := (&applyRBAC{}).targetClusters(context.Background(), cl, membership(
		pmtenancyv1alpha1.MembershipScopeTenant, "", "tenant-cluster"))

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"c1", "c2"}, got)
	assert.NotContains(t, got, "tenant-cluster",
		"nothing is ever bound in the Tenant workspace: a tenant with access there could "+
			"rewrite the Memberships that decide access, behind the virtual workspace rather than through it")
}

// The Tenant tier stays unreachable even when the tenant has no Projects at
// all — which is the state every Tenant is in at bootstrap, since the owner
// Membership is written before the first Project exists.
func TestTargetClustersForTenantScopeNeverBindsTheTenantItself(t *testing.T) {
	got, err := (&applyRBAC{}).targetClusters(context.Background(), rbacClient(t), membership(
		pmtenancyv1alpha1.MembershipScopeTenant, "", "tenant-cluster"))

	require.NoError(t, err)
	assert.Empty(t, got, "no Projects yet means nothing to bind, not a binding in the tenant workspace")
}

// A Project with no workspace yet is SKIPPED rather than waited for: the other
// grants should not be held up, and the Project's own event brings us back.
func TestTargetClustersSkipsProjectsThatAreNotReady(t *testing.T) {
	cl := rbacClient(t,
		readyAccount("ready", "c1"),
		&pmtenancyv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "pending"}},
	)

	got, err := (&applyRBAC{}).targetClusters(context.Background(), cl, membership(
		pmtenancyv1alpha1.MembershipScopeTenant, "", "tenant-cluster"))

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"c1"}, got)
}

// A Project on its way out must not be granted into — the binding would outlive
// the workspace.
func TestTargetClustersSkipsDeletingProjects(t *testing.T) {
	deleting := readyAccount("going", "c2")
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"keep.example.com"}

	cl := rbacClient(t, readyAccount("ready", "c1"), deleting)

	got, err := (&applyRBAC{}).targetClusters(context.Background(), cl, membership(
		pmtenancyv1alpha1.MembershipScopeTenant, "", "tenant-cluster"))

	require.NoError(t, err)
	assert.NotContains(t, got, "c2")
}

// A bad spec is terminal, not a retry.
func TestTargetClustersRejectsAProjectScopeWithNoProject(t *testing.T) {
	_, err := (&applyRBAC{}).targetClusters(context.Background(), rbacClient(t), membership(
		pmtenancyv1alpha1.MembershipScopeProject, "", "tenant-cluster"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires spec.project")
}

// Granting into a Project that has no workspace would write a binding nowhere.
func TestTargetClustersWaitsForTheNamedAccountsWorkspace(t *testing.T) {
	cl := rbacClient(t, &pmtenancyv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "proj-1"}})

	_, err := (&applyRBAC{}).targetClusters(context.Background(), cl, membership(
		pmtenancyv1alpha1.MembershipScopeProject, "proj-1", "tenant-cluster"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workspace yet")
}

func TestTargetClustersRejectsAMissingAccount(t *testing.T) {
	_, err := (&applyRBAC{}).targetClusters(context.Background(), rbacClient(t), membership(
		pmtenancyv1alpha1.MembershipScopeProject, "ghost", "tenant-cluster"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

// Without the cluster annotation there is no way to know which Tenant a
// Membership belongs to, and guessing would write a grant into the wrong tenant.
func TestOrgClusterOfRequiresTheClusterAnnotation(t *testing.T) {
	_, err := tenantClusterOf(membership(pmtenancyv1alpha1.MembershipScopeTenant, "", ""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster annotation")
}
