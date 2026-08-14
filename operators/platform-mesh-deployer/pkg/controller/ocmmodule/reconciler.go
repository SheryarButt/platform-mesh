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
	"errors"
	"fmt"
	"time"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	pmocmmodule "go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// reconciler holds everything resolved during a single pass. One instance per
// Reconcile, never reused, so a step can hand a value to a later step without
// threading it through every signature.
type reconciler struct {
	opts Options
	req  reconcile.Request

	old *pmdeployv1alpha1.OCMModule
	mod *pmdeployv1alpha1.OCMModule

	pm        *pmdeployv1alpha1.PlatformMesh
	resolved  *pmocmmodule.Resolved
	instances []pmocmmodule.Instance
	// endpoints are published by the provisioner once the kcp side is done.
	endpoints map[string]string

	// requeueAfter is set by a step waiting on something no watch covers.
	requeueAfter time.Duration
}

func (r *reconciler) reconcile(ctx context.Context) (reconcile.Result, error) {
	if cont, err := r.fetchModule(ctx); err != nil || !cont {
		return reconcile.Result{}, err
	}

	if r.mod.DeletionTimestamp != nil {
		return r.finalize(ctx)
	}
	if cont, err := r.ensureFinalizer(ctx); err != nil || !cont {
		return reconcile.Result{}, err
	}

	// Steps past this point mutate r.mod, so the status is committed even
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
	if cont, err := r.validateSpec(ctx); err != nil || !cont {
		return err
	}
	if cont, err := r.fetchPlatformMesh(ctx); err != nil || !cont {
		return err
	}
	if cont, err := r.awaitTopology(); err != nil || !cont {
		return err
	}
	if cont, err := r.awaitDependencies(ctx); err != nil || !cont {
		return err
	}
	if cont, err := r.resolveComponent(ctx); err != nil || !cont {
		return err
	}
	if cont, err := r.fanOut(); err != nil || !cont {
		return err
	}
	if cont, err := r.awaitSetup(ctx); err != nil || !cont {
		return err
	}
	return r.deployInstances(ctx)
}

func (r *reconciler) fetchModule(ctx context.Context) (bool, error) {
	mod, err := r.opts.GetModule(ctx, r.req.NamespacedName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("getting OCMModule: %w", err)
	}
	r.old, r.mod = mod, mod.DeepCopy()
	return true, nil
}

// validateSpec rejects a spec no reconcile can satisfy. Recording it and
// returning nil is deliberate: only an edit fixes such a spec, and that
// re-triggers the watch, so retrying would spin forever. A failure to read the
// other modules is not that, and is returned so it is retried.
func (r *reconciler) validateSpec(ctx context.Context) (bool, error) {
	if err := validate(r.mod); err != nil {
		setCondition(r.mod, specInvalid(r.mod.Generation, err))
		return false, nil
	}
	cycle, err := r.detectCycle(ctx, r.mod)
	if err != nil {
		return false, err
	}
	if cycle != nil {
		setCondition(r.mod, specInvalid(r.mod.Generation, cycle))
		return false, nil
	}
	setCondition(r.mod, specValid(r.mod.Generation))
	return true, nil
}

func (r *reconciler) fetchPlatformMesh(ctx context.Context) (bool, error) {
	pm, err := r.platformMesh(ctx, r.mod)
	if err != nil {
		return false, err
	}
	r.pm = pm
	return true, nil
}

// awaitTopology gates on the PlatformMesh, which is watched, so it stops
// without a requeue. A pre-topology module skips it: the topology is what
// waits for that one.
func (r *reconciler) awaitTopology() (bool, error) {
	if r.mod.Spec.Stage != pmdeployv1alpha1.StagePostTopology {
		return true, nil
	}
	if ready, reason := topologyReady(r.pm); !ready {
		setCondition(r.mod, gatedOn(r.mod.Generation, "WaitingForTopology", reason))
		return false, nil
	}
	return true, nil
}

// awaitDependencies gates on other OCMModules, which a watch maps back to their
// dependents, so it stops without a requeue.
func (r *reconciler) awaitDependencies(ctx context.Context) (bool, error) {
	if ok, reason := r.dependenciesReady(ctx, r.mod); !ok {
		setCondition(r.mod, gatedOn(r.mod.Generation, "WaitingForDependency", reason))
		return false, nil
	}
	setCondition(r.mod, dependenciesReady(r.mod.Generation))
	return true, nil
}

func (r *reconciler) resolveComponent(ctx context.Context) (bool, error) {
	resolved, err := r.opts.ResolveModule(ctx, r.mod, &r.pm.Spec.OCM)
	if err != nil {
		setCondition(r.mod, resolveFailed(r.mod.Generation, err))
		return false, err
	}
	setCondition(r.mod, componentResolved(r.mod.Generation))
	r.resolved = resolved
	r.mod.Status.ResolvedDigest = resolved.Digest()
	return true, nil
}

func (r *reconciler) fanOut() (bool, error) {
	instances, err := r.opts.FanOut(r.mod)
	if err != nil {
		return false, fmt.Errorf("fanning out over the engaged clusters: %w", err)
	}
	r.instances = instances
	return true, nil
}

// awaitSetup writes the OCMModuleSetup handshake and waits for the provisioner.
// The OCMModuleSetup is owned by this OCMModule and therefore watched, so it stops
// without a requeue.
func (r *reconciler) awaitSetup(ctx context.Context) (bool, error) {
	if err := r.ensureSetup(ctx); err != nil {
		if errors.Is(err, errSetupPending) {
			setCondition(r.mod, deployPending(r.mod.Generation, "WaitingForSetup", err.Error()))
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *reconciler) deployInstances(ctx context.Context) error {
	if err := r.deploy(ctx); err != nil {
		// kcp-operator and cert-manager mint these asynchronously and
		// nothing watches the Secrets, so waiting for one polls.
		if errors.Is(err, errKubeconfigPending) || errors.Is(err, errServingCertPending) || errors.Is(err, errRequestHeaderCAPending) {
			setCondition(r.mod, deployPending(r.mod.Generation, "WaitingForKubeconfig", err.Error()))
			r.requeueAfter = r.opts.Requeue
			return nil
		}
		setCondition(r.mod, deployFailed(r.mod.Generation, err))
		return err
	}
	setCondition(r.mod, deployed(r.mod.Generation))
	return nil
}

// setReady writes the aggregate last, from the conditions the steps wrote and
// the error the chain stopped on.
func (r *reconciler) setReady(err error) {
	meta.SetStatusCondition(&r.mod.Status.Conditions, readyCondition(r.mod, err))
}

func (r *reconciler) commitStatus(ctx context.Context) error {
	if equality.Semantic.DeepEqual(r.old.Status, r.mod.Status) {
		return nil
	}
	return r.opts.PatchStatus(ctx, r.old, r.mod)
}
