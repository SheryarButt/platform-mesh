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

package memberships

import (
	"context"
	"fmt"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"
	"go.platform-mesh.io/tenancy-operator/pkg/clusters"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"
	"go.platform-mesh.io/tenancy-operator/pkg/paths"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/kcp-dev/logicalcluster/v3"
)

// rbacFinalizer holds the Membership until its role binding is gone.
//
// The single most important thing in this package. Grant and revoke are the same
// reconciler, and a Membership that disappears without removing its binding
// leaves LIVE ACCESS behind with no record that it exists — the failure mode is
// silent and permanent, and no amount of reconciling the remaining objects finds
// it, because there is nothing left pointing at it.
const rbacFinalizer = "membership.tenancy.platform-mesh.io/rbac"

// applyRBAC projects a Membership into RBAC in its target workspace.
type applyRBAC struct {
	// platform reads Users from the directory: a Membership names a User by its
	// digest name, but a role binding must name the rbacIdentity kcp will actually
	// see. Those are different strings, deliberately.
	platform mcmanager.Manager

	// access writes the ClusterRole and ClusterRoleBinding. Both the Tenant
	// and the child Workspace types bind this export by default.
	access mcmanager.Manager

	// resolver mirrors kcp's username convention, which is what a role binding
	// must name.
	resolver *identity.Resolver

	layout paths.Layout
}

func (r *applyRBAC) Name() string { return pmtenancyv1alpha1.MembershipConditionRBACApplied }

func (r *applyRBAC) FinalizerName() string { return rbacFinalizer }

func (r *applyRBAC) Reconcile(ctx context.Context, cl ctrlruntimeclient.Client, m *pmtenancyv1alpha1.Membership) (chain.Status, error) {
	subject, err := r.rbacIdentity(ctx, m)
	if err != nil {
		chain.MarkFalse(m, r.Name(), "Pending", err.Error())
		return chain.StopAndRequeue, nil
	}

	targetClusters, err := r.targetClusters(ctx, cl, m)
	if err != nil {
		chain.MarkFalse(m, r.Name(), "Pending", err.Error())
		return chain.StopAndRequeue, nil
	}

	roleName, err := clusterRoleFor(m.Spec.Role)
	if err != nil {
		// A role outside the enum. Terminal: no retry fixes a bad spec.
		chain.MarkFalse(m, r.Name(), "Invalid", err.Error())
		return chain.Stop, nil
	}
	rules, err := rulesFor(m.Spec.Role)
	if err != nil {
		chain.MarkFalse(m, r.Name(), "Invalid", err.Error())
		return chain.Stop, nil
	}

	for _, targetCluster := range targetClusters {
		if err := r.bind(ctx, targetCluster, roleName, rules, subject, m); err != nil {
			chain.MarkFalse(m, r.Name(), "Pending", err.Error())
			return chain.StopAndRequeue, nil
		}
	}

	// Recorded only when it identifies ONE place. A project-scope Membership binds
	// in exactly one cluster, so the field is the answer to "where is this
	// enforced". A tenant-scope Membership binds in every Project of the tenant — a
	// set that grows — and there is no cluster to name: it is left empty rather
	// than filled with the first element of a list whose order is a listing's
	// order. The delete path re-derives the set, so nothing depends on it.
	//
	// The empty case is also the NORMAL one at bootstrap: the owner's tenant-scope
	// Membership is written before any Project exists, so targetClusters is empty
	// and there is genuinely nothing bound yet. Indexing into it here panicked.
	m.Status.ClusterID = ""
	if m.Spec.Scope == pmtenancyv1alpha1.MembershipScopeProject && len(targetClusters) == 1 {
		m.Status.ClusterID = targetClusters[0]
	}
	chain.MarkTrue(m, r.Name())
	return chain.Continue, nil
}

