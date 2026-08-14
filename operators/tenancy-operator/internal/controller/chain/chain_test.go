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

package chain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// step is a scriptable chain step. Tenant is used as the object because it
// already implements chain.Object; nothing here depends on what a Tenant
// means.
type step struct {
	name      string
	status    chain.Status
	err       error
	finalizer string
	finStatus chain.Status
	finErr    error

	ran    int
	final  int
	mutate func(*pmtenancyv1alpha1.Tenant)
}

func (s *step) Name() string { return s.name }

func (s *step) Reconcile(_ context.Context, _ ctrlruntimeclient.Client, tenant *pmtenancyv1alpha1.Tenant) (chain.Status, error) {
	s.ran++
	if s.mutate != nil {
		s.mutate(tenant)
	}
	return s.status, s.err
}

// finalizing wraps step with the optional half of the interface, so a test can
// have steps that do and do not finalize in one chain.
type finalizing struct{ *step }

func (f *finalizing) FinalizerName() string { return f.finalizer }

func (f *finalizing) Finalize(_ context.Context, _ ctrlruntimeclient.Client, _ *pmtenancyv1alpha1.Tenant) (chain.Status, error) {
	f.final++
	return f.finStatus, f.finErr
}

func testClient(t *testing.T, objs ...ctrlruntimeclient.Object) ctrlruntimeclient.Client {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(pmtenancyv1alpha1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&pmtenancyv1alpha1.Tenant{}).Build()
}

func tenant(name string) *pmtenancyv1alpha1.Tenant {
	return &pmtenancyv1alpha1.Tenant{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestRunStopsWhereTheStepSaysTo(t *testing.T) {
	for name, tc := range map[string]struct {
		first       chain.Status
		wantSecond  int
		wantRequeue bool
	}{
		"continue runs the rest": {chain.Continue, 1, false},
		"stop ends quietly":      {chain.Stop, 0, false},
		"stop and requeue":       {chain.StopAndRequeue, 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			a := &step{name: "A", status: tc.first}
			b := &step{name: "B"}

			requeue, err := chain.Run(context.Background(), testClient(t), tenant("o"),
				[]chain.Reconciler[*pmtenancyv1alpha1.Tenant]{a, b})

			require.NoError(t, err)
			assert.Equal(t, 1, a.ran)
			assert.Equal(t, tc.wantSecond, b.ran)
			assert.Equal(t, tc.wantRequeue, requeue)
		})
	}
}

// A failing step must not hide the state of the ones after it: it records its own
// condition and the chain carries on, aggregating errors. One broken step
// reporting for all of them is how a stalled object becomes unreadable.
func TestRunAggregatesErrorsWithoutStopping(t *testing.T) {
	boom := errors.New("boom")
	a := &step{name: "A", err: boom}
	b := &step{name: "B"}

	_, err := chain.Run(context.Background(), testClient(t), tenant("o"),
		[]chain.Reconciler[*pmtenancyv1alpha1.Tenant]{a, b})

	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, b.ran, "a later independent step must still run")
}

// Finalizers run in REVERSE order. A step's teardown may depend on state an
// earlier step created, so tearing down forwards would pull the ground out from
// under the steps that follow.
func TestRunFinalizeRunsInReverse(t *testing.T) {
	var order []string
	mk := func(n string) *finalizing {
		s := &step{name: n, finalizer: n + ".example.com"}
		f := &finalizing{step: s}
		s.mutate = nil
		return f
	}
	a, b := mk("A"), mk("B")

	// Record order through the shared slice.
	steps := []chain.Reconciler[*pmtenancyv1alpha1.Tenant]{
		&orderedFinalizer{finalizing: a, order: &order},
		&orderedFinalizer{finalizing: b, order: &order},
	}

	o := tenant("o")
	// Only steps whose finalizer is actually ON the object are torn down — a
	// finalizer that was never added means the step never created anything.
	chain.EnsureFinalizers(o, steps)

	_, err := chain.RunFinalize(context.Background(), testClient(t), o, steps)
	require.NoError(t, err)
	assert.Equal(t, []string{"B", "A"}, order)
}

// The other half of that rule: a step whose finalizer is absent is skipped
// entirely, so an interrupted delete never runs teardown for state that was
// never built.
func TestRunFinalizeSkipsStepsWithoutTheirFinalizer(t *testing.T) {
	s := &step{name: "A", finalizer: "a.example.com"}
	steps := []chain.Reconciler[*pmtenancyv1alpha1.Tenant]{&finalizing{step: s}}

	_, err := chain.RunFinalize(context.Background(), testClient(t), tenant("o"), steps)
	require.NoError(t, err)
	assert.Zero(t, s.final)
}

