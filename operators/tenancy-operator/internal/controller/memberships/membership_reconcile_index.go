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
	"sort"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"
	"go.platform-mesh.io/tenancy-operator/pkg/clusters"
	"go.platform-mesh.io/tenancy-operator/pkg/paths"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// indexFinalizer keeps a Membership around long enough to remove its own row.
// Without it, revoking access would leave the subject's index advertising a place
// they can no longer reach — and since clients list Projects from the index, that
// is a workspace that appears in the UI and 403s when opened.
const indexFinalizer = "membership.tenancy.platform-mesh.io/index"

// membershipIndex maintains the subject's row in the per-User read model.
//
// ONE WRITER, BOTH SCOPES. Every row in a UserMembershipIndex corresponds to
// exactly one Membership, and this step owns it. An earlier arrangement had the
// Tenant reconciler write the row for its first admin, which covered the
// bootstrap path and nothing else: a second tenant-scope member, and every
// project-scope grant, got working RBAC and no row at all. kcp let them in and the
// tenancy API told them they had nothing — the one direction §2.2 says a
// disagreement should never take, because the API is what a client believes.
//
// Two writers was also unstable once roles became editable: the Tenant's row
// hardcoded `admin`, so demoting the owner would have left the two steps
// overwriting each other's answer forever.
//
// Runs AFTER the RBAC step, deliberately. The index must not advertise access
// before the role binding backing it exists, or a client renders a workspace that
// is not yet reachable.
type membershipIndex struct {
	// platform reaches the directory workspace, where the indexes live.
	platform mcmanager.Manager
	layout   paths.Layout
}

func (r *membershipIndex) Name() string { return pmtenancyv1alpha1.MembershipConditionIndexSynced }

func (r *membershipIndex) FinalizerName() string { return indexFinalizer }

func (r *membershipIndex) Reconcile(ctx context.Context, cl ctrlruntimeclient.Client, m *pmtenancyv1alpha1.Membership) (chain.Status, error) {
	directory, err := clusters.ClientForCluster(ctx, r.platform, r.layout.Directory)
	if err != nil {
		// Requeue WITHOUT the error: an informer that has not synced yet is the
		// normal state at startup, and surfacing it as a reconcile failure buries
		// the real ones under a log line that always fires. The condition carries
		// the reason instead.
		chain.MarkFalse(m, r.Name(), "Pending", "directory workspace not reachable yet")
		return chain.StopAndRequeue, nil //nolint:nilerr // deliberate: pending, not failed
	}

	entry, err := r.entryFor(ctx, cl, directory, m)
	if err != nil {
		chain.MarkFalse(m, r.Name(), "Pending", err.Error())
		return chain.StopAndRequeue, nil
	}

	umi := &pmtenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: m.Spec.User},
	}
	// No owner label: the index is NAMED for its User, so the name is already the
	// lookup key — and a 64-character digest exceeds the 63-byte limit on a label
	// value anyway.
	if _, err := controllerutil.CreateOrUpdate(ctx, directory, umi, func() error {
		umi.Spec.Entries = upsertEntry(umi.Spec.Entries, *entry)
		return nil
	}); err != nil {
		err = fmt.Errorf("upserting index for user %q: %w", m.Spec.User, err)
		chain.MarkFalse(m, r.Name(), "Error", err.Error())
		return chain.StopAndRequeue, err
	}

	if err := syncEntryCount(ctx, directory, umi); err != nil {
		chain.MarkFalse(m, r.Name(), "Error", err.Error())
		return chain.StopAndRequeue, err
	}

	chain.MarkTrue(m, r.Name())
	return chain.Continue, nil
}

