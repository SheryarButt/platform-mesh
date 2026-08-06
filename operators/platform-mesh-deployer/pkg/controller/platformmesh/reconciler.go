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
	"errors"
	"fmt"
	"time"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// reconciler holds everything resolved during a single pass: the loaded
// PlatformMesh and the copy that is mutated. One instance per Reconcile, never
// reused, so a step can hand a value to a later step without threading it
// through every signature.
type reconciler struct {
	opts Options
	req  reconcile.Request

	old *pmdeployv1alpha1.PlatformMesh
	pm  *pmdeployv1alpha1.PlatformMesh

	// requeueAfter is set by a step waiting on something no watch covers.
	requeueAfter time.Duration
}

func (r *reconciler) reconcile(ctx context.Context) (reconcile.Result, error) {
	if cont, err := r.fetchPlatformMesh(ctx); err != nil || !cont {
		return reconcile.Result{}, err
	}

	// Steps mutate r.pm, so the status is committed even when one of them
	// fails — the failure is recorded on the object.
	err := r.run(ctx)
	meta.SetStatusCondition(&r.pm.Status.Conditions, readyCondition(r.pm, err))
	err = errors.Join(err, r.commitStatus(ctx))
	if err != nil {
		// A returned error discards Result and retries with backoff, so
		// keeping the requeue would only make controller-runtime warn.
		return reconcile.Result{}, err
	}
	return reconcile.Result{RequeueAfter: r.requeueAfter}, nil
}

func (r *reconciler) run(ctx context.Context) error {
	if cont, err := r.awaitPreTopology(ctx); err != nil || !cont {
		return err
	}
	if cont, err := r.reconcileTopology(ctx); err != nil || !cont {
		return err
	}
	if cont, err := r.reconcileExposure(ctx); err != nil || !cont {
		return err
	}
	// The root structure is only provisioned where this deployer also runs
	// the provisioner controller.
	if r.opts.KcpConfig != nil {
		if cont, err := r.reconcileRootStructure(ctx); err != nil || !cont {
			return err
		}
	}
	r.pm.Status.ResolvedVersion = r.pm.Spec.Version
	return nil
}

func (r *reconciler) fetchPlatformMesh(ctx context.Context) (bool, error) {
	pm, err := r.opts.GetPlatformMesh(ctx, r.req.NamespacedName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("getting PlatformMesh: %w", err)
	}
	r.old, r.pm = pm, pm.DeepCopy()
	return true, nil
}

func (r *reconciler) commitStatus(ctx context.Context) error {
	if equality.Semantic.DeepEqual(r.old.Status, r.pm.Status) {
		return nil
	}
	return r.opts.PatchStatus(ctx, r.old, r.pm)
}
