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

package users

import (
	"context"
	"fmt"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/config"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"
	"go.platform-mesh.io/tenancy-operator/pkg/clusters"
	"go.platform-mesh.io/tenancy-operator/pkg/membership"
	"go.platform-mesh.io/tenancy-operator/pkg/naming"
	"go.platform-mesh.io/tenancy-operator/pkg/paths"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// seedProject creates the first Project inside the personal Tenant.
//
// It runs in the USER chain rather than the Tenant one because the intent
// lives on the User (spec.tenancy.seedProject). A Tenant created by an
// invitation should not sprout a workspace just because some member wanted one
// for their own personal tenant.
type seedProject struct {
	naming naming.Strategy

	// tenancy reaches `memberships`, which live in the Tenant workspace and
	// belong to a different export than the Workspace object itself.
	tenancy mcmanager.Manager

	layout paths.Layout
	cfg    config.TenancyConfig
}

func (r *seedProject) Name() string { return pmtenancyv1alpha1.UserConditionSeedProjectReady }

func (r *seedProject) Reconcile(ctx context.Context, cl ctrlruntimeclient.Client, user *pmtenancyv1alpha1.User) (chain.Status, error) {
	if !r.cfg.PersonalTenantsEnabled || !user.Spec.Tenancy.SeedTenant || !user.Spec.Tenancy.SeedProject {
		// Nothing asked for. A steady state, so True rather than absent — an
		// observer should be able to tell "not wanted" from "not done yet".
		chain.MarkTrue(user, r.Name())
		return chain.Continue, nil
	}

	if user.Status.DefaultProject != "" {
		// Already seeded. ONE-SHOT, exactly like the Tenant above: if the
		// User deleted this Workspace, it stays deleted.
		chain.MarkTrue(user, r.Name())
		return chain.Continue, nil
	}

	if user.Status.DefaultTenant == "" {
		// The previous step stops the chain when it cannot make the Tenant,
		// so reaching here without one means seeding is off for this User.
		chain.MarkTrue(user, r.Name())
		return chain.Continue, nil
	}

	tenant := &pmtenancyv1alpha1.Tenant{}
	if err := cl.Get(ctx, ctrlruntimeclient.ObjectKey{Name: user.Status.DefaultTenant}, tenant); err != nil {
		if apierrors.IsNotFound(err) {
			// The Tenant was seeded and then deleted. Nothing rebuilds it, so
			// there will never be anywhere to put this Workspace: terminal, not
			// pending. Requeuing here would retry forever against a User who simply
			// removed their personal tenant.
			chain.MarkTrue(user, r.Name())
			return chain.Continue, nil
		}
		chain.MarkFalse(user, r.Name(), "Error", err.Error())
		return chain.StopAndRequeue, err
	}

	// The Tenant's workspace has to exist before anything can be created
	// inside it, and status.clusterID is set only once it is Ready.
	if tenant.Status.ClusterID == "" {
		chain.MarkFalse(user, r.Name(), "Pending",
			fmt.Sprintf("Tenant %s has no workspace yet", tenant.Name))
		return chain.StopAndRequeue, nil
	}

	tenantCluster, err := clusters.ClientForCluster(ctx, r.tenancy, tenant.Status.ClusterID)
	if err != nil {
		// The binding is created with the workspace, but the provider's cache warms
		// asynchronously. A wait, not a failure.
		chain.MarkFalse(user, r.Name(), "Pending", "Tenant workspace not reachable through the tenancy export yet")
		//nolint:nilerr // deliberate: not-yet-reachable is a wait, not a failure
		return chain.StopAndRequeue, nil
	}

	// A Project, not a kcp Workspace: Project IS the work tier in this API, and
	// the workspace behind it is the Project reconciler's business. Seeding a raw
	// Workspace here would create one nothing owns and nothing lists.
	//
	// Seeded for the same reason the Tenant above is: the create and the
	// pointer that records it are two writes, so a requeue between them must adopt
	// rather than seed a second Project.
	name, err := naming.Seeded(r.naming,
		naming.Request{Kind: naming.KindProject, DisplayName: r.displayName(), Seed: user.Name},
		func(candidateName string) (bool, error) {
			proj := &pmtenancyv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{
					Name:        candidateName,
					Annotations: map[string]string{pmtenancyv1alpha1.AnnotationSeededFor: user.Name},
				},
				Spec: pmtenancyv1alpha1.ProjectSpec{
					DisplayName: r.displayName(),
				},
			}

			err := tenantCluster.Create(ctx, proj)
			if err == nil {
				return true, nil
			}
			if !apierrors.IsAlreadyExists(err) {
				return false, fmt.Errorf("creating the seed Project in Tenant %s: %w", tenant.Name, err)
			}

			existing := &pmtenancyv1alpha1.Project{}
			if err := tenantCluster.Get(ctx, ctrlruntimeclient.ObjectKey{Name: candidateName}, existing); err != nil {
				return false, fmt.Errorf("inspecting Project %q in Tenant %s: %w", candidateName, tenant.Name, err)
			}
			return existing.Annotations[pmtenancyv1alpha1.AnnotationSeededFor] == user.Name, nil
		},
	)
	if err != nil {
		chain.MarkFalse(user, r.Name(), "Error", err.Error())
		return chain.StopAndRequeue, err
	}

	// The grant that makes the workspace usable. Without it the tenant owns a
	// workspace they are refused in — the Tenant-tier Membership carries
	// implicit admin in children, but only once something projects it into RBAC,
	// and an explicit row is what the Membership reconciler will act on.
	if err := r.seedMembership(ctx, tenant, user.Name, name); err != nil {
		chain.MarkFalse(user, r.Name(), "Error", err.Error())
		return chain.StopAndRequeue, err
	}

	// Recorded without waiting for the workspace to be Ready: the guard means "I
	// already made this", and withholding it until Ready would seed a second one
	// if this reconcile lost its race.
	user.Status.DefaultProject = name

	chain.MarkTrue(user, r.Name())
	return chain.Continue, nil
}

func (r *seedProject) displayName() string {
	if r.cfg.SeedProjectDisplayName == "" {
		return "default"
	}
	return r.cfg.SeedProjectDisplayName
}

// seedMembership writes the project-scope admin row for the seeded Project.
//
// Stored in the TENANT workspace, not the child: a Membership names its
// target but always lives one tier up, so managing access never requires access
// to the thing being managed.
func (r *seedProject) seedMembership(ctx context.Context, tenant *pmtenancyv1alpha1.Tenant, userName, projectName string) error {
	cl, err := clusters.ClientForCluster(ctx, r.tenancy, tenant.Status.ClusterID)
	if err != nil {
		return fmt.Errorf("reaching Tenant %s through the tenancy export: %w", tenant.Name, err)
	}

	m := &pmtenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{
			Name: membership.Name(userName, pmtenancyv1alpha1.MembershipScopeProject, projectName),
		},
		Spec: pmtenancyv1alpha1.MembershipSpec{
			User:    userName,
			Scope:   pmtenancyv1alpha1.MembershipScopeProject,
			Project: projectName,
			Role:    pmtenancyv1alpha1.MembershipRoleAdmin,
		},
	}
	if err := cl.Create(ctx, m); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating the seed Project Membership in Tenant %s: %w", tenant.Name, err)
	}
	return nil
}
