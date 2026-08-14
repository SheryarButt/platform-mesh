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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/kcp"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func deleting(setup *pmdeployv1alpha1.OCMModuleSetup) *pmdeployv1alpha1.OCMModuleSetup {
	now := metav1.Now()
	setup.DeletionTimestamp = &now
	controllerutil.AddFinalizer(setup, Finalizer)
	return setup
}

func notFound() error {
	return apierrors.NewNotFound(schema.GroupResource{Resource: "platformmeshes"}, "customer-a")
}

func TestEnsureFinalizer_stopsThePassAfterAddingIt(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	var updated *pmdeployv1alpha1.OCMModuleSetup
	r := newReconciler(t, setup, nil, Options{
		UpdateModuleSetup: func(_ context.Context, s *pmdeployv1alpha1.OCMModuleSetup) error {
			updated = s
			return nil
		},
	})

	cont, err := r.ensureFinalizer(t.Context())
	require.NoError(t, err)

	assert.False(t, cont, "the update re-triggers the watch, so the pass stops here")
	require.NotNil(t, updated)
	assert.True(t, controllerutil.ContainsFinalizer(updated, Finalizer))
}

func TestEnsureFinalizer_continuesWhenAlreadyPresent(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	controllerutil.AddFinalizer(setup, Finalizer)
	r := newReconciler(t, setup, nil, Options{})

	cont, err := r.ensureFinalizer(t.Context())
	require.NoError(t, err)
	assert.True(t, cont)
}

// A deleted PlatformMesh takes its kcp with it, so there is nothing to clean up.
func TestFinalize_releasesWhenThePlatformMeshIsGone(t *testing.T) {
	t.Parallel()
	setup := deleting(moduleSetup())
	var updated *pmdeployv1alpha1.OCMModuleSetup
	r := newReconciler(t, setup, nil, Options{
		GetPlatformMesh: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			return nil, notFound()
		},
		UpdateModuleSetup: func(_ context.Context, s *pmdeployv1alpha1.OCMModuleSetup) error {
			updated = s
			return nil
		},
	})

	res, err := r.finalize(t.Context())
	require.NoError(t, err)

	assert.Zero(t, res.RequeueAfter)
	require.NotNil(t, updated)
	assert.False(t, controllerutil.ContainsFinalizer(updated, Finalizer))
}

// Without a reachable kcp the workspaces cannot be removed, and they are gone
// with kcp anyway.
func TestFinalize_releasesWhenKcpIsUnreachable(t *testing.T) {
	t.Parallel()
	setup := deleting(moduleSetup())
	var updated *pmdeployv1alpha1.OCMModuleSetup
	r := newReconciler(t, setup, nil, Options{
		GetPlatformMesh: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			return platformMesh(true), nil
		},
		KcpConfig: func(context.Context, *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) {
			return nil, fmt.Errorf("%w: customer-a-provisioner", kcp.ErrPending)
		},
		UpdateModuleSetup: func(_ context.Context, s *pmdeployv1alpha1.OCMModuleSetup) error {
			updated = s
			return nil
		},
	})

	res, err := r.finalize(t.Context())
	require.NoError(t, err)

	assert.Zero(t, res.RequeueAfter)
	require.NotNil(t, updated)
	assert.False(t, controllerutil.ContainsFinalizer(updated, Finalizer))
}

func TestFinalize_holdsTheFinalizerWhileAWorkspaceIsTerminating(t *testing.T) {
	t.Parallel()
	setup := deleting(moduleSetup())
	r := newReconciler(t, setup, nil, Options{
		Requeue: defaultRequeue,
		GetPlatformMesh: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			return platformMesh(true), nil
		},
		KcpConfig: func(context.Context, *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) {
			return &rest.Config{Host: "https://kcp.example.com"}, nil
		},
		DeletePath: func(context.Context, *rest.Config, string) error {
			return fmt.Errorf("%w: acme is terminating", kcp.ErrWorkspacePending)
		},
		PatchStatus: func(context.Context, *pmdeployv1alpha1.OCMModuleSetup, *pmdeployv1alpha1.OCMModuleSetup) error {
			return nil
		},
		UpdateModuleSetup: func(context.Context, *pmdeployv1alpha1.OCMModuleSetup) error {
			t.Fatal("the finalizer must not be dropped while a workspace is terminating")
			return nil
		},
	})

	res, err := r.finalize(t.Context())
	require.NoError(t, err)

	assert.Equal(t, defaultRequeue, res.RequeueAfter)
	assert.True(t, controllerutil.ContainsFinalizer(setup, Finalizer))
}

