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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func stubOptions() Options {
	return Options{
		GetPlatformMesh: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			return simplePlatformMesh(), nil
		},
		PatchStatus: func(context.Context, *pmdeployv1alpha1.PlatformMesh, *pmdeployv1alpha1.PlatformMesh) error {
			return nil
		},
		ListModules: func(context.Context, string) ([]pmdeployv1alpha1.OCMModule, error) { return nil, nil },
		GetTemplate: func(context.Context, ctrlruntimeclient.ObjectKey, ctrlruntimeclient.Object) error { return nil },
		Apply: func(context.Context, *pmdeployv1alpha1.PlatformMesh, ctrlruntimeclient.Object, func()) error {
			return nil
		},
		Teardown: func(context.Context, *pmdeployv1alpha1.PlatformMesh, string, ctrlruntimeclient.ObjectList, map[string]struct{}) error {
			return nil
		},
		ClustersFor:    func(string, string) []clusters.Cluster { return nil },
		RegistryEvents: func() <-chan event.GenericEvent { return nil },
	}
}

func TestValidate_rejectsAMissingDependency(t *testing.T) {
	t.Parallel()
	opts := stubOptions()
	opts.ClustersFor = nil

	_, err := NewController(opts)
	require.ErrorContains(t, err, "ClustersFor is required")
}

// The root structure step is in the chain only when both kcp funcs are set;
// half a pair would silently skip it or panic.
func TestValidate_rejectsHalfOfTheKcpPair(t *testing.T) {
	t.Parallel()
	opts := stubOptions()
	opts.KcpConfig = func(context.Context, *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) { return nil, nil }

	_, err := NewController(opts)
	require.ErrorContains(t, err, "must be set together")
}

func TestValidate_defaultsTheRequeue(t *testing.T) {
	t.Parallel()
	opts := stubOptions()
	require.NoError(t, opts.validate())
	assert.Equal(t, defaultRequeue, opts.Requeue)
}

func TestRun_skipsTheRootStructureWithoutKcp(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	pm.Spec.Version = "1.2.3"
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")
	r := newReconciler(t, newClient(t, pm, rootShardTemplate(), shardTemplate()), reg, pm)

	require.NoError(t, r.run(t.Context()))

	assert.Nil(t, meta.FindStatusCondition(pm.Status.Conditions, ConditionRootStructureProvisioned),
		"a deployer without the provisioner must not claim anything about the root structure")
	assert.Equal(t, "1.2.3", pm.Status.ResolvedVersion)
}

func TestRun_stopsBeforeTheTopologyWhilePreTopologyModulesArePending(t *testing.T) {
	t.Parallel()
	pm := simplePlatformMesh()
	opts := stubOptions()
	opts.ListModules = func(context.Context, string) ([]pmdeployv1alpha1.OCMModule, error) {
		mod := pmdeployv1alpha1.OCMModule{ObjectMeta: metav1.ObjectMeta{Name: "etcd", Namespace: "pm"}}
		mod.Spec.PlatformMeshRef.Name = pm.Name
		mod.Spec.Stage = pmdeployv1alpha1.StagePreTopology
		return []pmdeployv1alpha1.OCMModule{mod}, nil
	}
	opts.Apply = func(context.Context, *pmdeployv1alpha1.PlatformMesh, ctrlruntimeclient.Object, func()) error {
		t.Fatal("the topology must not render before its pre-topology modules are ready")
		return nil
	}
	require.NoError(t, opts.validate())
	r := &reconciler{opts: opts, old: pm.DeepCopy(), pm: pm}

	require.NoError(t, r.run(t.Context()))
	assert.Zero(t, r.requeueAfter, "OCMModules are watched, so this must not poll")
	assert.Empty(t, pm.Status.ResolvedVersion, "the version is only resolved by a complete pass")
}

