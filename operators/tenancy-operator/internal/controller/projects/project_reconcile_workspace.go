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

	"github.com/kcp-dev/logicalcluster/v3"
	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// workspaceFinalizer holds the Project until its workspace is gone, so deleting
// a Project cascades rather than orphaning a logical cluster and everything a
// tenant put in it.
const workspaceFinalizer = "project.tenancy.platform-mesh.io/workspace"

// projectWorkspace materializes the kcp Workspace behind a Project.
//
// The Workspace is never exposed: a client creates a Project and this makes the
// workspace, which is what keeps kcp concepts from leaking into the tenant API.
//
// Where it goes depends on nesting. A top-level Project's workspace is created in
// the Tenant's own cluster; a sub-project's is created inside its parent's
// workspace, which is why the `workspace` WorkspaceType binds the provisioner
// export and allows itself as a child.
type projectWorkspace struct {
	// provisioner carries the `create workspaces` claim, in the Tenant
	// workspace and in every Project workspace.
	provisioner mcmanager.Manager
	layout      paths.Layout
	cfg         config.TenancyConfig
}

func (r *projectWorkspace) Name() string {
	return pmtenancyv1alpha1.ProjectConditionWorkspaceReady
}

func (r *projectWorkspace) FinalizerName() string { return workspaceFinalizer }

func (r *projectWorkspace) Reconcile(ctx context.Context, _ ctrlruntimeclient.Client, proj *pmtenancyv1alpha1.Project) (chain.Status, error) {
	tenantCluster, err := tenantClusterOf(proj)
	if err != nil {
		chain.MarkFalse(proj, r.Name(), "Pending", err.Error())
		return chain.StopAndRequeue, nil
	}

	parent, err := clusters.ClientForCluster(ctx, r.provisioner, tenantCluster)
	if err != nil {
		chain.MarkFalse(proj, r.Name(), "Pending", "Tenant workspace not reachable through the provisioner export yet")
		return chain.StopAndRequeue, nil //nolint:nilerr // provider caches warm asynchronously
	}

	// A workspace of this name may still be tearing down from a previous Project.
	// Adopting it would mean waiting forever for something on its way out.
	existing := &kcptenancyv1alpha1.Workspace{}
	if err := parent.Get(ctx, ctrlruntimeclient.ObjectKey{Name: proj.Name}, existing); err == nil {
		if !existing.DeletionTimestamp.IsZero() {
			chain.MarkFalse(proj, r.Name(), "Pending",
				fmt.Sprintf("workspace %s is still being deleted; waiting before recreating it", proj.Name))
			return chain.StopAndRequeue, nil
		}
	} else if !apierrors.IsNotFound(err) {
		chain.MarkFalse(proj, r.Name(), "Error", err.Error())
		return chain.StopAndRequeue, err
	}

	ws := &kcptenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: proj.Name}}
	if _, err := controllerutil.CreateOrUpdate(ctx, parent, ws, func() error {
		if ws.Annotations == nil {
			ws.Annotations = map[string]string{}
		}
		// Display names live in metadata, never in a path: renaming a Project must
		// never move its workspace.
		ws.Annotations[pmtenancyv1alpha1.AnnotationDisplayName] = proj.Spec.DisplayName

		// spec.type is immutable after creation, so only set it while creating.
		if ws.Spec.Type == nil {
			ws.Spec.Type = &kcptenancyv1alpha1.WorkspaceTypeReference{
				Name: kcptenancyv1alpha1.WorkspaceTypeName(r.cfg.ProjectWorkspaceType),
				Path: r.layout.Exports,
			}
		}
		return nil
	}); err != nil {
		err = fmt.Errorf("creating the workspace for Project %s: %w", proj.Name, err)
		chain.MarkFalse(proj, r.Name(), "Error", err.Error())
		return chain.StopAndRequeue, err
	}

	if ws.Status.Phase != kcpcorev1alpha1.LogicalClusterPhaseReady {
		chain.MarkFalse(proj, r.Name(), "Pending",
			fmt.Sprintf("workspace for Project %s is %s", proj.Name, phaseOrUnknown(ws.Status.Phase)))
		return chain.StopAndRequeue, nil
	}

	proj.Status.ClusterID = ws.Spec.Cluster
	proj.Status.WorkspacePath = ws.Spec.URL

	chain.MarkTrue(proj, r.Name())
	return chain.Continue, nil
}

// Finalize deletes the Project's workspace, which cascades everything inside it —
// sub-project workspaces included, because they are children of this one.
func (r *projectWorkspace) Finalize(ctx context.Context, _ ctrlruntimeclient.Client, proj *pmtenancyv1alpha1.Project) (chain.Status, error) {
	tenantCluster, err := tenantClusterOf(proj)
	if err != nil {
		// The Tenant is already gone, taking every project workspace with it.
		return chain.Continue, nil //nolint:nilerr // nothing left to delete
	}

	parent, err := clusters.ClientForCluster(ctx, r.provisioner, tenantCluster)
	if err != nil {
		// Unreachable is not "gone": hold the finalizer rather than orphan a cluster.
		return chain.StopAndRequeue, nil //nolint:nilerr // wait for the cache
	}

	ws := &kcptenancyv1alpha1.Workspace{}
	if err := parent.Get(ctx, ctrlruntimeclient.ObjectKey{Name: proj.Name}, ws); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return chain.Continue, nil
		}
		return chain.StopAndRequeue, err
	}

	if ws.GetDeletionTimestamp() == nil {
		if err := parent.Delete(ctx, ws); err != nil && !apierrors.IsNotFound(err) {
			return chain.StopAndRequeue, err
		}
	}

	// Hold until it is actually gone, not merely asked to go.
	return chain.StopAndRequeue, nil
}

// tenantClusterOf reads the Tenant cluster this Project lives in, which is
// also where its workspace goes: Projects are a flat list, so every project
// workspace is a sibling under the Tenant.
func tenantClusterOf(proj *pmtenancyv1alpha1.Project) (string, error) {
	cluster := logicalcluster.From(proj)
	if cluster.Empty() {
		return "", fmt.Errorf("project %s carries no cluster annotation", proj.Name)
	}
	return cluster.String(), nil
}

func phaseOrUnknown(p kcpcorev1alpha1.LogicalClusterPhaseType) string {
	if p == "" {
		return "not scheduled yet"
	}
	return string(p)
}