func TestFinalize_deletesDeepestFirstThenReleases(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	setup.Spec.Workspaces = append(setup.Spec.Workspaces,
		pmdeployv1alpha1.OCMModuleSetupWorkspace{Path: "root:modules:acme:validation"})
	deleting(setup)

	var deleted []string
	var updated *pmdeployv1alpha1.OCMModuleSetup
	r := newReconciler(t, setup, nil, Options{
		GetPlatformMesh: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			return platformMesh(true), nil
		},
		KcpConfig: func(context.Context, *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) {
			return &rest.Config{Host: "https://kcp.example.com"}, nil
		},
		DeletePath: func(_ context.Context, _ *rest.Config, path string) error {
			deleted = append(deleted, path)
			return nil
		},
		UpdateModuleSetup: func(_ context.Context, s *pmdeployv1alpha1.OCMModuleSetup) error {
			updated = s
			return nil
		},
	})

	res, err := r.finalize(t.Context())
	require.NoError(t, err)

	assert.Zero(t, res.RequeueAfter)
	assert.Equal(t, []string{"root:modules:acme:validation", "root:modules:acme"}, deleted)
	require.NotNil(t, updated)
	assert.False(t, controllerutil.ContainsFinalizer(updated, Finalizer))
}

// Deletion is idempotent: a second pass after the finalizer is gone must not
// try to reach kcp again.
func TestFinalize_isANoOpOnceTheFinalizerIsGone(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	now := metav1.Now()
	setup.DeletionTimestamp = &now
	r := newReconciler(t, setup, nil, Options{})

	res, err := r.finalize(t.Context())
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
}

func TestDeepestFirst_deletesChildrenBeforeParents(t *testing.T) {
	t.Parallel()
	got := deepestFirst([]pmdeployv1alpha1.OCMModuleSetupWorkspace{
		{Path: "root:modules:acme"},
		{Path: "root:modules:acme:validation"},
		{Path: "root:modules:acme:audit"},
	})
	assert.Equal(t, []string{
		"root:modules:acme:audit",
		"root:modules:acme:validation",
		"root:modules:acme",
	}, got)
}

// The finalizer is the only thing keeping the workspaces reachable, so a
// failure to delete one must not release it.
func TestFinalize_holdsTheFinalizerWhenADeleteFails(t *testing.T) {
	t.Parallel()
	setup := deleting(moduleSetup())
	r := newReconciler(t, setup, nil, Options{
		Requeue: defaultRequeue,
		GetPlatformMesh: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			return platformMesh(true), nil
		},
		KcpConfig: func(context.Context, *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) {
			return &rest.Config{Host: "https://kcp.example.com"}, nil
		},
		DeletePath: func(context.Context, *rest.Config, string) error { return assert.AnError },
		UpdateModuleSetup: func(context.Context, *pmdeployv1alpha1.OCMModuleSetup) error {
			t.Fatal("the finalizer must not be dropped when a workspace could not be deleted")
			return nil
		},
	})

	_, err := r.finalize(t.Context())
	require.ErrorIs(t, err, assert.AnError)
	assert.True(t, controllerutil.ContainsFinalizer(setup, Finalizer))
}

// A PlatformMesh that cannot be read is not the same as one that is gone, so
// the finalizer stays until it is known which.
func TestFinalize_holdsTheFinalizerWhenThePlatformMeshCannotBeRead(t *testing.T) {
	t.Parallel()
	setup := deleting(moduleSetup())
	r := newReconciler(t, setup, nil, Options{
		GetPlatformMesh: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			return nil, assert.AnError
		},
		UpdateModuleSetup: func(context.Context, *pmdeployv1alpha1.OCMModuleSetup) error {
			t.Fatal("the finalizer must not be dropped on an unknown PlatformMesh state")
			return nil
		},
	})

	_, err := r.finalize(t.Context())
	require.ErrorIs(t, err, assert.AnError)
	assert.True(t, controllerutil.ContainsFinalizer(setup, Finalizer))
}

func TestEnsureFinalizer_reportsAFailedUpdate(t *testing.T) {
	t.Parallel()
	r := newReconciler(t, moduleSetup(), nil, Options{
		UpdateModuleSetup: func(context.Context, *pmdeployv1alpha1.OCMModuleSetup) error { return assert.AnError },
	})

	cont, err := r.ensureFinalizer(t.Context())
	require.ErrorIs(t, err, assert.AnError)
	assert.False(t, cont)
}

// A stuck deletion is otherwise invisible on the object.
func TestFinalize_recordsWhyItIsWaiting(t *testing.T) {
	t.Parallel()
	setup := deleting(moduleSetup())
	var patched *pmdeployv1alpha1.OCMModuleSetup
	r := newReconciler(t, setup, nil, Options{
		Requeue: defaultRequeue,
		GetPlatformMesh: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			return platformMesh(true), nil
		},
		KcpConfig: func(context.Context, *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) {
			return &rest.Config{Host: "https://kcp.example.com"}, nil
		},
		DeletePath: func(context.Context, *rest.Config, string) error {
			return fmt.Errorf("%w: acme is terminating", kcp.ErrWorkspacePending)
		},
		PatchStatus: func(_ context.Context, _, current *pmdeployv1alpha1.OCMModuleSetup) error {
			patched = current
			return nil
		},
	})

	res, err := r.finalize(t.Context())
	require.NoError(t, err)
	assert.Equal(t, defaultRequeue, res.RequeueAfter)

	require.NotNil(t, patched)
	cond := meta.FindStatusCondition(patched.Status.Conditions, ConditionWorkspacesProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, "WaitingForWorkspace", cond.Reason)
	assert.Contains(t, cond.Message, "terminating")
}