type orderedFinalizer struct {
	*finalizing
	order *[]string
}

func (f *orderedFinalizer) Finalize(ctx context.Context, cl ctrlruntimeclient.Client, o *pmtenancyv1alpha1.Tenant) (chain.Status, error) {
	*f.order = append(*f.order, f.Name())
	return f.finalizing.Finalize(ctx, cl, o)
}

// A finalizer is removed only once its step reports Continue. Removing it while
// teardown is still pending would let the object go and orphan whatever the step
// created.
func TestRunFinalizeKeepsTheFinalizerUntilTeardownIsDone(t *testing.T) {
	s := &step{name: "A", finalizer: "a.example.com", finStatus: chain.StopAndRequeue}
	f := &finalizing{step: s}
	steps := []chain.Reconciler[*pmtenancyv1alpha1.Tenant]{f}

	o := tenant("o")
	chain.EnsureFinalizers(o, steps)
	require.Contains(t, o.Finalizers, "a.example.com")

	requeue, err := chain.RunFinalize(context.Background(), testClient(t), o, steps)
	require.NoError(t, err)
	assert.True(t, requeue)
	assert.Contains(t, o.Finalizers, "a.example.com", "teardown is not done, so the object must not be released")

	// Now it finishes.
	s.finStatus = chain.Continue
	_, err = chain.RunFinalize(context.Background(), testClient(t), o, steps)
	require.NoError(t, err)
	assert.NotContains(t, o.Finalizers, "a.example.com")
}

// A failing teardown must not release the object — that is the direction where
// the damage is permanent. This is how every real step reports failure: the
// error rides along with StopAndRequeue.
func TestRunFinalizeHoldsOnAnError(t *testing.T) {
	s := &step{name: "A", finalizer: "a.example.com", finStatus: chain.StopAndRequeue, finErr: errors.New("nope")}
	steps := []chain.Reconciler[*pmtenancyv1alpha1.Tenant]{&finalizing{step: s}}

	o := tenant("o")
	chain.EnsureFinalizers(o, steps)
	requeue, err := chain.RunFinalize(context.Background(), testClient(t), o, steps)

	require.Error(t, err)
	assert.True(t, requeue)
	assert.Contains(t, o.Finalizers, "a.example.com")
}

// CURRENT BEHAVIOUR, pinned because it is sharp rather than because it is
// obviously right: the STATUS decides whether the finalizer goes, and the error
// is only reported. A step returning (Continue, err) is released despite having
// failed, which for a teardown means the object goes and whatever it did not
// clean up is orphaned.
//
// No step in this operator does that today — they all pair an error with
// StopAndRequeue — so this documents the contract rather than a live defect. If
// the framework should instead refuse to release on any error, this is the test
// to invert.
func TestRunFinalizeReleasesWhenAStepReturnsContinueWithAnError(t *testing.T) {
	s := &step{name: "A", finalizer: "a.example.com", finStatus: chain.Continue, finErr: errors.New("nope")}
	steps := []chain.Reconciler[*pmtenancyv1alpha1.Tenant]{&finalizing{step: s}}

	o := tenant("o")
	chain.EnsureFinalizers(o, steps)
	_, err := chain.RunFinalize(context.Background(), testClient(t), o, steps)

	require.Error(t, err)
	assert.NotContains(t, o.Finalizers, "a.example.com")
}

func TestEnsureFinalizersOnlyAddsForFinalizingSteps(t *testing.T) {
	plain := &step{name: "plain"}
	fin := &finalizing{step: &step{name: "fin", finalizer: "fin.example.com"}}
	steps := []chain.Reconciler[*pmtenancyv1alpha1.Tenant]{plain, fin}

	o := tenant("o")
	assert.True(t, chain.EnsureFinalizers(o, steps))
	assert.Equal(t, []string{"fin.example.com"}, o.Finalizers)

	// Idempotent: nothing to add the second time, so nothing to patch.
	assert.False(t, chain.EnsureFinalizers(o, steps))
}

