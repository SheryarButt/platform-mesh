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
	"strings"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/config"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"
	"go.platform-mesh.io/tenancy-operator/pkg/naming"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// seedTenant creates the personal Tenant for a User, once.
//
// ONE-SHOT: status.defaultTenant means "I already did this", and its mere presence
// ends the step. The Tenant is not fetched, not verified and not rebuilt.
// A User who deletes their personal Tenant has deleted it — re-creating it
// would make the platform argue with a deliberate act, and would resurrect a
// workspace tree the User asked to be rid of.
//
// It is not an access-control input and not a request-scoping default — there is
// no default workspace in this model.
type seedTenant struct {
	cfg    config.TenancyConfig
	naming naming.Strategy
}

func (r *seedTenant) Name() string { return pmtenancyv1alpha1.UserConditionSeedTenantReady }

func (r *seedTenant) Reconcile(ctx context.Context, cl ctrlruntimeclient.Client, user *pmtenancyv1alpha1.User) (chain.Status, error) {
	// Two gates, and the order matters: the operator-level switch can turn seeding
	// off fleet-wide, but never on for a User whose spec says no.
	if !r.cfg.PersonalTenantsEnabled || !user.Spec.Tenancy.SeedTenant {
		// The user must be invited into an existing Tenant before they can
		// create anything. That is a valid steady state, not a pending one, so the
		// condition is True rather than absent.
		chain.MarkTrue(user, r.Name())
		return chain.Continue, nil
	}

	if user.Status.DefaultTenant != "" {
		// Already seeded. Deliberately no existence check: whether the
		// Tenant is still there is not this step's business.
		chain.MarkTrue(user, r.Name())
		return chain.Continue, nil
	}

	tenant := &pmtenancyv1alpha1.Tenant{}

	// Seeded, not Apply: the name must be reproducible, because creating the
	// Tenant and recording the pointer to it are two writes. A requeue in
	// between has to land on the same object and adopt it, not mint a second
	// personal Tenant with its own workspace.
	//
	// The annotation is the ownership proof. status.firstAdmin cannot serve —
	// it is a separate write, so it is empty in exactly the crash window this
	// has to survive.
	if _, err := naming.Seeded(r.naming,
		naming.Request{Kind: naming.KindTenant, DisplayName: r.displayName(user), Seed: user.Name},
		func(name string) (bool, error) {
			candidate := &pmtenancyv1alpha1.Tenant{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name,
					Annotations: map[string]string{pmtenancyv1alpha1.AnnotationSeededFor: user.Name},
				},
				Spec: pmtenancyv1alpha1.TenantSpec{
					DisplayName:               r.displayName(user),
					Personal:                  true,
					ProjectCreation:           pmtenancyv1alpha1.CreationMembers,
					ProvidersMetadataCreation: pmtenancyv1alpha1.CreationMembers,
				},
			}

			err := cl.Create(ctx, candidate)
			if err == nil {
				*tenant = *candidate
				return true, nil
			}
			if !apierrors.IsAlreadyExists(err) {
				return false, fmt.Errorf("creating personal Tenant for user %q: %w", user.Name, err)
			}

			existing := &pmtenancyv1alpha1.Tenant{}
			if err := cl.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name}, existing); err != nil {
				return false, fmt.Errorf("inspecting Tenant %q while seeding user %q: %w", name, user.Name, err)
			}
			if existing.Annotations[pmtenancyv1alpha1.AnnotationSeededFor] != user.Name {
				// Somebody else's. Only reachable with a strategy whose name space is
				// small enough for two users to derive the same candidate; the next
				// attempt derives a different one.
				return false, nil
			}
			*tenant = *existing
			return true, nil
		},
	); err != nil {
		chain.MarkFalse(user, r.Name(), "Error", err.Error())
		return chain.StopAndRequeue, err
	}

	// Status is a subresource, so it must be written separately — set on the
	// struct passed to Create it is silently discarded, and the index step would
	// then skip this Tenant forever as "owned by nobody".
	if tenant.Status.FirstAdmin != user.Name {
		tenant.Status.FirstAdmin = user.Name
		if err := cl.Status().Update(ctx, tenant); err != nil {
			err = fmt.Errorf("recording the first admin of Tenant %q: %w", tenant.Name, err)
			chain.MarkFalse(user, r.Name(), "Error", err.Error())
			return chain.StopAndRequeue, err
		}
	}

	user.Status.DefaultTenant = tenant.Name
	chain.MarkTrue(user, r.Name())
	return chain.Continue, nil
}

func (r *seedTenant) displayName(user *pmtenancyv1alpha1.User) string {
	label := strings.TrimSpace(user.Spec.Name)
	if label == "" {
		label = strings.TrimSpace(user.Spec.Email)
	}
	if label == "" {
		label = user.Name
	}
	format := r.cfg.PersonalTenantDisplayNameFormat
	if !strings.Contains(format, "%s") {
		return label
	}
	return fmt.Sprintf(format, label)
}
