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

package platformmesh

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/kcp"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// withKcp adds the two kcp funcs, whose presence is what puts the root
// structure step in the chain at all.
func withKcp(t *testing.T, pm *pmdeployv1alpha1.PlatformMesh, config func() (*rest.Config, error), ensure func(path string) error) *reconciler {
	t.Helper()
	r := newReconciler(t, newClient(t, pm), clusters.NewRegistry(), pm)
	r.opts.KcpConfig = func(context.Context, *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) { return config() }
	r.opts.EnsureKcpPath = func(_ context.Context, _ *rest.Config, path string) error { return ensure(path) }
	return r
}

func simplePlatformMesh() *pmdeployv1alpha1.PlatformMesh {
	return &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm", Generation: 2},
	}
}

// kcp-operator writes the admin kubeconfig asynchronously and nothing watches
// the secret, so this one polls.
func TestReconcileRootStructure_pollsWhileTheKubeconfigIsUnminted(t *testing.T) {
	t.Parallel()
	pm := simplePlatformMesh()
	r := withKcp(t, pm,
		func() (*rest.Config, error) { return nil, fmt.Errorf("%w: customer-a-provisioner", kcp.ErrPending) },
		func(string) error { return nil })

	cont, err := r.reconcileRootStructure(t.Context())
	require.NoError(t, err, "an unminted kubeconfig is ordinary progress, not a failure")

	assert.False(t, cont)
	assert.Equal(t, defaultRequeue, r.requeueAfter)

	cond := meta.FindStatusCondition(pm.Status.Conditions, ConditionRootStructureProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "WaitingForKubeconfig", cond.Reason)
	assert.Equal(t, pm.Generation, cond.ObservedGeneration)
}

func TestReconcileRootStructure_pollsWhileAWorkspaceIsPending(t *testing.T) {
	t.Parallel()
	pm := simplePlatformMesh()
	r := withKcp(t, pm,
		func() (*rest.Config, error) { return &rest.Config{Host: "https://kcp.example.com"}, nil },
		func(string) error { return fmt.Errorf("%w: modules is Initializing", kcp.ErrWorkspacePending) })

	cont, err := r.reconcileRootStructure(t.Context())
	require.NoError(t, err)

	assert.False(t, cont)
	assert.Equal(t, defaultRequeue, r.requeueAfter, "kcp is a separate API server and is not watched")

	cond := meta.FindStatusCondition(pm.Status.Conditions, ConditionRootStructureProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, "WaitingForWorkspace", cond.Reason)
}

func TestReconcileRootStructure_createsEveryRootWorkspace(t *testing.T) {
	t.Parallel()
	pm := simplePlatformMesh()
	var ensured []string
	r := withKcp(t, pm,
		func() (*rest.Config, error) { return &rest.Config{Host: "https://kcp.example.com"}, nil },
		func(path string) error { ensured = append(ensured, path); return nil })

	cont, err := r.reconcileRootStructure(t.Context())
	require.NoError(t, err)
	require.True(t, cont)

	assert.Equal(t, roots, ensured)
	assert.Zero(t, r.requeueAfter)

	cond := meta.FindStatusCondition(pm.Status.Conditions, ConditionRootStructureProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "Provisioned", cond.Reason)
	assert.Equal(t, pm.Generation, cond.ObservedGeneration)
}

// A transient kcp failure must not leave RootStructureProvisioned stuck False
// once a later pass succeeds.
func TestReconcileRootStructure_clearsAStalePendingOnALaterSuccess(t *testing.T) {
	t.Parallel()
	pm := simplePlatformMesh()
	pending := true
	r := withKcp(t, pm,
		func() (*rest.Config, error) { return &rest.Config{Host: "https://kcp.example.com"}, nil },
		func(string) error {
			if pending {
				return fmt.Errorf("%w: modules is Initializing", kcp.ErrWorkspacePending)
			}
			return nil
		})

	_, err := r.reconcileRootStructure(t.Context())
	require.NoError(t, err)
	require.Equal(t, metav1.ConditionFalse,
		meta.FindStatusCondition(pm.Status.Conditions, ConditionRootStructureProvisioned).Status)

	pending = false
	cont, err := r.reconcileRootStructure(t.Context())
	require.NoError(t, err)
	require.True(t, cont)

	cond := meta.FindStatusCondition(pm.Status.Conditions, ConditionRootStructureProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status, "a success must clear the earlier wait")
}

func TestReconcileRootStructure_namesTheWorkspaceItCouldNotCreate(t *testing.T) {
	t.Parallel()
	pm := simplePlatformMesh()
	r := withKcp(t, pm,
		func() (*rest.Config, error) { return &rest.Config{Host: "https://kcp.example.com"}, nil },
		func(string) error { return assert.AnError })

	_, err := r.reconcileRootStructure(t.Context())
	require.ErrorIs(t, err, assert.AnError)
	assert.ErrorContains(t, err, roots[0])
	assert.Zero(t, r.requeueAfter, "a real failure retries with backoff, not a fixed poll")
}
