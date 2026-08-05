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

package ocmmodulesetup

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/kcp"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// newReconciler builds a reconciler with the loaded objects already in place
// and every Options func a no-op, so a test fills in only the seam it drives.
// The zero-value funcs are a deliberate statement that the test does not reach
// them; a pass that did would panic rather than silently succeed.
func newReconciler(t *testing.T, setup *pmdeployv1alpha1.OCMModuleSetup, pm *pmdeployv1alpha1.PlatformMesh, opts Options) *reconciler {
	t.Helper()
	return &reconciler{
		opts:  opts,
		log:   testr.New(t),
		old:   setup.DeepCopy(),
		setup: setup,
		pm:    pm,
	}
}

func moduleSetup() *pmdeployv1alpha1.OCMModuleSetup {
	return &pmdeployv1alpha1.OCMModuleSetup{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", Namespace: "pm", Generation: 3},
		Spec: pmdeployv1alpha1.OCMModuleSetupSpec{
			PlatformMeshRef: corev1.LocalObjectReference{Name: "customer-a"},
			OCMModuleRef:    corev1.LocalObjectReference{Name: "acme"},
			Workspaces: []pmdeployv1alpha1.OCMModuleSetupWorkspace{
				{Path: "root:modules:acme"},
			},
		},
	}
}

func platformMesh(rootStructureDone bool) *pmdeployv1alpha1.PlatformMesh {
	pm := &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm"},
	}
	status := metav1.ConditionFalse
	if rootStructureDone {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&pm.Status.Conditions, metav1.Condition{
		Type: rootStructureCondition, Status: status, Reason: "R", Message: "root",
	})
	return pm
}

func TestAwaitRootStructure_stopsWithoutRequeueWhileTheRootStructureIsMissing(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	r := newReconciler(t, setup, platformMesh(false), Options{Requeue: defaultRequeue})

	cont, err := r.awaitRootStructure()
	require.NoError(t, err)

	assert.False(t, cont, "the chain must not continue without the root structure")
	assert.Zero(t, r.requeueAfter, "the PlatformMesh is watched, so waiting on it must not poll")

	cond := meta.FindStatusCondition(setup.Status.Conditions, ConditionWorkspacesProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "WaitingForRootStructure", cond.Reason)
	assert.Equal(t, setup.Generation, cond.ObservedGeneration)
}

func TestAwaitRootStructure_continuesOnceProvisioned(t *testing.T) {
	t.Parallel()
	r := newReconciler(t, moduleSetup(), platformMesh(true), Options{})

	cont, err := r.awaitRootStructure()
	require.NoError(t, err)
	assert.True(t, cont)
}

func TestConnectKcp_pollsWhileTheKubeconfigIsUnminted(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	r := newReconciler(t, setup, platformMesh(true), Options{
		Requeue: defaultRequeue,
		KcpConfig: func(context.Context, *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) {
			return nil, fmt.Errorf("%w: customer-a-provisioner", kcp.ErrPending)
		},
	})

	cont, err := r.connectKcp(t.Context())
	require.NoError(t, err, "an unminted kubeconfig is ordinary progress, not a failure")

	assert.False(t, cont)
	assert.Equal(t, defaultRequeue, r.requeueAfter, "nothing watches the minted secret, so this must poll")

	cond := meta.FindStatusCondition(setup.Status.Conditions, ConditionWorkspacesProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, "WaitingForKubeconfig", cond.Reason)
}

func TestConnectKcp_returnsRealErrors(t *testing.T) {
	t.Parallel()
	r := newReconciler(t, moduleSetup(), platformMesh(true), Options{
		KcpConfig: func(context.Context, *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) {
			return nil, assert.AnError
		},
	})

	_, err := r.connectKcp(t.Context())
	require.ErrorIs(t, err, assert.AnError)
	assert.Zero(t, r.requeueAfter, "a real failure retries with backoff, not a fixed poll")
}

func TestProvisionWorkspaces_pollsWhileAWorkspaceIsPending(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	r := newReconciler(t, setup, platformMesh(true), Options{
		Requeue: defaultRequeue,
		EnsurePath: func(context.Context, *rest.Config, string) (ctrlruntimeclient.Client, error) {
			return nil, fmt.Errorf("%w: acme is Initializing", kcp.ErrWorkspacePending)
		},
	})

	cont, err := r.provisionWorkspaces(t.Context())
	require.NoError(t, err)

	assert.False(t, cont)
	assert.Equal(t, defaultRequeue, r.requeueAfter, "kcp is a separate API server and is not watched")

	cond := meta.FindStatusCondition(setup.Status.Conditions, ConditionWorkspacesProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, "WaitingForWorkspace", cond.Reason)
}

func TestProvisionWorkspaces_marksProvisionedOnceEveryWorkspaceExists(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	var paths []string
	r := newReconciler(t, setup, platformMesh(true), Options{
		EnsurePath: func(_ context.Context, _ *rest.Config, path string) (ctrlruntimeclient.Client, error) {
			paths = append(paths, path)
			return nil, nil
		},
	})

	cont, err := r.provisionWorkspaces(t.Context())
	require.NoError(t, err)
	require.True(t, cont)

	assert.Equal(t, []string{"root:modules:acme"}, paths)
	cond := meta.FindStatusCondition(setup.Status.Conditions, ConditionWorkspacesProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "Provisioned", cond.Reason)
	assert.Equal(t, setup.Generation, cond.ObservedGeneration)
}

