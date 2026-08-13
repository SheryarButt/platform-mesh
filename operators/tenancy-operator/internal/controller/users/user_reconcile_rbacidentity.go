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

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"

	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// rbacIdentity keeps spec.rbacIdentity equal to what kcp will actually see.
//
// The field mirrors kcp's username convention, and a mirror written once is not a
// mirror. When --oidc-username-prefix changes, every existing User names a subject
// that no longer authenticates and every binding built from it is inert — a 403 in
// a workspace the user owns, with a Membership that reads as correct.
//
// Recomputing here turns that permanent breakage into a convergence: the mirror is
// corrected on restart and the Membership reconciler rewrites the bindings.
type rbacIdentity struct {
	resolver *identity.Resolver
}

func (r *rbacIdentity) Name() string { return pmtenancyv1alpha1.UserConditionRBACIdentityCurrent }

func (r *rbacIdentity) Reconcile(_ context.Context, _ ctrlruntimeclient.Client, user *pmtenancyv1alpha1.User) (chain.Status, error) {
	want, err := r.resolver.RBACIdentity(identity.Claims{
		Issuer:  user.Spec.Issuer,
		Subject: user.Spec.Subject,
		Email:   user.Spec.Email,
	})
	if err != nil {
		// The recorded claims carry nothing the configured convention can use.
		// Terminal: no retry invents the missing claim, and overwriting a working
		// identity with an empty string would revoke access rather than fix it.
		chain.MarkFalse(user, r.Name(), "Unresolvable", err.Error())
		return chain.Stop, nil
	}

	if user.Spec.RBACIdentity != want {
		// A spec write from a controller, which is unusual — but this whole spec is
		// server-owned: the client sends an empty object and the platform fills every
		// field. Nobody's intent is being overwritten.
		user.Spec.RBACIdentity = want
		chain.MarkTrue(user, r.Name())
		// Stop: downstream steps read this User, and letting them run against the
		// value we just replaced would have them act on the stale one for one pass.
		return chain.Stop, nil
	}

	chain.MarkTrue(user, r.Name())
	return chain.Continue, nil
}
