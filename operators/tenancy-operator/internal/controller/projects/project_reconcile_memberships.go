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

package projects

import (
	"context"
	"fmt"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// projectMembershipsFinalizer keeps the Project around long enough to delete the
// grants that name it.
const projectMembershipsFinalizer = "project.tenancy.platform-mesh.io/memberships"

// projectMemberships deletes the project-scope Memberships naming this Project
// when it goes away.
//
// This is the cost of storing Memberships one tier up (§Where tenancy state
// lives). A Membership lives in the TENANT's workspace, so deleting an
// Project destroys its workspace and everything inside it while leaving every
// grant that pointed at it untouched — dangling objects referring to a place that
// no longer exists, which nothing else will ever clean up.
//
// It could not be an ownerReference. Owner-based garbage collection would be the
// obvious answer, and both objects do live in the same logical cluster, but
// nothing else in this operator relies on kcp running a garbage collector and a
// cleanup path that silently does nothing is worse than none.
//
// ORDER MATTERS, and it is expressed by where this step sits in the chain rather
// than by anything here: chain.RunFinalize walks steps in REVERSE, so this must be
// registered AFTER the workspace step to finalize BEFORE it. That way the
// Memberships are deleted while their target workspace still exists, and their own
// finalizers can remove the role bindings properly instead of relying on the
// workspace teardown to take the bindings with it.
type projectMemberships struct{}

func (r *projectMemberships) Name() string {
	return pmtenancyv1alpha1.ProjectConditionMembershipsPruned
}

func (r *projectMemberships) FinalizerName() string { return projectMembershipsFinalizer }

// Reconcile does nothing but hold the finalizer that Finalize needs.
//
// There is deliberately no live-state check here. Counting the grants that name
// this Project on every pass would be a list per reconcile to maintain a number
// nothing reads, and a Membership pointing at a Project is not a problem worth
// reporting — it is the normal case.
func (r *projectMemberships) Reconcile(_ context.Context, _ ctrlruntimeclient.Client, proj *pmtenancyv1alpha1.Project) (chain.Status, error) {
	chain.MarkTrue(proj, r.Name())
	return chain.Continue, nil
}

// Finalize deletes every project-scope Membership naming this Project, and waits
// for them to actually go.
//
// Waiting rather than firing-and-forgetting is the point: each Membership holds
// finalizers of its own that remove role bindings and prune index rows, and
// letting the workspace be destroyed underneath them turns an orderly revoke into
// a race that usually works. Neither of those finalizers can deadlock against this
// one — both have a release path for a target that is already gone — so the wait
// terminates.
func (r *projectMemberships) Finalize(ctx context.Context, cl ctrlruntimeclient.Client, proj *pmtenancyv1alpha1.Project) (chain.Status, error) {
	// Memberships live in the same logical cluster as the Project: both are in the
	// Tenant's workspace, served by the `tenancy` export. So this is an
	// ordinary list, not a cross-cluster reach.
	list := &pmtenancyv1alpha1.MembershipList{}
	if err := cl.List(ctx, list); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || apierrors.IsForbidden(err) {
			// The Tenant's workspace is going too, taking its Memberships with
			// it. Nothing to prune and nothing to wait for.
			return chain.Continue, nil
		}
		return chain.StopAndRequeue, fmt.Errorf("listing memberships to prune: %w", err)
	}

	remaining := 0
	for i := range list.Items {
		m := &list.Items[i]
		if m.Spec.Scope != pmtenancyv1alpha1.MembershipScopeProject || m.Spec.Project != proj.Name {
			continue
		}
		remaining++
		if !m.DeletionTimestamp.IsZero() {
			// Already on its way out; its own finalizers are what we are waiting for.
			continue
		}
		if err := cl.Delete(ctx, m); err != nil && !apierrors.IsNotFound(err) {
			return chain.StopAndRequeue, fmt.Errorf("deleting membership %s: %w", m.Name, err)
		}
	}

	if remaining > 0 {
		// Say what is being waited for. A deletion that hangs with no explanation is
		// how the last cascade deadlock stayed invisible until someone read kcp's own
		// conditions.
		chain.MarkFalse(proj, r.Name(), "Pruning",
			fmt.Sprintf("waiting for %d membership(s) naming this project to be revoked", remaining))
		return chain.StopAndRequeue, nil
	}

	return chain.Continue, nil
}
