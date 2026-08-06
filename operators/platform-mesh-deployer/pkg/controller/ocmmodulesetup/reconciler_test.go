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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// stubOptions fills every required func with a harmless double, so a test
// overrides only the one it is about and validate() still passes.
func stubOptions() Options {
	return Options{
		GetPlatformMesh: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			return platformMesh(true), nil
		},
		GetModule: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModule, error) {
			return &pmdeployv1alpha1.OCMModule{}, nil
		},
		GetModuleSetup: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModuleSetup, error) {
			return moduleSetup(), nil
		},
		UpdateModuleSetup: func(context.Context, *pmdeployv1alpha1.OCMModuleSetup) error { return nil },
		PatchStatus: func(context.Context, *pmdeployv1alpha1.OCMModuleSetup, *pmdeployv1alpha1.OCMModuleSetup) error {
			return nil
		},
		KcpConfig: func(context.Context, *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) {
			return &rest.Config{Host: "https://kcp.example.com"}, nil
		},
		EnsurePath: func(context.Context, *rest.Config, string) (ctrlruntimeclient.Client, error) { return nil, nil },
		DeletePath: func(context.Context, *rest.Config, string) error { return nil },
		ResolveModule: func(context.Context, *pmdeployv1alpha1.OCMModule, *pmdeployv1alpha1.OCMRepository) (*ocmmodule.Resolved, error) {
			return &ocmmodule.Resolved{}, nil
		},
		DownloadResource: func(context.Context, *ocmmodule.Resolved, string) ([]byte, error) { return nil, nil },
	}
}

func TestValidate_rejectsAMissingDependency(t *testing.T) {
	t.Parallel()
	opts := stubOptions()
	opts.KcpConfig = nil

	_, err := NewController(opts)
	require.ErrorContains(t, err, "KcpConfig is required")
}

func TestValidate_defaultsTheRequeueAndApply(t *testing.T) {
	t.Parallel()
	opts := stubOptions()
	require.NoError(t, opts.validate())

	assert.Equal(t, defaultRequeue, opts.Requeue)
	assert.NotNil(t, opts.ApplyObject, "ApplyObject defaults to the real apply")
}

func TestReconcile_isANoOpForADeletedModuleSetup(t *testing.T) {
	t.Parallel()
	opts := stubOptions()
	opts.GetModuleSetup = func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModuleSetup, error) {
		return nil, notFound()
	}
	c, err := NewController(opts)
	require.NoError(t, err)

	res, err := c.Reconcile(t.Context(), reconcile.Request{})
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)
}

// The status carries why a pass stopped, so it is committed even when a step
// did not finish.
func TestReconcile_commitsTheStatusWhenAStepStops(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	controllerutil.AddFinalizer(setup, Finalizer)

	var patched *pmdeployv1alpha1.OCMModuleSetup
	opts := stubOptions()
	opts.GetModuleSetup = func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModuleSetup, error) {
		return setup, nil
	}
	opts.GetPlatformMesh = func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
		return platformMesh(false), nil
	}
	opts.PatchStatus = func(_ context.Context, _, current *pmdeployv1alpha1.OCMModuleSetup) error {
		patched = current
		return nil
	}
	c, err := NewController(opts)
	require.NoError(t, err)

	res, err := c.Reconcile(t.Context(), reconcile.Request{})
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "the PlatformMesh is watched, so this must not poll")

	require.NotNil(t, patched, "the reason the pass stopped has to reach the API server")
	cond := meta.FindStatusCondition(patched.Status.Conditions, ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "WaitingForRootStructure", cond.Reason)
}

