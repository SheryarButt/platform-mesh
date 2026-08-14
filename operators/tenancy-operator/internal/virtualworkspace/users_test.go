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

package virtualworkspace_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/virtualworkspace"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testIssuer  = "https://auth.example.com"
	testSubject = "9c4b8e1f-1111-2222-3333-444455556666"
	testEmail   = "alice@acme.example"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(pmtenancyv1alpha1.AddToScheme(s))
	return s
}

func testStorage(t *testing.T, objs ...ctrlruntimeclient.Object) *virtualworkspace.UserStorage {
	t.Helper()
	resolver, err := identity.NewResolver(identity.Config{
		UsernameClaim:  identity.ClaimEmail,
		UsernamePrefix: "pm:",
	})
	require.NoError(t, err)

	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&pmtenancyv1alpha1.User{}).
		Build()
	return virtualworkspace.NewUserStorage(c, resolver)
}

// authenticated builds the request context the authenticator would produce.
func authenticated(issuer, subject, email string) context.Context {
	return authenticatedInGroups(issuer, subject, email)
}

// authenticatedInGroups is the same, carrying IdP groups on top of the synthetic
// one every authenticated caller has.
func authenticatedInGroups(issuer, subject, email string, groups ...string) context.Context {
	return request.WithUser(context.Background(), &user.DefaultInfo{
		Name:   "pm:" + email,
		Groups: append([]string{"system:authenticated"}, groups...),
		Extra: map[string][]string{
			virtualworkspace.ExtraIssuer:  {issuer},
			virtualworkspace.ExtraSubject: {subject},
			virtualworkspace.ExtraEmail:   {email},
			virtualworkspace.ExtraName:    {"Alice Doe"},
		},
	})
}

func wantName(t *testing.T) string {
	t.Helper()
	n, err := identity.UserName(testIssuer, testSubject)
	require.NoError(t, err)
	return n
}

// The one call a new identity makes. Every field comes from the token, and the
// name is the identity digest — not anything the client sent.
func TestCreateSelfProvisionsFromTheToken(t *testing.T) {
	s := testStorage(t)
	ctx := authenticated(testIssuer, testSubject, testEmail)

	// A hostile body: a name that is not the caller's, and a spec claiming
	// somebody else's identity and a raised quota.
	submitted := &pmtenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "somebody-else"},
		Spec: pmtenancyv1alpha1.UserSpec{
			Email:        "attacker@evil.example",
			RBACIdentity: "pm:root",
			TenantQuota:  9999,
		},
	}

	obj, err := s.Create(ctx, submitted, nil, &metav1.CreateOptions{})
	require.NoError(t, err)

	created, ok := obj.(*pmtenancyv1alpha1.User)
	require.True(t, ok)

	assert.Equal(t, wantName(t), created.Name, "the name is the identity digest, never the request body")
	assert.Equal(t, testEmail, created.Spec.Email)
	assert.Equal(t, "pm:"+testEmail, created.Spec.RBACIdentity, "rbacIdentity mirrors kcp's username convention")
	assert.Equal(t, testIssuer, created.Spec.Issuer)
	assert.Equal(t, testSubject, created.Spec.Subject)
	assert.Zero(t, created.Spec.TenantQuota, "a client cannot raise its own quota")
}

// Two tabs, two calls, one project. This is why the name is the digest rather
// than a generated value with the hash in a label.
func TestCreateIsIdempotent(t *testing.T) {
	s := testStorage(t)
	ctx := authenticated(testIssuer, testSubject, testEmail)

	first, err := s.Create(ctx, &pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.NoError(t, err)
	second, err := s.Create(ctx, &pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.NoError(t, err, "a repeated self-provision must not error")

	assert.Equal(t,
		first.(*pmtenancyv1alpha1.User).Name,
		second.(*pmtenancyv1alpha1.User).Name)
}

// An unprovisioned caller is the normal first-run state, and the answer is 404 —
// the client's cue to call create.
func TestGetIs404UntilProvisioned(t *testing.T) {
	s := testStorage(t)
	ctx := authenticated(testIssuer, testSubject, testEmail)

	_, err := s.Get(ctx, virtualworkspace.SelfAlias, &metav1.GetOptions{})
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err))

	_, err = s.Create(ctx, &pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.NoError(t, err)

	obj, err := s.Get(ctx, virtualworkspace.SelfAlias, &metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, wantName(t), obj.(*pmtenancyv1alpha1.User).Name)
}

// The VW is never a directory. Reading somebody else's User returns 404, not 403
// — a 403 would confirm the object exists and make this an oracle for who else is
// on the platform.
func TestGetOfAnotherUserIs404NotForbidden(t *testing.T) {
	otherName, err := identity.UserName(testIssuer, "someone-else")
	require.NoError(t, err)

	s := testStorage(t, &pmtenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: otherName}})
	ctx := authenticated(testIssuer, testSubject, testEmail)

	_, err = s.Get(ctx, otherName, &metav1.GetOptions{})
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err), "existence must not leak as a 403")
}