func TestReadyCondition_reportsTheFirstUnmetStep(t *testing.T) {
	t.Parallel()
	pm := simplePlatformMesh()
	meta.SetStatusCondition(&pm.Status.Conditions, preTopologyReady(pm.Generation))
	meta.SetStatusCondition(&pm.Status.Conditions, topologyFailed(pm.Generation, assert.AnError))
	meta.SetStatusCondition(&pm.Status.Conditions, exposureReady(pm.Generation, "applied"))

	cond := readyCondition(pm, nil)

	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "RenderFailed", cond.Reason, "the first unmet step is the reason, not the last")
	assert.Equal(t, pm.Generation, cond.ObservedGeneration)
}

// A step that fails before writing its own condition would otherwise leave a
// stale True from an earlier pass, and modules would keep deploying.
func TestReadyCondition_isFalseWhenTheChainErroredDespiteGreenSteps(t *testing.T) {
	t.Parallel()
	pm := simplePlatformMesh()
	for _, c := range []metav1.Condition{
		preTopologyReady(pm.Generation),
		topologyReady(pm.Generation),
		exposureReady(pm.Generation, "applied"),
		rootStructureProvisioned(pm.Generation),
	} {
		meta.SetStatusCondition(&pm.Status.Conditions, c)
	}

	cond := readyCondition(pm, assert.AnError)

	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "Error", cond.Reason)
	assert.Contains(t, cond.Message, assert.AnError.Error())
}

func TestReadyCondition_isTrueOnceEveryStepIsGreen(t *testing.T) {
	t.Parallel()
	pm := simplePlatformMesh()
	for _, c := range []metav1.Condition{
		preTopologyReady(pm.Generation),
		topologyReady(pm.Generation),
		exposureReady(pm.Generation, "applied"),
		rootStructureProvisioned(pm.Generation),
	} {
		meta.SetStatusCondition(&pm.Status.Conditions, c)
	}

	cond := readyCondition(pm, nil)

	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "Ready", cond.Reason)
}

// A deployer that does not run the provisioner never writes the root structure
// condition, and must still reach Ready.
func TestReadyCondition_isTrueWithoutTheOptionalRootStructure(t *testing.T) {
	t.Parallel()
	pm := simplePlatformMesh()
	for _, c := range []metav1.Condition{
		preTopologyReady(pm.Generation),
		topologyReady(pm.Generation),
		exposureReady(pm.Generation, "applied"),
	} {
		meta.SetStatusCondition(&pm.Status.Conditions, c)
	}

	assert.Equal(t, metav1.ConditionTrue, readyCondition(pm, nil).Status)
}

func TestReadyCondition_isFalseBeforeAnyStepHasRun(t *testing.T) {
	t.Parallel()
	cond := readyCondition(simplePlatformMesh(), nil)

	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "NotReconciled", cond.Reason)
	assert.Contains(t, cond.Message, ConditionPreTopologyModulesReady)
}

func TestReconcile_isANoOpForADeletedPlatformMesh(t *testing.T) {
	t.Parallel()
	opts := stubOptions()
	opts.GetPlatformMesh = func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
		return nil, notFoundPlatformMesh()
	}
	c, err := NewController(opts)
	require.NoError(t, err)

	res, err := c.Reconcile(t.Context(), reconcileRequest())
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
}

func TestReconcile_commitsReadyOnEveryPass(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	pm.Spec.Version = "1.2.3"
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")

	var patched *pmdeployv1alpha1.PlatformMesh
	opts := configPlaneOptions(t, newClient(t, pm, rootShardTemplate(), shardTemplate()), reg)
	opts.PatchStatus = func(_ context.Context, _, current *pmdeployv1alpha1.PlatformMesh) error {
		patched = current
		return nil
	}
	c, err := NewController(opts)
	require.NoError(t, err)

	_, err = c.Reconcile(t.Context(), reconcileRequest())
	require.NoError(t, err)

	require.NotNil(t, patched)
	cond := meta.FindStatusCondition(patched.Status.Conditions, ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "1.2.3", patched.Status.ResolvedVersion)
}
