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

package chain

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Status is what one step tells the chain to do next.
type Status int

const (
	// Continue runs the next step.
	Continue Status = iota

	// Stop ends the chain successfully. The remaining steps are
	// not reached, and nothing is requeued: the step is waiting on an event that
	// will bring us back.
	Stop

	// StopAndRequeue ends the chain and asks to be called again.
	// The workqueue's rate limiter decides when, which is why no step carries a
	// duration of its own — per-object backoff belongs to the queue.
	StopAndRequeue
)

// Object is any object of ours that reports conditions.
type Object interface {
	ctrlruntimeclient.Object
	GetConditions() []metav1.Condition
	SetConditions([]metav1.Condition)
}

// Reconciler is one step of a chain.
//
// Deliberately small: a step receives the object, mutates it, and says what
// should happen next. It does not patch, does not manage finalizers and does not
// decide the aggregate Ready — the controller owns all three, so a step can be
// read on its own.
type Reconciler[T Object] interface {
	// name is the condition type this step reports under, which is also how it
	// appears in `kubectl get -o yaml`. It is the step's public surface.
	Name() string

	// cl is a client for the object's OWN logical cluster, handed in rather than
	// derived from the context. Deriving it needs a cluster that only the
	// multicluster machinery puts there, and a step that quietly gets it wrong
	// fails at runtime with "no cluster in context" rather than at compile time.
	// A step needing some other cluster holds its own manager (see tenantWorkspace).
	Reconcile(ctx context.Context, cl ctrlruntimeclient.Client, obj T) (Status, error)
}

// Finalizing is a step with teardown to do before the object may go.
type Finalizing[T Object] interface {
	Reconciler[T]

	// finalizerName is added on first reconcile and removed once finalize
	// reports Continue.
	FinalizerName() string

	Finalize(ctx context.Context, cl ctrlruntimeclient.Client, obj T) (Status, error)
}

// Run runs steps in order, stopping on the first one that says to.
//
// Errors do NOT stop the chain by themselves — a step that fails records its own
// condition and returns, and later independent steps still run. That is why the
// errors are aggregated rather than returned at the first failure: one broken
// step should not hide the state of the others.
func Run[T Object](ctx context.Context, cl ctrlruntimeclient.Client, obj T, steps []Reconciler[T]) (requeue bool, err error) {
	var errs []error

	for _, step := range steps {
		status, err := step.Reconcile(ctx, cl, obj)
		if err != nil {
			errs = append(errs, err)
		}
		if status == StopAndRequeue {
			requeue = true
			break
		}
		if status == Stop {
			break
		}
	}

	return requeue, kerrors.NewAggregate(errs)
}

// RunFinalize runs teardown in REVERSE order and drops each finalizer as its
// step reports done.
//
// Reverse because the chain builds state forwards: the index references the
// workspace, so the index rows go before the workspace they point at. Removing a
// finalizer only on Continue is what makes deletion safe to interrupt — a process
// killed mid-teardown resumes at the same step rather than skipping it.
func RunFinalize[T Object](ctx context.Context, cl ctrlruntimeclient.Client, obj T, steps []Reconciler[T]) (requeue bool, err error) {
	var errs []error

	for i := len(steps) - 1; i >= 0; i-- {
		step, ok := steps[i].(Finalizing[T])
		if !ok {
			continue
		}
		if !controllerutil.ContainsFinalizer(obj, step.FinalizerName()) {
			continue
		}

		status, err := step.Finalize(ctx, cl, obj)
		if err != nil {
			errs = append(errs, err)
		}
		if status == StopAndRequeue {
			return true, kerrors.NewAggregate(errs)
		}
		if status == Stop {
			return false, kerrors.NewAggregate(errs)
		}

		controllerutil.RemoveFinalizer(obj, step.FinalizerName())
	}

	return requeue, kerrors.NewAggregate(errs)
}

// EnsureFinalizers adds any finalizer the chain declares and is missing.
//
// Reports whether it changed anything so the caller can patch and return: the
// object must carry its finalizers BEFORE any step creates external state, or a
// delete arriving in between orphans whatever that step made.
func EnsureFinalizers[T Object](obj T, steps []Reconciler[T]) bool {
	changed := false
	for _, s := range steps {
		f, ok := s.(Finalizing[T])
		if !ok {
			continue
		}
		if controllerutil.AddFinalizer(obj, f.FinalizerName()) {
			changed = true
		}
	}
	return changed
}

// MarkTrue records that a step succeeded.
func MarkTrue(obj Object, condType string) {
	setCondition(obj, condType, metav1.ConditionTrue, "Complete", "")
}

