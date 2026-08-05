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

package workspaces

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

func childClient(t *testing.T, objs ...ctrlruntimeclient.Object) ctrlruntimeclient.Client {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func workspace(name, cluster string) *kcptenancyv1alpha1.Workspace {
	return &kcptenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kcptenancyv1alpha1.WorkspaceSpec{Cluster: cluster},
	}
}

// kcp does not create `default` for the tenant WorkspaceType, because it omits
// `extend: root:universal` — so this step does.
func TestDefaultNamespaceIsCreated(t *testing.T) {
	cl := childClient(t)

	res, err := (&defaultNamespace{}).Reconcile(context.Background(), cl, workspace("ws", "c1"))
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	ns := &corev1.Namespace{}
	require.NoError(t, cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: DefaultNamespace}, ns))
}

// AlreadyExists is the success case on every reconcile after the first: this step
// owns the namespace's existence and nothing about its content.
func TestDefaultNamespaceIsIdempotent(t *testing.T) {
	cl := childClient(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNamespace}})

	for range 3 {
		res, err := (&defaultNamespace{}).Reconcile(context.Background(), cl, workspace("ws", "c1"))
		require.NoError(t, err)
		assert.Zero(t, res.RequeueAfter)
	}
}

// Steps are independent, so one asking to be retried must not cancel another's
// request — the soonest wins and they all run again together.
func TestSoonestKeepsTheEarlierRequeue(t *testing.T) {
	none := ctrl.Result{}
	fast := ctrl.Result{RequeueAfter: time.Second}
	slow := ctrl.Result{RequeueAfter: time.Minute}

	assert.Equal(t, none, soonest(none, none))
	assert.Equal(t, fast, soonest(none, fast))
	assert.Equal(t, fast, soonest(fast, none))
	assert.Equal(t, fast, soonest(slow, fast))
	assert.Equal(t, fast, soonest(fast, slow))
}

// The step must name itself in an error, or a failing multi-step reconcile says
// only that something went wrong.
func TestDefaultNamespaceHasAName(t *testing.T) {
	assert.NotEmpty(t, (&defaultNamespace{}).Name())
}

// The filter decides whether this controller acts at all. It currently matches
// `workspace`, while the WorkspaceType this operator actually creates tenant
// workspaces with is `project` — so nothing matches and the step never runs.
//
// Pinned as CURRENT behaviour rather than as desired behaviour: the constant and
// cfg.ProjectWorkspaceType have drifted apart, and this test is what will fail
// when they are reconciled.
func TestTenantWorkspaceTypeConstantIsStale(t *testing.T) {
	assert.Equal(t, "workspace", tenantWorkspaceType,
		"if this changed, the filter and --tenancy-project-workspace-type may now agree; "+
			"check that the controller matches the WorkspaceType Projects are created with")
}
