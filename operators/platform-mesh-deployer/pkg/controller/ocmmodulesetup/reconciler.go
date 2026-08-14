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
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// reconciler holds everything resolved during a single pass: the loaded
// objects, the kcp connection and the copy that is mutated. One instance per
// Reconcile, never reused, so a step can hand a value to a later step without
// threading it through every signature.
type reconciler struct {
	opts Options
	req  reconcile.Request
	log  logr.Logger

	old   *pmdeployv1alpha1.OCMModuleSetup
	setup *pmdeployv1alpha1.OCMModuleSetup
	pm    *pmdeployv1alpha1.PlatformMesh
	cfg   *rest.Config

	// requeueAfter is set by a step waiting on something no watch covers.
	requeueAfter time.Duration
}

func (r *reconciler) reconcile(ctx context.Context) (reconcile.Result, error) {
	if cont, err := r.fetchModuleSetup(ctx); err != nil || !cont {
		return reconcile.Result{}, err
	}

	if r.setup.DeletionTimestamp != nil {
		return r.finalize(ctx)
	}
	if cont, err := r.ensureFinalizer(ctx); err != nil || !cont {
		return reconcile.Result{}, err
	}

	// Steps past this point mutate r.setup, so the status is committed even
	// when one of them fails — the failure is recorded on the object.
	err := r.run(ctx)
	r.setReady(err)
	err = errors.Join(err, r.commitStatus(ctx))
	if err != nil {
		// A returned error discards Result and retries with backoff, so
		// keeping the requeue would only make controller-runtime warn.
		return reconcile.Result{}, err
	}
	return reconcile.Result{RequeueAfter: r.requeueAfter}, nil
}

func (r *reconciler) run(ctx context.Context) error {
	if cont, err := r.fetchPlatformMesh(ctx); err != nil || !cont {
		return err
	}
	if cont, err := r.awaitRootStructure(); err != nil || !cont {
		return err
	}
	if cont, err := r.connectKcp(ctx); err != nil || !cont {
		return err
	}
	if cont, err := r.provisionWorkspaces(ctx); err != nil || !cont {
		return err
	}
	r.publishEndpoints()
	return nil
}

func (r *reconciler) fetchModuleSetup(ctx context.Context) (bool, error) {
	setup, err := r.opts.GetModuleSetup(ctx, r.req.NamespacedName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("getting OCMModuleSetup: %w", err)
	}
	r.old, r.setup = setup, setup.DeepCopy()
	return true, nil
}

func (r *reconciler) fetchPlatformMesh(ctx context.Context) (bool, error) {
	key := ctrlruntimeclient.ObjectKey{Namespace: r.setup.Namespace, Name: r.setup.Spec.PlatformMeshRef.Name}
	pm, err := r.opts.GetPlatformMesh(ctx, key)
	if err != nil {
		return false, fmt.Errorf("getting PlatformMesh %q: %w", key.Name, err)
	}
	r.pm = pm
	return true, nil
}

// setReady writes the aggregate last, from the conditions the steps wrote and
// the error the chain stopped on.
func (r *reconciler) setReady(err error) {
	provisioned := meta.FindStatusCondition(r.setup.Status.Conditions, ConditionWorkspacesProvisioned)
	meta.SetStatusCondition(&r.setup.Status.Conditions, readyCondition(r.setup.Generation, provisioned, err))
}

func (r *reconciler) commitStatus(ctx context.Context) error {
	if equality.Semantic.DeepEqual(r.old.Status, r.setup.Status) {
		return nil
	}
	return r.opts.PatchStatus(ctx, r.old, r.setup)
}