func TestReconcile_publishesEndpointsAndReadyOnASuccessfulPass(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	controllerutil.AddFinalizer(setup, Finalizer)

	var patched *pmdeployv1alpha1.OCMModuleSetup
	opts := stubOptions()
	opts.GetModuleSetup = func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModuleSetup, error) {
		return setup, nil
	}
	opts.PatchStatus = func(_ context.Context, _, current *pmdeployv1alpha1.OCMModuleSetup) error {
		patched = current
		return nil
	}
	c, err := NewController(opts)
	require.NoError(t, err)

	res, err := c.Reconcile(t.Context(), reconcile.Request{})
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	require.NotNil(t, patched)
	assert.Equal(t, map[string]string{
		"workspace": "https://kcp.example.com/clusters/root:modules:acme",
	}, patched.Status.Endpoints)

	cond := meta.FindStatusCondition(patched.Status.Conditions, ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestCommitStatus_skipsAnUnchangedStatus(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	r := &reconciler{
		opts: Options{PatchStatus: func(context.Context, *pmdeployv1alpha1.OCMModuleSetup, *pmdeployv1alpha1.OCMModuleSetup) error {
			t.Fatal("an unchanged status must not be patched")
			return nil
		}},
		log:   testr.New(t),
		old:   setup.DeepCopy(),
		setup: setup,
	}

	require.NoError(t, r.commitStatus(t.Context()))
}

func TestFetchModuleSetup_reportsARealReadFailure(t *testing.T) {
	t.Parallel()
	r := &reconciler{
		opts: Options{GetModuleSetup: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModuleSetup, error) {
			return nil, assert.AnError
		}},
		log: testr.New(t),
	}

	cont, err := r.fetchModuleSetup(t.Context())
	require.ErrorIs(t, err, assert.AnError)
	assert.False(t, cont)
}

func TestFetchPlatformMesh_namesTheReferenceItCouldNotRead(t *testing.T) {
	t.Parallel()
	r := newReconciler(t, moduleSetup(), nil, Options{
		GetPlatformMesh: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			return nil, assert.AnError
		},
	})

	cont, err := r.fetchPlatformMesh(t.Context())
	require.ErrorIs(t, err, assert.AnError)
	assert.ErrorContains(t, err, `getting PlatformMesh "customer-a"`)
	assert.False(t, cont)
}

// A failing pass must not also carry a RequeueAfter: controller-runtime
// discards the Result when an error is returned and warns about the pair.
func TestReconcile_doesNotReturnBothARequeueAndAnError(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	controllerutil.AddFinalizer(setup, Finalizer)

	opts := stubOptions()
	opts.GetModuleSetup = func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModuleSetup, error) {
		return setup, nil
	}
	opts.KcpConfig = func(context.Context, *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) {
		return nil, fmt.Errorf("%w: unminted", kcp.ErrPending)
	}
	opts.PatchStatus = func(context.Context, *pmdeployv1alpha1.OCMModuleSetup, *pmdeployv1alpha1.OCMModuleSetup) error {
		return assert.AnError
	}
	c, err := NewController(opts)
	require.NoError(t, err)

	res, err := c.Reconcile(t.Context(), reconcile.Request{})
	require.Error(t, err)
	assert.Zero(t, res.RequeueAfter)
}

// The status has to reach the API server even when a step failed outright, or
// Ready stays True from the previous pass.
func TestReconcile_reportsAFailedStepOnReady(t *testing.T) {
	t.Parallel()
	setup := moduleSetup()
	controllerutil.AddFinalizer(setup, Finalizer)
	meta.SetStatusCondition(&setup.Status.Conditions, workspacesProvisioned(setup.Generation))

	var patched *pmdeployv1alpha1.OCMModuleSetup
	opts := stubOptions()
	opts.GetModuleSetup = func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModuleSetup, error) {
		return setup, nil
	}
	opts.EnsurePath = func(context.Context, *rest.Config, string) (ctrlruntimeclient.Client, error) {
		return nil, assert.AnError
	}
	opts.PatchStatus = func(_ context.Context, _, current *pmdeployv1alpha1.OCMModuleSetup) error {
		patched = current
		return nil
	}
	c, err := NewController(opts)
	require.NoError(t, err)

	_, err = c.Reconcile(t.Context(), reconcile.Request{})
	require.ErrorIs(t, err, assert.AnError)

	require.NotNil(t, patched)
	cond := meta.FindStatusCondition(patched.Status.Conditions, ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status, "a broken pass must not leave Ready True")
	assert.Equal(t, "Error", cond.Reason)
}