// bind writes the ClusterRole and its binding into one workspace.
func (r *applyRBAC) bind(ctx context.Context, targetCluster, roleName string, rules []rbacv1.PolicyRule, subject string, m *pmtenancyv1alpha1.Membership) error {
	target, err := clusters.ClientForCluster(ctx, r.access, targetCluster)
	if err != nil {
		return fmt.Errorf("workspace %s not reachable through the tenancy-access export yet", targetCluster)
	}

	// The ClusterRole is ensured per target rather than bootstrapped once: project
	// workspaces are created at runtime, so there is no install-time moment at
	// which to put it there.
	role := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}}
	if _, err := controllerutil.CreateOrUpdate(ctx, target, role, func() error {
		role.Rules = rules
		return nil
	}); err != nil {
		return fmt.Errorf("ensuring ClusterRole %s in %s: %w", roleName, targetCluster, err)
	}

	owner, err := tenantClusterOf(m)
	if err != nil {
		return err
	}

	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: bindingName(m.Name)}}
	if _, err := controllerutil.CreateOrUpdate(ctx, target, binding, func() error {
		// A back-reference to the Membership that owns this grant, and a finalizer
		// so we still get to read it.
		//
		// The binding sits in a project workspace; its Membership lives one tier
		// up, in the Tenant. A delete event arrives AFTER the object is gone,
		// so without these there is nothing left to say which Membership to repair
		// — only the name, which does not identify the cluster to look in. The
		// finalizer turns the delete into an update, with the labels still readable.
		if binding.Labels == nil {
			binding.Labels = map[string]string{}
		}
		binding.Labels[labelMembership] = m.Name
		binding.Labels[pmtenancyv1alpha1.LabelTenant] = owner
		controllerutil.AddFinalizer(binding, bindingFinalizer)

		// RoleRef is immutable, so it is only correct to set it while creating. A
		// role change on an existing Membership is handled by deleting and
		// recreating below.
		binding.RoleRef = rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     roleName,
		}
		binding.Subjects = []rbacv1.Subject{{
			APIGroup: rbacv1.GroupName,
			Kind:     rbacv1.UserKind,
			Name:     subject,
		}}
		return nil
	}); err != nil {
		if !apierrors.IsInvalid(err) {
			return fmt.Errorf("ensuring the role binding for Membership %s in %s: %w", m.Name, targetCluster, err)
		}
		// roleRef is immutable: the Membership's role changed. Recreate rather than
		// leaving the old grant in place, which would be a silent failure to
		// downgrade someone.
		if err := target.Delete(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return fmt.Errorf("role changed; recreating the binding in %s", targetCluster)
	}

	return nil
}

// Finalize removes the binding. See rbacFinalizer for why this is the important half.
func (r *applyRBAC) Finalize(ctx context.Context, cl ctrlruntimeclient.Client, m *pmtenancyv1alpha1.Membership) (chain.Status, error) {
	targetClusters, err := r.targetClusters(ctx, cl, m)
	if err != nil || len(targetClusters) == 0 {
		// Nothing resolvable left to revoke from — the Tenant or Project is
		// already gone, and the bindings went with it.
		return chain.Continue, nil //nolint:nilerr // nothing was granted
	}

	for _, targetCluster := range targetClusters {
		if status, err := r.revoke(ctx, cl, m, targetCluster); err != nil || status != chain.Continue {
			return status, err
		}
	}
	return chain.Continue, nil
}

// revoke removes this Membership's binding from one workspace.
func (r *applyRBAC) revoke(ctx context.Context, cl ctrlruntimeclient.Client, m *pmtenancyv1alpha1.Membership, targetCluster string) (chain.Status, error) {
	if targetCluster == "" {
		// The caller already resolved the target list, so an empty entry means
		// there was never anything to revoke here.
		return chain.Continue, nil
	}

	target, err := clusters.ClientForCluster(ctx, r.access, targetCluster)
	if err != nil {
		// Unreachable is normally not "gone", and holding the finalizer is what
		// keeps a grant from outliving its record. But holding it unconditionally
		// deadlocks a workspace teardown: the target is a Project's workspace, and
		// if the Project that named it is gone then so is the workspace and every
		// binding in it — so we would block a deletion waiting to clean up something
		// the deletion already removed.
		//
		// There used to be a second arm here, releasing when the binding lived in
		// the SAME cluster as the Membership. That case cannot arise any more:
		// nothing is bound in the Tenant workspace (see targetClusters), so a
		// binding is never co-located with the Membership that owns it.
		//
		// The test is "does a live Project still claim this cluster", which covers
		// both scopes with one question. A tenant-scope Membership spans every Project,
		// so asking only about spec.project would leave it spinning forever against a
		// cluster that no longer exists — the same deadlock one tier out.
		live, liveErr := projectClaimsCluster(ctx, cl, targetCluster)
		if liveErr == nil && !live {
			return chain.Continue, nil
		}

		// Genuinely a transient miss. Record why, because a finalizer that reports
		// nothing is a deletion that hangs with no explanation — which is how the
		// deadlock above stayed invisible until someone read kcp's own conditions.
		chain.MarkFalse(m, r.Name(), "Revoking",
			fmt.Sprintf("waiting to reach %s to remove the role binding", targetCluster))
		return chain.StopAndRequeue, nil
	}

	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: bindingName(m.Name)}}
	// Our own finalizer would otherwise hold the binding we are trying to revoke.
	if err := releaseBinding(ctx, target, bindingName(m.Name)); err != nil {
		return chain.StopAndRequeue, err
	}
	if err := target.Delete(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
		if meta.IsNoMatchError(err) || apierrors.IsForbidden(err) {
			// The whole workspace is going away, taking its RBAC with it.
			return chain.Continue, nil
		}
		return chain.StopAndRequeue, err
	}

	// The ClusterRole is deliberately left: it is shared by every Membership in
	// this workspace, and removing it here would revoke everyone else.
	return chain.Continue, nil
}