// A transient content failure must not leave WorkspacesProvisioned stuck False
// once a later pass succeeds.
func TestProvisionWorkspaces_clearsAStaleFailureOnALaterSuccess(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	setup.Spec.Workspaces[0].Content = []pmdeployv1alpha1.ResourceRef{{Name: "manifests"}}

	fail := true
	opts := Options{
		EnsurePath: func(context.Context, *rest.Config, string) (ctrlruntimeclient.Client, error) { return nil, nil },
		GetModule: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModule, error) {
			return &pmdeployv1alpha1.OCMModule{ObjectMeta: metav1.ObjectMeta{Name: "acme"}}, nil
		},
		ResolveModule: func(context.Context, *pmdeployv1alpha1.OCMModule, *pmdeployv1alpha1.OCMRepository) (*ocmmodule.Resolved, error) {
			return &ocmmodule.Resolved{}, nil
		},
		DownloadResource: func(context.Context, *ocmmodule.Resolved, string) ([]byte, error) {
			if fail {
				return nil, assert.AnError
			}
			return []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n"), nil
		},
		ApplyObject: func(context.Context, ctrlruntimeclient.Client, *unstructured.Unstructured) error { return nil },
	}
	r := newReconciler(t, setup, platformMesh(true), opts)

	_, err := r.provisionWorkspaces(t.Context())
	require.Error(t, err)
	cond := meta.FindStatusCondition(setup.Status.Conditions, ConditionWorkspacesProvisioned)
	require.NotNil(t, cond)
	require.Equal(t, "ContentFailed", cond.Reason)

	fail = false
	cont, err := r.provisionWorkspaces(t.Context())
	require.NoError(t, err)
	require.True(t, cont)

	cond = meta.FindStatusCondition(setup.Status.Conditions, ConditionWorkspacesProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status, "a success must clear the earlier failure")
	assert.Equal(t, "Provisioned", cond.Reason)
}

func TestPublishEndpoints_namesTheShallowestWorkspaceWorkspace(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	setup.Spec.Workspaces = append(setup.Spec.Workspaces,
		pmdeployv1alpha1.OCMModuleSetupWorkspace{Path: "root:modules:acme:validation"})
	r := newReconciler(t, setup, platformMesh(true), Options{})
	r.cfg = &rest.Config{Host: "https://kcp.example.com"}

	r.publishEndpoints()

	assert.Equal(t, map[string]string{
		"workspace":  "https://kcp.example.com/clusters/root:modules:acme",
		"validation": "https://kcp.example.com/clusters/root:modules:acme:validation",
	}, setup.Status.Endpoints)
}

func TestWorkspaceEndpoints_isNilWithoutWorkspaces(t *testing.T) {
	t.Parallel()
	assert.Nil(t, workspaceEndpoints("https://kcp.example.com", nil))
}

func TestReadyCondition_isTrueOnlyWhenTheWorkspacesAreProvisioned(t *testing.T) {
	t.Parallel()

	t.Run("true once provisioned", func(t *testing.T) {
		t.Parallel()
		setup := moduleSetup()
		r := newReconciler(t, setup, nil, Options{})
		meta.SetStatusCondition(&setup.Status.Conditions, workspacesProvisioned(setup.Generation))

		r.setReady(nil)

		cond := meta.FindStatusCondition(setup.Status.Conditions, ConditionReady)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, setup.Generation, cond.ObservedGeneration)
	})

	t.Run("carries the reason of the step that stopped", func(t *testing.T) {
		t.Parallel()
		setup := moduleSetup()
		r := newReconciler(t, setup, nil, Options{})
		meta.SetStatusCondition(&setup.Status.Conditions,
			workspacesPending(setup.Generation, "WaitingForWorkspace", "root:modules:acme is Initializing"))

		r.setReady(nil)

		cond := meta.FindStatusCondition(setup.Status.Conditions, ConditionReady)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, "WaitingForWorkspace", cond.Reason)
		assert.Contains(t, cond.Message, "Initializing")
	})

	t.Run("false before any step has run", func(t *testing.T) {
		t.Parallel()
		setup := moduleSetup()
		r := newReconciler(t, setup, nil, Options{})

		r.setReady(nil)

		cond := meta.FindStatusCondition(setup.Status.Conditions, ConditionReady)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, "NotProvisioned", cond.Reason)
	})

	// A step that fails before reaching WorkspacesProvisioned would otherwise
	// leave a stale True from an earlier pass, and the module controller would
	// keep deploying against a broken setup.
	t.Run("false when the chain failed even though provisioning once succeeded", func(t *testing.T) {
		t.Parallel()
		setup := moduleSetup()
		r := newReconciler(t, setup, nil, Options{})
		meta.SetStatusCondition(&setup.Status.Conditions, workspacesProvisioned(setup.Generation))

		r.setReady(assert.AnError)

		cond := meta.FindStatusCondition(setup.Status.Conditions, ConditionReady)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, "Error", cond.Reason)
		assert.Contains(t, cond.Message, assert.AnError.Error())
	})
}
