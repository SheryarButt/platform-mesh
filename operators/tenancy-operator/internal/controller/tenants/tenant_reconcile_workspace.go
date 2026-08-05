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

package tenants

import (
	"context"
	"fmt"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/config"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"
	"go.platform-mesh.io/tenancy-operator/pkg/clusters"
	"go.platform-mesh.io/tenancy-operator/pkg/paths"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// tenantWorkspaceFinalizer holds the Tenant until its kcp workspace is gone,
// so a delete cascades rather than orphaning a whole logical cluster.
const tenantWorkspaceFinalizer = "tenant.tenancy.platform-mesh.io/workspace"

// tenantWorkspace creates and deletes the Tenant's kcp workspace.
//
// kcp workspace creation is asynchronous and has no rollback, so every step
// re-checks for existing state before creating and requeues rather than blocking.
// That makes "idempotent, with a reconciler as the safety net" a hard requirement
// here, not a style choice.
type tenantWorkspace struct {
	// provisioner reads and writes through the tenancy-provisioner export, the
	// only path this operator has into the fleet root.
	provisioner mcmanager.Manager
	layout      paths.Layout
	cfg         config.TenancyConfig
}

func (r *tenantWorkspace) Name() string { return pmtenancyv1alpha1.TenantConditionWorkspaceReady }

func (r *tenantWorkspace) FinalizerName() string { return tenantWorkspaceFinalizer }

// The passed-in client is for the Tenant's own cluster and is unused: this
// step writes into the FLEET ROOT, which it reaches through its own manager.
func (r *tenantWorkspace) Reconcile(ctx context.Context, _ ctrlruntimeclient.Client, tenant *pmtenancyv1alpha1.Tenant) (chain.Status, error) {
	parent, err := clusters.ClientForCluster(ctx, r.provisioner, r.layout.TenantFleetRoot)
	if err != nil {
		// The fleet root binds tenancy-provisioner at install, and the provider's
		// cache warms asynchronously. A requeue is the right answer, not an error
		// that would mark WorkspaceReady False on every restart.
		chain.MarkFalse(tenant, r.Name(), "Pending", "tenant fleet root not reachable through the provisioner export yet")
		//nolint:nilerr // deliberate: not-yet-reachable is a wait, not a failure
		return chain.StopAndRequeue, nil
	}

	// A workspace with this name may still be tearing down from a previous
	// Tenant — the name is derived, so a deleted-and-recreated Tenant
	// lands on exactly the same one. Adopting it would mean waiting forever for an
	// object that is on its way out, and CreateOrUpdate cannot tell the difference.
	existing := &kcptenancyv1alpha1.Workspace{}
	if err := parent.Get(ctx, ctrlruntimeclient.ObjectKey{Name: tenant.Name}, existing); err == nil {
		if !existing.DeletionTimestamp.IsZero() {
			chain.MarkFalse(tenant, r.Name(), "Pending",
				fmt.Sprintf("workspace %s is still being deleted; waiting before recreating it", tenant.Name))
			return chain.StopAndRequeue, nil
		}
	} else if !apierrors.IsNotFound(err) {
		chain.MarkFalse(tenant, r.Name(), "Error", err.Error())
		return chain.StopAndRequeue, err
	}

	ws := &kcptenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: tenant.Name}}
	if _, err := controllerutil.CreateOrUpdate(ctx, parent, ws, func() error {
		if ws.Annotations == nil {
			ws.Annotations = map[string]string{}
		}
		// Display names live in metadata, never in a path: renaming an
		// Tenant must never move its workspace.
		ws.Annotations[pmtenancyv1alpha1.AnnotationDisplayName] = tenant.Spec.DisplayName

		if ws.Labels == nil {
			ws.Labels = map[string]string{}
		}
		ws.Labels[pmtenancyv1alpha1.LabelTenant] = tenant.Name

		// spec.type is immutable after creation, so only set it while creating.
		if ws.Spec.Type == nil {
			ws.Spec.Type = &kcptenancyv1alpha1.WorkspaceTypeReference{
				Name: kcptenancyv1alpha1.WorkspaceTypeName(r.cfg.TenantWorkspaceType),
				Path: r.layout.Exports,
			}
		}
		return nil
	}); err != nil {
		err = fmt.Errorf("creating workspace %s: %w", r.layout.Tenant(tenant.Name), err)
		chain.MarkFalse(tenant, r.Name(), "Error", err.Error())
		return chain.StopAndRequeue, err
	}

	tenant.Status.WorkspacePath = r.layout.Tenant(tenant.Name)

	if ws.Status.Phase != kcpcorev1alpha1.LogicalClusterPhaseReady {
		chain.MarkFalse(tenant, r.Name(), "Pending",
			fmt.Sprintf("workspace %s is %s", tenant.Status.WorkspacePath, phaseOrUnknown(ws.Status.Phase)))
		return chain.StopAndRequeue, nil
	}

	// Clients address a workspace by cluster ID, never by name. Resolving it once
	// here saves every reader the lookup.
	tenant.Status.ClusterID = ws.Spec.Cluster

	chain.MarkTrue(tenant, r.Name())
	return chain.Continue, nil
}

// finalize deletes the Tenant's workspace, which cascades everything inside
// it — child Workspaces, APIBindings, Memberships, RBAC, ServiceAccounts. There is
// no per-resource teardown code because there does not need to be.
func (r *tenantWorkspace) Finalize(ctx context.Context, _ ctrlruntimeclient.Client, tenant *pmtenancyv1alpha1.Tenant) (chain.Status, error) {
	parent, err := clusters.ClientForCluster(ctx, r.provisioner, r.layout.TenantFleetRoot)
	if err != nil {
		// Same reasoning as reconcile: wait, do not fail. Failing here would drop
		// the finalizer's guarantee that the workspace is actually gone.
		//nolint:nilerr // deliberate: not-yet-reachable is a wait, not a failure
		return chain.StopAndRequeue, nil
	}

	ws := &kcptenancyv1alpha1.Workspace{}
	if err := parent.Get(ctx, ctrlruntimeclient.ObjectKey{Name: tenant.Name}, ws); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			// Gone: the finalizer has nothing left to guard.
			return chain.Continue, nil
		}
		return chain.StopAndRequeue, err
	}

	if ws.GetDeletionTimestamp() == nil {
		if err := parent.Delete(ctx, ws); err != nil && !apierrors.IsNotFound(err) {
			return chain.StopAndRequeue, err
		}
	}

	// Hold the finalizer until the workspace is actually gone, not merely asked to
	// go — otherwise the Tenant disappears while its logical cluster is
	// still being torn down.
	return chain.StopAndRequeue, nil
}

func phaseOrUnknown(p kcpcorev1alpha1.LogicalClusterPhaseType) string {
	if p == "" {
		return "not scheduled yet"
	}
	return string(p)
}