// Finalize removes this Membership's row.
//
// Best-effort on a missing Tenant: if the Tenant is gone the row is
// pruned by ITS finalizer, which sweeps every index at once. Blocking here would
// deadlock the cascade — the Tenant is what is being deleted.
func (r *membershipIndex) Finalize(ctx context.Context, cl ctrlruntimeclient.Client, m *pmtenancyv1alpha1.Membership) (chain.Status, error) {
	directory, err := clusters.ClientForCluster(ctx, r.platform, r.layout.Directory)
	if err != nil {
		// Unreachable directory during a teardown is the cascade, not a fault.
		return chain.Continue, nil //nolint:nilerr // the Tenant's own finalizer prunes
	}

	tenantUUID, err := r.tenantUUID(ctx, directory, m)
	if err != nil {
		return chain.Continue, nil //nolint:nilerr // nothing identifiable to prune
	}

	umi := &pmtenancyv1alpha1.UserMembershipIndex{}
	if err := directory.Get(ctx, ctrlruntimeclient.ObjectKey{Name: m.Spec.User}, umi); err != nil {
		if apierrors.IsNotFound(err) {
			return chain.Continue, nil
		}
		return chain.StopAndRequeue, err
	}

	kept := make([]pmtenancyv1alpha1.MembershipIndexEntry, 0, len(umi.Spec.Entries))
	for _, e := range umi.Spec.Entries {
		if e.TenantUUID == tenantUUID && e.ProjectUUID == m.Spec.Project {
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == len(umi.Spec.Entries) {
		return chain.Continue, nil
	}

	umi.Spec.Entries = kept
	if err := directory.Update(ctx, umi); err != nil {
		return chain.StopAndRequeue, fmt.Errorf("pruning index %q: %w", umi.Name, err)
	}
	if err := syncEntryCount(ctx, directory, umi); err != nil {
		return chain.StopAndRequeue, err
	}
	return chain.Continue, nil
}

// entryFor builds the row this Membership is responsible for.
//
// Denormalized on purpose: a client renders a tenant/project switcher from the index
// alone, and a row that carried only UUIDs would need one lookup per row against a
// tier the client cannot reach.
func (r *membershipIndex) entryFor(
	ctx context.Context,
	cl, directory ctrlruntimeclient.Client,
	m *pmtenancyv1alpha1.Membership,
) (*pmtenancyv1alpha1.MembershipIndexEntry, error) {
	tenant, err := r.tenant(ctx, directory, m)
	if err != nil {
		return nil, err
	}

	entry := &pmtenancyv1alpha1.MembershipIndexEntry{
		TenantUUID:        tenant.Name,
		TenantDisplayName: tenant.Spec.DisplayName,
		TenantCreatedAt:   tenant.CreationTimestamp,
		TenantFirstAdmin:  tenant.Status.FirstAdmin,
		TenantClusterID:   tenant.Status.ClusterID,
		// The Membership's own role, NOT the Tenant's idea of one. This is
		// what makes `memberships set-role` show up in the index.
		Role:     m.Spec.Role,
		Personal: tenant.Spec.Personal,
	}

	if m.Spec.Scope != pmtenancyv1alpha1.MembershipScopeProject {
		// A tenant-scope row. It carries no projectUUID and reaches every Project in
		// the Tenant, which is the same implication applyRBAC materializes as
		// one binding per Project.
		return entry, nil
	}

	proj := &pmtenancyv1alpha1.Project{}
	if err := cl.Get(ctx, ctrlruntimeclient.ObjectKey{Name: m.Spec.Project}, proj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("project %s does not exist", m.Spec.Project)
		}
		return nil, err
	}
	entry.ProjectUUID = proj.Name
	entry.ProjectDisplayName = proj.Spec.DisplayName
	// What a kubeconfig's server URL points at. Empty until the Project's workspace
	// is Ready, and the row is still worth writing without it: the caller can see
	// the Project exists while it settles.
	entry.ProjectClusterID = proj.Status.ClusterID
	return entry, nil
}

// tenant resolves the Tenant a Membership belongs to.
//
// By CLUSTER, because that is the only link a Membership carries: it is stored in
// the Tenant's workspace and names no Tenant UUID. Listing the
// directory is cheap — one small, system-owned collection — and it avoids putting
// a denormalized UUID on the Membership that could disagree with where the object
// actually is.
func (r *membershipIndex) tenant(ctx context.Context, directory ctrlruntimeclient.Client, m *pmtenancyv1alpha1.Membership) (*pmtenancyv1alpha1.Tenant, error) {
	cluster, err := tenantClusterOf(m)
	if err != nil {
		return nil, err
	}

	list := &pmtenancyv1alpha1.TenantList{}
	if err := directory.List(ctx, list); err != nil {
		return nil, fmt.Errorf("listing tenants: %w", err)
	}
	for i := range list.Items {
		if list.Items[i].Status.ClusterID == cluster {
			return &list.Items[i], nil
		}
	}
	return nil, fmt.Errorf("no tenant resolves to cluster %s yet", cluster)
}

// tenantUUID is the name-only form, for the delete path.
func (r *membershipIndex) tenantUUID(ctx context.Context, directory ctrlruntimeclient.Client, m *pmtenancyv1alpha1.Membership) (string, error) {
	tenant, err := r.tenant(ctx, directory, m)
	if err != nil {
		return "", err
	}
	return tenant.Name, nil
}

// upsertEntry replaces the row with the same (tenant, project) key, or
// appends it, and keeps the list ordered so an unchanged reconcile produces an
// unchanged object — otherwise every pass would write, and every write would wake
// every watcher.
func upsertEntry(entries []pmtenancyv1alpha1.MembershipIndexEntry, e pmtenancyv1alpha1.MembershipIndexEntry) []pmtenancyv1alpha1.MembershipIndexEntry {
	replaced := false
	for i := range entries {
		if entries[i].TenantUUID == e.TenantUUID && entries[i].ProjectUUID == e.ProjectUUID {
			entries[i] = e
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].TenantUUID != entries[j].TenantUUID {
			return entries[i].TenantUUID < entries[j].TenantUUID
		}
		return entries[i].ProjectUUID < entries[j].ProjectUUID
	})
	return entries
}

// syncEntryCount keeps status.entryCount in step with the rows.
//
// A separate subresource write. Skipping it leaves an index reporting rows it no
// longer has, which reads as "you still belong to something" long after you do
// not.
func syncEntryCount(ctx context.Context, directory ctrlruntimeclient.Client, umi *pmtenancyv1alpha1.UserMembershipIndex) error {
	want := int32(len(umi.Spec.Entries)) //nolint:gosec // entry count cannot realistically overflow int32
	if umi.Status.EntryCount == want {
		return nil
	}
	umi.Status.EntryCount = want
	umi.Status.ObservedGeneration = umi.Generation
	if err := directory.Status().Update(ctx, umi); err != nil {
		return fmt.Errorf("updating index status for user %q: %w", umi.Name, err)
	}
	return nil
}