// rbacIdentity resolves the Membership's User to the username kcp will see.
func (r *applyRBAC) rbacIdentity(ctx context.Context, m *pmtenancyv1alpha1.Membership) (string, error) {
	directory, err := clusters.ClientForCluster(ctx, r.platform, r.layout.Directory)
	if err != nil {
		return "", fmt.Errorf("directory workspace not reachable yet")
	}

	user := &pmtenancyv1alpha1.User{}
	if err := directory.Get(ctx, ctrlruntimeclient.ObjectKey{Name: m.Spec.User}, user); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("user %s does not exist", m.Spec.User)
		}
		return "", err
	}
	// Computed from the CURRENT convention rather than read from
	// spec.rbacIdentity. The stored field is the mirror and the User controller
	// keeps it fresh, but a binding built from a mirror that has not been
	// refreshed yet grants nobody anything while looking entirely correct — and
	// that failure is only visible as a 403 somewhere else entirely.
	subject, err := r.resolver.RBACIdentity(identity.Claims{
		Issuer:  user.Spec.Issuer,
		Subject: user.Spec.Subject,
		Email:   user.Spec.Email,
	})
	if err != nil {
		return "", fmt.Errorf("user %s: %w", m.Spec.User, err)
	}
	return subject, nil
}

// targetClusters resolves every cluster this Membership must be bound in.
//
// An PROJECT-scope Membership binds in exactly one place: the Project it names.
//
// An TENANT-scope Membership binds in every Project workspace of that Tenant,
// and NOWHERE ELSE. Tenant admin means admin everywhere in the tenant, and kcp asks only
// "is there a binding here" — so the implication has to be materialized, one
// binding per Project.
//
// NOTHING IS EVER BOUND IN THE TENANT WORKSPACE ITSELF. That tier holds the
// Memberships and Projects that decide access, and a tenant who could reach it
// could rewrite them — bypassing every guard the tenancy virtual workspace applies
// on the way in, because kcp would be answering a question the VW never saw. The
// Tenant tier is unreachable to tenants not because a check refuses them but
// because no binding there names them, which survives a new client, a forgotten
// code path and a direct front-proxy call. Granting a tenant access here is not a
// stronger version of tenant-admin; it is the removal of the property the tier exists
// for. Membership and Project writes go through the virtual workspace, which holds
// the only credential for this cluster.
//
// This is why the controller watches Projects. A binding written once when the
// Membership appeared would silently fail to cover every Project created
// afterwards — a tenant admin locked out of the workspace they just made.
func (r *applyRBAC) targetClusters(ctx context.Context, cl ctrlruntimeclient.Client, m *pmtenancyv1alpha1.Membership) ([]string, error) {
	if m.Spec.Scope == pmtenancyv1alpha1.MembershipScopeProject {
		if m.Spec.Project == "" {
			return nil, fmt.Errorf("scope=project requires spec.project")
		}
		// The Project carries its own cluster ID once its workspace is Ready, so
		// this never touches a kcp Workspace object — the Project IS the handle.
		proj := &pmtenancyv1alpha1.Project{}
		if err := cl.Get(ctx, ctrlruntimeclient.ObjectKey{Name: m.Spec.Project}, proj); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("project %s does not exist", m.Spec.Project)
			}
			return nil, err
		}
		if proj.Status.ClusterID == "" {
			return nil, fmt.Errorf("project %s has no workspace yet", m.Spec.Project)
		}
		return []string{proj.Status.ClusterID}, nil
	}

	var targets []string

	list := &pmtenancyv1alpha1.ProjectList{}
	if err := cl.List(ctx, list); err != nil {
		return nil, fmt.Errorf("listing Projects to expand a tenant-scope Membership: %w", err)
	}
	for i := range list.Items {
		proj := &list.Items[i]
		// A Project with no workspace yet is skipped rather than waited for: the
		// other grants should not be held up, and the Project's own event brings us
		// back when it is Ready.
		if proj.Status.ClusterID != "" && proj.DeletionTimestamp.IsZero() {
			targets = append(targets, proj.Status.ClusterID)
		}
	}
	return targets, nil
}

// projectClaimsCluster reports whether a Project that is not being deleted still
// names this logical cluster.
//
// False means the workspace behind it is gone, so there is nothing left to revoke
// and holding the finalizer would only stall a deletion. An error is NOT false:
// failing to list must leave the finalizer held, because "I could not check" and
// "it is gone" have opposite consequences.
func projectClaimsCluster(ctx context.Context, cl ctrlruntimeclient.Client, clusterID string) (bool, error) {
	list := &pmtenancyv1alpha1.ProjectList{}
	if err := cl.List(ctx, list); err != nil {
		return false, err
	}
	for i := range list.Items {
		proj := &list.Items[i]
		if proj.Status.ClusterID == clusterID && proj.DeletionTimestamp.IsZero() {
			return true, nil
		}
	}
	return false, nil
}

// tenantClusterOf reads the logical cluster the Membership lives in.
//
// From the object's own kcp.io/cluster annotation, which kcp stamps on
// everything it stores — so this needs no API call and cannot disagree with where
// the object actually is.
func tenantClusterOf(m *pmtenancyv1alpha1.Membership) (string, error) {
	cluster := logicalcluster.From(m)
	if cluster.Empty() {
		return "", fmt.Errorf("membership %s carries no cluster annotation", m.Name)
	}
	return cluster.String(), nil
}
