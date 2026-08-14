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

package projects

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func pruneTestClient(t *testing.T, objs ...ctrlruntimeclient.Object) ctrlruntimeclient.Client {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(pmtenancyv1alpha1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func projectObj(name string) *pmtenancyv1alpha1.Project {
	return &pmtenancyv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func grant(name, scope, project string) *pmtenancyv1alpha1.Membership {
	return &pmtenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: pmtenancyv1alpha1.MembershipSpec{
			User: "someone", Scope: scope, Project: project, Role: "member",
		},
	}
}

// The gap this closes: a Project's workspace is destroyed on delete, but its
// grants live one tier up and nothing disposed of them — dangling objects naming a
// place that no longer exists.
func TestPruneDeletesTheGrantsNamingThisAccount(t *testing.T) {
	cl := pruneTestClient(t,
		grant("mine", pmtenancyv1alpha1.MembershipScopeProject, "doomed"),
		grant("other-project", pmtenancyv1alpha1.MembershipScopeProject, "survivor"),
		grant("tenant-wide", pmtenancyv1alpha1.MembershipScopeTenant, ""),
	)

	proj := projectObj("doomed")

	// TWO PASSES, and the first one not finishing is the design. Deletion is
	// asynchronous — the grants it just asked to remove still hold their own
	// finalizers — so the step counts what it found and comes back rather than
	// declaring the workspace safe to destroy.
	status, err := (&projectMemberships{}).Finalize(context.Background(), cl, proj)
	require.NoError(t, err)
	assert.Equal(t, chain.StopAndRequeue, status, "the first pass has only just asked")

	status, err = (&projectMemberships{}).Finalize(context.Background(), cl, proj)
	require.NoError(t, err)
	assert.Equal(t, chain.Continue, status, "once they are gone, the delete may proceed")

	list := &pmtenancyv1alpha1.MembershipList{}
	require.NoError(t, cl.List(context.Background(), list))

	names := map[string]bool{}
	for i := range list.Items {
		names[list.Items[i].Name] = true
	}
	assert.False(t, names["mine"], "the grant naming this project must go")
	assert.True(t, names["other-project"], "a grant naming a DIFFERENT project must not")
	assert.True(t, names["tenant-wide"], "a tenant-scope grant reaches every project and is not this project's to delete")
}

// Deletion waits for the grants to actually go, rather than firing and forgetting:
// each Membership holds finalizers that remove role bindings and prune index rows,
// and destroying the workspace underneath them turns an orderly revoke into a race.
func TestPruneWaitsForGrantsStillFinalizing(t *testing.T) {
	holding := grant("slow", pmtenancyv1alpha1.MembershipScopeProject, "doomed")
	now := metav1.Now()
	holding.DeletionTimestamp = &now
	holding.Finalizers = []string{"membership.tenancy.platform-mesh.io/rbac"}

	cl := pruneTestClient(t, holding)
	proj := projectObj("doomed")

	status, err := (&projectMemberships{}).Finalize(context.Background(), cl, proj)
	require.NoError(t, err)
	assert.Equal(t, chain.StopAndRequeue, status)

	c := meta.FindStatusCondition(proj.Status.Conditions, pmtenancyv1alpha1.ProjectConditionMembershipsPruned)
	require.NotNil(t, c, "a deletion that hangs must say what it is waiting for")
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, "Pruning", c.Reason)
	assert.Contains(t, c.Message, "1 membership")
}

// Nothing to prune is the common case and must not block the delete.
func TestPruneIsANoOpWithNoGrants(t *testing.T) {
	cl := pruneTestClient(t, grant("elsewhere", pmtenancyv1alpha1.MembershipScopeProject, "survivor"))

	status, err := (&projectMemberships{}).Finalize(context.Background(), cl, projectObj("doomed"))
	require.NoError(t, err)
	assert.Equal(t, chain.Continue, status)
}

// Reconcile holds the finalizer and nothing else: a Membership pointing at an
// Project is the normal case, not a condition worth a list per pass.
func TestPruneReconcileIsAHolder(t *testing.T) {
	proj := projectObj("live")
	status, err := (&projectMemberships{}).Reconcile(context.Background(), pruneTestClient(t), proj)

	require.NoError(t, err)
	assert.Equal(t, chain.Continue, status)

	c := meta.FindStatusCondition(proj.Status.Conditions, pmtenancyv1alpha1.ProjectConditionMembershipsPruned)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
}