// Failure closed: no verified identity, no provisioning.
func TestUnauthenticatedIsRejected(t *testing.T) {
	s := testStorage(t)

	_, err := s.Create(context.Background(), &pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	assert.Error(t, err)

	// Authenticated, but the token carried no issuer/subject — which means the VW
	// is misconfigured against a different issuer than kcp.
	ctx := request.WithUser(context.Background(), &user.DefaultInfo{Name: "pm:alice", Groups: []string{"system:authenticated"}})
	_, err = s.Create(ctx, &pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	assert.Error(t, err)
}

// kubectl asks for a Table even on a plain `get users ~`. The apiserver only
// REQUIRES a TableConvertor on listers, so a missing one is not caught at
// registration — it surfaces as a nil-interface dereference inside the GET
// handler, i.e. a 500 on the one call a new identity makes first.
func TestUserStorageRendersATable(t *testing.T) {
	s := testStorage(t)

	user := &pmtenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "abc", CreationTimestamp: metav1.Now()},
		Spec:       pmtenancyv1alpha1.UserSpec{Email: "dex@pm.localhost", Name: "dex"},
		Status: pmtenancyv1alpha1.UserStatus{
			Conditions: []metav1.Condition{{Type: pmtenancyv1alpha1.UserConditionReady, Status: metav1.ConditionTrue}},
		},
	}

	table, err := s.ConvertToTable(context.Background(), user, nil)
	require.NoError(t, err)
	require.Len(t, table.Rows, 1)

	assert.Equal(t, "abc", table.Rows[0].Cells[0])
	assert.Equal(t, "dex@pm.localhost", table.Rows[0].Cells[1])
	assert.Equal(t, "dex", table.Rows[0].Cells[2])
	assert.Equal(t, "True", table.Rows[0].Cells[3], "the Ready condition drives the Ready column")
}

