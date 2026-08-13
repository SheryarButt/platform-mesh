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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/controller/chain"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func resolverFor(t *testing.T, prefix string) *identity.Resolver {
	t.Helper()
	r, err := identity.NewResolver(identity.Config{UsernameClaim: identity.ClaimEmail, UsernamePrefix: prefix})
	require.NoError(t, err)
	return r
}

// The field mirrors kcp's username convention, and a mirror written once is not a
// mirror. When the platform's prefix moves, every existing User names a subject
// that no longer authenticates — this step is what converges them.
func TestRBACIdentityIsRecomputedWhenTheConventionMoves(t *testing.T) {
	user := &pmtenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "u"},
		Spec: pmtenancyv1alpha1.UserSpec{
			Email:        "alice@acme.example",
			RBACIdentity: "old:alice@acme.example",
		},
	}

	status, err := (&rbacIdentity{resolver: resolverFor(t, "pm:")}).Reconcile(context.Background(), nil, user)
	require.NoError(t, err)
	assert.Equal(t, "pm:alice@acme.example", user.Spec.RBACIdentity)

	// Stop, not Continue: everything downstream reads this User, and letting the
	// rest of the chain run against the value that was just replaced would have
	// them act on the stale one for a whole pass.
	assert.Equal(t, chain.Stop, status)
}

// Once it is current the chain carries on — the stop above is about the write,
// not about the step.
func TestRBACIdentityContinuesWhenAlreadyCurrent(t *testing.T) {
	user := &pmtenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "u"},
		Spec: pmtenancyv1alpha1.UserSpec{
			Email:        "alice@acme.example",
			RBACIdentity: "pm:alice@acme.example",
		},
	}

	status, err := (&rbacIdentity{resolver: resolverFor(t, "pm:")}).Reconcile(context.Background(), nil, user)
	require.NoError(t, err)
	assert.Equal(t, chain.Continue, status)
}

// A steady state must not rewrite the object, or the operator feeds its own watch.
func TestRBACIdentityLeavesACurrentValueAlone(t *testing.T) {
	user := &pmtenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "u"},
		Spec: pmtenancyv1alpha1.UserSpec{
			Email:        "alice@acme.example",
			RBACIdentity: "pm:alice@acme.example",
		},
	}
	before := user.DeepCopy()

	_, err := (&rbacIdentity{resolver: resolverFor(t, "pm:")}).Reconcile(context.Background(), nil, user)
	require.NoError(t, err)
	assert.Equal(t, before.Spec, user.Spec)
}

// An underivable identity must STOP rather than write an empty string: an empty
// rbacIdentity would revoke every binding built from it instead of fixing
// anything.
func TestRBACIdentityStopsWhenItCannotBeDerived(t *testing.T) {
	user := &pmtenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "u"},
		Spec:       pmtenancyv1alpha1.UserSpec{RBACIdentity: "pm:alice@acme.example"},
	}

	status, err := (&rbacIdentity{resolver: resolverFor(t, "pm:")}).Reconcile(context.Background(), nil, user)
	require.NoError(t, err)
	assert.Equal(t, chain.Stop, status)
	assert.Equal(t, "pm:alice@acme.example", user.Spec.RBACIdentity, "the existing identity must not be cleared")

	c := meta.FindStatusCondition(user.Status.Conditions, pmtenancyv1alpha1.UserConditionRBACIdentityCurrent)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, "Unresolvable", c.Reason)
}
