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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestValidate_rejectsAMissingDependency(t *testing.T) {
	t.Parallel()
	opts := configPlaneOptions(fake.NewClientBuilder().Build(), clusters.NewRegistry(), nil)
	opts.FanOut = nil

	_, err := NewController(opts)
	require.ErrorContains(t, err, "FanOut is required")
}

func TestValidate_defaultsTheRequeue(t *testing.T) {
	t.Parallel()
	opts := configPlaneOptions(fake.NewClientBuilder().Build(), clusters.NewRegistry(), nil)
	opts.Requeue = 0
	require.NoError(t, opts.validate())
	assert.Equal(t, defaultRequeue, opts.Requeue)
}

func TestReconcile_isANoOpForADeletedModule(t *testing.T) {
	t.Parallel()
	local := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	c, err := NewController(configPlaneOptions(local, clusters.NewRegistry(), testResolver()))
	require.NoError(t, err)

	res, err := c.Reconcile(t.Context(), reconcile.Request{
		NamespacedName: ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: "gone"},
	})
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)
}

// The finalizer has to be in place before anything is applied, so the first
// pass only adds it.
func TestReconcile_addsTheFinalizerBeforeDeploying(t *testing.T) {
	t.Parallel()
	mod := testModule()
	local := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(platformMesh(true), mod).Build()
	c, err := NewController(configPlaneOptions(local, clusters.NewRegistry(), testResolver()))
	require.NoError(t, err)

	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: ctrlruntimeclient.ObjectKeyFromObject(mod)})
	require.NoError(t, err)

	got := &pmdeployv1alpha1.OCMModule{}
	require.NoError(t, local.Get(t.Context(), ctrlruntimeclient.ObjectKeyFromObject(mod), got))
	assert.Contains(t, got.Finalizers, Finalizer)
}

func TestReadyCondition_reportsTheFirstUnmetStep(t *testing.T) {
	t.Parallel()
	mod := testModule()
	mod.Generation = 4
	setCondition(mod, cond(ConditionSpecValid, mod.Generation, metav1.ConditionTrue, "Valid", "ok"))
	setCondition(mod, cond(ConditionGated, mod.Generation, metav1.ConditionFalse, "WaitingForDependency", `dependency "etcd" is not ready`))
	setCondition(mod, cond(ConditionResolved, mod.Generation, metav1.ConditionTrue, "Resolved", "ok"))

	cond := readyCondition(mod, nil)

	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "WaitingForDependency", cond.Reason, "the first unmet step is the reason, not the last")
	assert.Contains(t, cond.Message, "etcd")
	assert.Equal(t, mod.Generation, cond.ObservedGeneration)
}

// A step that fails before writing its own condition would otherwise leave a
// stale True from an earlier pass, and the PlatformMesh's pre-topology gate
// would read it as ready.
func TestReadyCondition_isFalseWhenTheChainErroredDespiteGreenSteps(t *testing.T) {
	t.Parallel()
	mod := testModule()
	for _, c := range chainConditions {
		setCondition(mod, cond(c, mod.Generation, metav1.ConditionTrue, "Ok", "ok"))
	}

	cond := readyCondition(mod, assert.AnError)

	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "Error", cond.Reason)
	assert.Contains(t, cond.Message, assert.AnError.Error())
}

func TestReadyCondition_isTrueOnceEveryStepIsGreen(t *testing.T) {
	t.Parallel()
	mod := testModule()
	for _, c := range chainConditions {
		setCondition(mod, cond(c, mod.Generation, metav1.ConditionTrue, "Ok", "ok"))
	}

	cond := readyCondition(mod, nil)

	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "Deployed", cond.Reason)
}

func TestReadyCondition_isFalseBeforeAnyStepHasRun(t *testing.T) {
	t.Parallel()
	cond := readyCondition(testModule(), nil)

	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "NotReconciled", cond.Reason)
	assert.Contains(t, cond.Message, ConditionSpecValid)
}

// For only enqueues the changed OCMModule, so without this watch a dependency
// becoming ready would never wake the modules waiting on it.
func TestEnqueueDependentModules_enqueuesTheModulesDependingOnIt(t *testing.T) {
	t.Parallel()
	waiting := testModule()
	waiting.Name = "waiting"
	waiting.Spec.DependsOn = []corev1.LocalObjectReference{{Name: "etcd"}}
	unrelated := testModule()
	unrelated.Name = "unrelated"

	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(waiting, unrelated).Build()
	dep := &pmdeployv1alpha1.OCMModule{ObjectMeta: metav1.ObjectMeta{Name: "etcd", Namespace: "pm"}}

	got := enqueueDependentModules(cl)(t.Context(), dep)

	require.Len(t, got, 1)
	assert.Equal(t, "waiting", got[0].Name)
}