// A token with no `email` claim, under --oidc-username-claim=email, has no
// derivable RBAC identity. It must be refused.
//
// There used to be a fallback that filled the email from the authenticated
// username, and because that username already carries the prefix it produced
// `pm:pm:alice@acme.example`. Nothing rejected it: the User was created, its
// bindings named a subject kcp never presents, and the caller was silently 403'd
// in workspaces they were a member of. The regression is invisible unless
// something asserts on the failure, hence this test.
func TestCreateRefusesATokenWithNoUsernameClaim(t *testing.T) {
	s := testStorage(t)

	ctx := request.WithUser(context.Background(), &user.DefaultInfo{
		Name:   "pm:" + testEmail,
		Groups: []string{"system:authenticated"},
		Extra: map[string][]string{
			virtualworkspace.ExtraIssuer:  {testIssuer},
			virtualworkspace.ExtraSubject: {testSubject},
			// No email: the IdP omitted the claim.
		},
	})

	_, err := s.Create(ctx, &pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.Error(t, err)
	assert.True(t, apierrors.IsBadRequest(err), "expected a BadRequest, got %v", err)
	assert.Contains(t, err.Error(), "cannot derive an RBAC identity")
}

// The display name is the token's `name` claim and nothing else: an absent claim
// leaves it empty so the seed reconciler can fall back to unprefixed values.
//
// This pins the storage half only. The authenticator used to default the same
// field to the authenticated username — which carries the RBAC prefix, so every
// UI string read "pm:alice@…'s personal" — and that half is not reachable from
// here, because the authenticator is unexported and this test injects the request
// context it would have produced.
func TestCreateDoesNotUseTheUsernameAsADisplayName(t *testing.T) {
	s := testStorage(t)

	ctx := request.WithUser(context.Background(), &user.DefaultInfo{
		Name:   "pm:" + testEmail,
		Groups: []string{"system:authenticated"},
		Extra: map[string][]string{
			virtualworkspace.ExtraIssuer:  {testIssuer},
			virtualworkspace.ExtraSubject: {testSubject},
			virtualworkspace.ExtraEmail:   {testEmail},
			// No name claim.
		},
	})

	obj, err := s.Create(ctx, &pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.NoError(t, err)

	created, ok := obj.(*pmtenancyv1alpha1.User)
	require.True(t, ok)
	assert.Empty(t, created.Spec.Name, "an absent name claim must not fall back to the prefixed username")
	assert.Equal(t, "pm:"+testEmail, created.Spec.RBACIdentity, "the identity must be prefixed exactly once")
}

// The group sample is bounded, and the bound is the point: a federated identity
// can arrive carrying thousands of groups, and this field exists to help someone
// debug — not to mirror a directory into an object the platform stores and lists.
func TestCreateStoresABoundedGroupSample(t *testing.T) {
	s := testStorage(t)

	many := make([]string, 0, 2000)
	for i := range 2000 {
		many = append(many, fmt.Sprintf("group-%04d", i))
	}

	obj, err := s.Create(authenticatedInGroups(testIssuer, testSubject, testEmail, many...),
		&pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.NoError(t, err)
	created := obj.(*pmtenancyv1alpha1.User)

	assert.Len(t, created.Status.Groups, pmtenancyv1alpha1.MaxObservedGroups)
	assert.Equal(t, int32(2000), created.Status.GroupCount)
	assert.Equal(t, "group-0000", created.Status.Groups[0])
	assert.Equal(t, "group-0031", created.Status.Groups[pmtenancyv1alpha1.MaxObservedGroups-1])
}

// `system:authenticated` names every caller there is. It is added by this server
// rather than by the issuer, and a field that listed it would be inviting whoever
// reads it next to treat "in a group" as meaning nothing.
func TestCreateDropsSyntheticAndOversizedGroups(t *testing.T) {
	s := testStorage(t)

	huge := strings.Repeat("x", pmtenancyv1alpha1.MaxObservedGroupLength+1)
	ctx := authenticatedInGroups(testIssuer, testSubject, testEmail,
		"acme-engineering", "system:masters", huge)

	obj, err := s.Create(ctx, &pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.NoError(t, err)
	created := obj.(*pmtenancyv1alpha1.User)

	assert.Equal(t, []string{"acme-engineering"}, created.Status.Groups)
	assert.Equal(t, int32(1), created.Status.GroupCount)
}

// Self-provision is the call a client makes on every login, so it is what stamps
// the login time — on the first create and on every repeat of it, which are two
// different code paths.
func TestCreateStampsLastLogin(t *testing.T) {
	s := testStorage(t)
	ctx := authenticatedInGroups(testIssuer, testSubject, testEmail, "acme-engineering")

	first, err := s.Create(ctx, &pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.NoError(t, err)
	require.NotNil(t, first.(*pmtenancyv1alpha1.User).Status.LastLogin,
		"the first provision is a login too")

	second, err := s.Create(ctx, &pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.NoError(t, err, "a repeated self-provision must not error")

	was := first.(*pmtenancyv1alpha1.User).Status.LastLogin
	now := second.(*pmtenancyv1alpha1.User).Status.LastLogin
	require.NotNil(t, now)
	assert.False(t, now.Before(was), "lastLogin must not move backwards")
}

// A read is not a login. This is the same rule that makes provisioning an
// explicit call: if GET moved the timestamp, a monitoring probe polling `users ~`
// would report a fleet of identities that never stopped signing in.
func TestGetDoesNotStampLastLogin(t *testing.T) {
	s := testStorage(t)
	ctx := authenticated(testIssuer, testSubject, testEmail)

	created, err := s.Create(ctx, &pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.NoError(t, err)
	stamped := created.(*pmtenancyv1alpha1.User).Status.LastLogin
	require.NotNil(t, stamped)

	got, err := s.Get(ctx, virtualworkspace.SelfAlias, &metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, stamped, got.(*pmtenancyv1alpha1.User).Status.LastLogin)
}

// A group change IS observed, and on the arm that matters: the first create runs
// once, so anything that only wrote there would record the sample at first login
// and let it rot through every login after.
func TestCreateRefreshesAChangedGroupSample(t *testing.T) {
	s := testStorage(t)

	_, err := s.Create(authenticatedInGroups(testIssuer, testSubject, testEmail, "acme-engineering"),
		&pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.NoError(t, err)

	obj, err := s.Create(authenticatedInGroups(testIssuer, testSubject, testEmail, "platform-admins"),
		&pmtenancyv1alpha1.User{}, nil, &metav1.CreateOptions{})
	require.NoError(t, err)

	updated := obj.(*pmtenancyv1alpha1.User)
	assert.Equal(t, []string{"platform-admins"}, updated.Status.Groups)
}