// Ready is the aggregate an operator actually looks at, so each way of being
// not-ready must be distinguishable by reason rather than by absence.
func TestSetReady(t *testing.T) {
	ready := pmtenancyv1alpha1.TenantConditionReady

	t.Run("no steps is False, not True", func(t *testing.T) {
		o := tenant("o")
		chain.SetReady(o, ready, nil)

		c := meta.FindStatusCondition(o.Status.Conditions, ready)
		require.NotNil(t, c)
		assert.Equal(t, metav1.ConditionFalse, c.Status)
		assert.Equal(t, "NoSteps", c.Reason, "a fully disabled reconciler must not look finished")
	})

	t.Run("a step that never reported is Pending", func(t *testing.T) {
		o := tenant("o")
		chain.SetReady(o, ready, []chain.Reconciler[*pmtenancyv1alpha1.Tenant]{&step{name: "A"}})

		c := meta.FindStatusCondition(o.Status.Conditions, ready)
		require.NotNil(t, c)
		assert.Equal(t, "Pending", c.Reason)
	})

	t.Run("a failed step is Incomplete and names itself", func(t *testing.T) {
		o := tenant("o")
		chain.MarkFalse(o, "A", "Boom", "it broke")
		chain.SetReady(o, ready, []chain.Reconciler[*pmtenancyv1alpha1.Tenant]{&step{name: "A"}})

		c := meta.FindStatusCondition(o.Status.Conditions, ready)
		require.NotNil(t, c)
		assert.Equal(t, "Incomplete", c.Reason)
		assert.Contains(t, c.Message, "A: it broke")
	})

	t.Run("all steps True is Ready", func(t *testing.T) {
		o := tenant("o")
		chain.MarkTrue(o, "A")
		chain.MarkTrue(o, "B")
		chain.SetReady(o, ready, []chain.Reconciler[*pmtenancyv1alpha1.Tenant]{
			&step{name: "A"}, &step{name: "B"},
		})

		c := meta.FindStatusCondition(o.Status.Conditions, ready)
		require.NotNil(t, c)
		assert.Equal(t, metav1.ConditionTrue, c.Status)
	})
}

func TestMarkTrueAndFalseCarryTheGeneration(t *testing.T) {
	o := tenant("o")
	o.Generation = 7

	chain.MarkFalse(o, "A", "Pending", "waiting")
	c := meta.FindStatusCondition(o.Status.Conditions, "A")
	require.NotNil(t, c)
	assert.Equal(t, int64(7), c.ObservedGeneration)
	assert.Equal(t, "waiting", c.Message)

	chain.MarkTrue(o, "A")
	c = meta.FindStatusCondition(o.Status.Conditions, "A")
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Empty(t, c.Message, "success must clear the failure message")
}

// A steady-state reconcile has to be read-only, or the operator feeds its own
// watch and never settles.
func TestCommitSkipsWhenNothingChanged(t *testing.T) {
	existing := tenant("o")
	existing.ResourceVersion = "1"
	cl := testClient(t, existing)

	current := &pmtenancyv1alpha1.Tenant{}
	require.NoError(t, cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: "o"}, current))
	original := current.DeepCopy()

	require.NoError(t, chain.Commit(context.Background(), cl, original, current))

	after := &pmtenancyv1alpha1.Tenant{}
	require.NoError(t, cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: "o"}, after))
	assert.Equal(t, current.ResourceVersion, after.ResourceVersion, "an unchanged object must not be written")
}

// Status is a subresource: one update would silently drop half the change
// depending on which endpoint it reached, so Commit has to write both.
func TestCommitWritesBothMetadataAndStatus(t *testing.T) {
	cl := testClient(t, tenant("o"))

	current := &pmtenancyv1alpha1.Tenant{}
	require.NoError(t, cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: "o"}, current))
	original := current.DeepCopy()

	current.Finalizers = append(current.Finalizers, "x.example.com")
	chain.MarkTrue(current, "A")

	require.NoError(t, chain.Commit(context.Background(), cl, original, current))

	after := &pmtenancyv1alpha1.Tenant{}
	require.NoError(t, cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: "o"}, after))
	assert.Contains(t, after.Finalizers, "x.example.com")
	assert.NotNil(t, meta.FindStatusCondition(after.Status.Conditions, "A"))
}

func TestIgnoreNotFound(t *testing.T) {
	gr := schema.GroupResource{Group: "tenancy.platform-mesh.io", Resource: "tenants"}
	assert.NoError(t, chain.IgnoreNotFound(apierrors.NewNotFound(gr, "gone")))

	boom := errors.New("boom")
	assert.ErrorIs(t, chain.IgnoreNotFound(boom), boom)
	assert.NoError(t, chain.IgnoreNotFound(nil))
}

func TestRequeueResult(t *testing.T) {
	assert.Zero(t, chain.RequeueResult(false).RequeueAfter)
	assert.Positive(t, chain.RequeueResult(true).RequeueAfter,
		"a requeue must carry a delay, or the queue spins")
}