func TestEnqueueDependentModules_isEmptyWhenTheListFails(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().Build()
	dep := &pmdeployv1alpha1.OCMModule{ObjectMeta: metav1.ObjectMeta{Name: "etcd", Namespace: "pm"}}

	assert.Empty(t, enqueueDependentModules(cl)(t.Context(), dep))
}

func TestEnqueueModulesOfPlatformMesh_enqueuesOnlyItsOwn(t *testing.T) {
	t.Parallel()
	mine := testModule()
	other := testModule()
	other.Name = "elsewhere"
	other.Spec.PlatformMeshRef = corev1.LocalObjectReference{Name: "customer-b"}

	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(mine, other).Build()
	pm := &pmdeployv1alpha1.PlatformMesh{ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm"}}

	got := enqueueModulesOfPlatformMesh(cl)(t.Context(), pm)

	require.Len(t, got, 1)
	assert.Equal(t, "acme", got[0].Name)
}

func TestCommitStatus_skipsAnUnchangedStatus(t *testing.T) {
	t.Parallel()
	mod := testModule()
	r := &reconciler{
		opts: Options{PatchStatus: func(context.Context, *pmdeployv1alpha1.OCMModule, *pmdeployv1alpha1.OCMModule) error {
			t.Fatal("an unchanged status must not be patched")
			return nil
		}},
		old: mod.DeepCopy(),
		mod: mod,
	}

	require.NoError(t, r.commitStatus(t.Context()))
}

// Ready is what the PlatformMesh pre-topology gate, the dependency gates and
// the e2e suite read, so a full pass has to commit it.
func TestReconcile_commitsReadyOnEveryPass(t *testing.T) {
	t.Parallel()
	mod := testModule()
	controllerutil.AddFinalizer(mod, Finalizer)

	workload := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "shards-default#customer-a--s1", workload)

	local := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(platformMesh(true), mod).WithStatusSubresource(mod).Build()
	c, err := NewController(configPlaneOptions(local, reg, testResolver()))
	require.NoError(t, err)

	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: ctrlruntimeclient.ObjectKeyFromObject(mod)})
	require.NoError(t, err)

	got := &pmdeployv1alpha1.OCMModule{}
	require.NoError(t, local.Get(t.Context(), ctrlruntimeclient.ObjectKeyFromObject(mod), got))

	ready := meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
	require.NotNil(t, ready, "the gates other controllers read must reach the API server")
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, "Deployed", ready.Reason)
}

// A pass that stops early still has to say so on Ready, or the gates read a
// stale True from an earlier pass.
func TestReconcile_commitsANotReadyReasonWhenTheChainStops(t *testing.T) {
	t.Parallel()
	mod := testModule()
	controllerutil.AddFinalizer(mod, Finalizer)

	local := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(platformMesh(false), mod).WithStatusSubresource(mod).Build()
	c, err := NewController(configPlaneOptions(local, clusters.NewRegistry(), testResolver()))
	require.NoError(t, err)

	_, err = c.Reconcile(t.Context(), reconcile.Request{NamespacedName: ctrlruntimeclient.ObjectKeyFromObject(mod)})
	require.NoError(t, err)

	got := &pmdeployv1alpha1.OCMModule{}
	require.NoError(t, local.Get(t.Context(), ctrlruntimeclient.ObjectKeyFromObject(mod), got))

	ready := meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, "WaitingForTopology", ready.Reason)
}

// A deleted OCMModule goes down the finalize path; a second pass after the
// finalizer is gone must not prune again.
func TestReconcile_finalizesADeletedModule(t *testing.T) {
	t.Parallel()
	mod := testModule()
	controllerutil.AddFinalizer(mod, Finalizer)
	now := metav1.Now()
	mod.DeletionTimestamp = &now

	local := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(platformMesh(true), mod).Build()
	r := newReconciler(t, local, clusters.NewRegistry(), testResolver(), mod)

	_, err := r.finalize(t.Context())
	require.NoError(t, err)
	require.False(t, controllerutil.ContainsFinalizer(mod, Finalizer))

	_, err = r.finalize(t.Context())
	require.NoError(t, err, "finalizing twice must be a no-op")
}

// A failure to read the other modules is transient, not an invalid spec:
// recording it as SpecValid=False would strand the module, since the status
// patch is the only thing that would re-trigger it.
func TestValidateSpec_returnsAListFailureInsteadOfMarkingTheSpecInvalid(t *testing.T) {
	t.Parallel()
	mod := testModule()
	r := newReconciler(t, fake.NewClientBuilder().WithScheme(scheme(t)).Build(),
		clusters.NewRegistry(), testResolver(), mod)
	r.opts.ListModules = func(context.Context, string) ([]pmdeployv1alpha1.OCMModule, error) {
		return nil, assert.AnError
	}

	cont, err := r.validateSpec(t.Context())
	require.ErrorIs(t, err, assert.AnError)

	assert.False(t, cont)
	assert.Nil(t, meta.FindStatusCondition(mod.Status.Conditions, ConditionSpecValid),
		"a transient read failure must not be recorded as an invalid spec")
}