// MarkFalse records why a step did not.
//
// The message is the whole point of per-step conditions: a stalled object should
// say which step it is on and why, in `kubectl get -o yaml`, without anyone
// having to find the operator's logs.
func MarkFalse(obj Object, condType, reason, message string) {
	setCondition(obj, condType, metav1.ConditionFalse, reason, message)
}

func setCondition(obj Object, condType string, status metav1.ConditionStatus, reason, message string) {
	conds := obj.GetConditions()
	meta.SetStatusCondition(&conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: obj.GetGeneration(),
	})
	obj.SetConditions(conds)
}

// SetReady derives the aggregate from the per-step conditions.
//
// True only when every step is True. A step that has not reported yet leaves
// Ready False rather than absent, so "not finished" and "finished badly" are
// distinguishable by reason instead of by the shape of the object.
func SetReady[T Object](obj T, readyType string, steps []Reconciler[T]) {
	if len(steps) == 0 {
		// Every step is disabled, so nothing ran. Reporting Ready=True here would
		// be a lie that is very hard to see: the object looks finished and carries
		// no per-step condition to contradict it — which is exactly how a missing
		// default silently disabled a whole reconciler once.
		setCondition(obj, readyType, metav1.ConditionFalse, "NoSteps",
			"every step of this reconciler is disabled, so nothing was done")
		return
	}
	for _, s := range steps {
		c := meta.FindStatusCondition(obj.GetConditions(), s.Name())
		if c == nil {
			setCondition(obj, readyType, metav1.ConditionFalse, "Pending", "awaiting first reconciliation for "+s.Name())
			return
		}
		if c.Status != metav1.ConditionTrue {
			setCondition(obj, readyType, metav1.ConditionFalse, "Incomplete", s.Name()+": "+c.Message)
			return
		}
	}
	setCondition(obj, readyType, metav1.ConditionTrue, "Complete", "")
}

// Commit writes back whatever the chain changed.
//
// Metadata and status are two patches because status is a subresource: a single
// update would silently drop one half depending on which endpoint it reached.
// Both are skipped when nothing changed, so a steady-state reconcile is read-only
// and does not feed its own watch.
func Commit[T Object](ctx context.Context, cl ctrlruntimeclient.Client, original, current T) error {
	// Computed BEFORE any patch, which mutates current in place.
	changed := !equality.Semantic.DeepEqual(original, current)
	if !changed {
		return nil
	}

	// The main resource first — finalizers AND spec. Status is a subresource, so
	// an ordinary patch never touches it, which is what lets one call cover both.
	//
	// The patch body is computed rather than assumed: when only status changed it
	// is "{}", and sending that would be a write request on every steady-state
	// reconcile — enough to keep the object's resourceVersion churning and feed
	// its own watch.
	patch := ctrlruntimeclient.MergeFrom(original)
	body, err := patch.Data(current)
	if err != nil {
		return err
	}
	if len(body) > 0 && string(body) != "{}" {
		// Patched through a COPY, never through current.
		//
		// cl.Patch refreshes the object it is given from the server's response, and
		// the response to a main-resource patch carries the server's status — which
		// is the status we have not written yet. Passing current here silently
		// replaces the conditions and status fields this reconcile just computed,
		// and the Status().Patch below then finds nothing to write. The object ends
		// up with its side effects performed and NO status at all: a workspace that
		// exists, and a Tenant that never learns its cluster ID.
		mainObj, ok := current.DeepCopyObject().(ctrlruntimeclient.Object)
		if !ok {
			return fmt.Errorf("cannot copy %T for patching", current)
		}
		if err := cl.Patch(ctx, mainObj, patch); err != nil {
			return err
		}
		// Both sides move to the version the server just wrote, so the status patch
		// is computed against a current base rather than being rejected as a
		// conflict on every reconcile that also touched spec or a finalizer.
		original.SetResourceVersion(mainObj.GetResourceVersion())
		current.SetResourceVersion(mainObj.GetResourceVersion())
	}

	return cl.Status().Patch(ctx, current, ctrlruntimeclient.MergeFrom(original))
}

// IgnoreNotFound turns the one error every reconciler gets on a deleted object
// into a successful no-op.
func IgnoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// requeueDelay is how long a step that asked to be retried waits.
//
// A constant rather than a per-object exponential backoff: every use here is
// polling external state that settles in seconds (a workspace finishing
// Initializing, a provider cache warming), and error-driven backoff is already
// handled by the controller's own rate limiter.
const requeueDelay = 5 * time.Second

// RequeueResult maps the chain's answer onto controller-runtime's. RequeueAfter
// rather than Requeue, which is deprecated.
func RequeueResult(requeue bool) ctrl.Result {
	if requeue {
		return ctrl.Result{RequeueAfter: requeueDelay}
	}
	return ctrl.Result{}
}
