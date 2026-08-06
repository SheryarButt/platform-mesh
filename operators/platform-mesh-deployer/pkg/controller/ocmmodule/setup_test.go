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

package ocmmodule

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// A module declaring workspaces must get a OCMModuleSetup that keeps each
// workspace's content with that workspace: flattening it would apply a child's
// manifests into the parent.
func TestProcessWritesModuleSetupPerWorkspace(t *testing.T) {
	mod := testModule()
	mod.Spec.Workspaces = []pmdeployv1alpha1.OCMModuleWorkspace{
		{Name: "", Content: []pmdeployv1alpha1.ResourceRef{{Name: "apiexports"}}},
		{Name: "validation", Content: []pmdeployv1alpha1.ResourceRef{{Name: "validation-schemas"}}},
	}

	workload := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "shards-default#customer-a--s1", workload)

	local := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(platformMesh(true), mod).Build()
	r := newReconciler(t, local, reg, testResolver(), mod)

	err := r.run(t.Context())
	require.NoError(t, err)
	cond := meta.FindStatusCondition(mod.Status.Conditions, ConditionDeployed)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status, "deploy waits until the setup is ready")
	assert.Equal(t, "WaitingForSetup", cond.Reason)

	setup := &pmdeployv1alpha1.OCMModuleSetup{}
	require.NoError(t, local.Get(t.Context(),
		ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: "acme"}, setup))

	require.Len(t, setup.Spec.Workspaces, 2)
	byPath := map[string][]string{}
	for _, ws := range setup.Spec.Workspaces {
		for _, c := range ws.Content {
			byPath[ws.Path] = append(byPath[ws.Path], c.Name)
		}
	}
	assert.Equal(t, map[string][]string{
		"root:modules:acme":            {"apiexports"},
		"root:modules:acme:validation": {"validation-schemas"},
	}, byPath)
}

// Without workspaces there is no kcp side, so no handshake object is written.
func TestProcessWithoutWorkspacesWritesNoSetup(t *testing.T) {
	mod := testModule()

	workload := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "shards-default#customer-a--s1", workload)

	local := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(platformMesh(true), mod).Build()
	r := newReconciler(t, local, reg, testResolver(), mod)

	err := r.run(t.Context())
	require.NoError(t, err)

	err = local.Get(t.Context(),
		ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: "acme"}, &pmdeployv1alpha1.OCMModuleSetup{})
	assert.Error(t, err)
}

// Deleting a OCMModule must remove what it applied on other clusters: owner
// references only reach objects on the config plane.
func TestFinalizePrunesWorkloads(t *testing.T) {
	mod := testModule()

	workload := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "shards-default#customer-a--s1", workload)

	local := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(platformMesh(true), mod).Build()
	r := newReconciler(t, local, reg, testResolver(), mod)

	err := r.run(t.Context())
	require.NoError(t, err)

	key := ctrlruntimeclient.ObjectKey{Namespace: "acme-system", Name: "acme-agent"}
	require.NoError(t, workload.Get(t.Context(), key, &corev1.Service{}))
	require.NotEmpty(t, mod.Status.AppliedKinds, "teardown needs the kinds recorded")

	controllerutil.AddFinalizer(mod, Finalizer)
	_, err = r.finalize(t.Context())
	require.NoError(t, err)

	assert.True(t, apierrors.IsNotFound(workload.Get(t.Context(), key, &corev1.Service{})),
		"the Service must be gone")
	assert.True(t, apierrors.IsNotFound(workload.Get(t.Context(), key, &corev1.ConfigMap{})),
		"the generated ConfigMap must be gone")
}

// The finalizer is added before anything is applied, since the workloads it
// protects live on clusters no owner reference reaches.
func TestEnsureFinalizer_stopsThePassAfterAddingIt(t *testing.T) {
	mod := testModule()
	local := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(mod).Build()
	r := newReconciler(t, local, clusters.NewRegistry(), testResolver(), mod)

	cont, err := r.ensureFinalizer(t.Context())
	require.NoError(t, err)
	assert.False(t, cont, "the update re-triggers the watch, so the pass stops here")
	assert.True(t, controllerutil.ContainsFinalizer(mod, Finalizer))
}
