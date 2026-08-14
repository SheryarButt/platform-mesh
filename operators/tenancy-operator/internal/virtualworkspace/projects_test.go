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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/virtualworkspace"
	"go.platform-mesh.io/tenancy-operator/pkg/naming"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// fleet wires a directory holding the caller's index and the Tenants, plus
// one client per Tenant workspace where the Projects actually live.
type fleet struct {
	directory ctrlruntimeclient.Client
	clusters  map[string]ctrlruntimeclient.Client
}

func (f *fleet) clusterClient(id string) (ctrlruntimeclient.Client, error) {
	c, ok := f.clusters[id]
	if !ok {
		return nil, fmt.Errorf("cluster %s unreachable", id)
	}
	return c, nil
}

func newFleet(t *testing.T, directory []ctrlruntimeclient.Object, clusters map[string][]ctrlruntimeclient.Object) *fleet {
	t.Helper()
	f := &fleet{
		directory: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(directory...).Build(),
		clusters:  map[string]ctrlruntimeclient.Client{},
	}
	for id, objs := range clusters {
		f.clusters[id] = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	}
	return f
}

func (f *fleet) storage(t *testing.T, strategyName string) *virtualworkspace.ProjectStorage {
	t.Helper()
	s, err := naming.Get(strategyName)
	require.NoError(t, err)
	return virtualworkspace.NewProjectStorage(f.directory, f.clusterClient, testResolver(t), s)
}

func project(name string) *pmtenancyv1alpha1.Project {
	return &pmtenancyv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func tenantWithPolicy(name, creation string) *pmtenancyv1alpha1.Tenant {
	return &pmtenancyv1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       pmtenancyv1alpha1.TenantSpec{DisplayName: name, ProjectCreation: creation},
	}
}

// Who may create is the Tenant's own policy. `members` means
// member-and-above — NOT "anyone holding a Membership", which is what would let a
// read-only grant create Projects.
func TestProjectCreateEnforcesTheTenantPolicy(t *testing.T) {
	for name, tc := range map[string]struct {
		policy    string
		role      string
		wantAllow bool
	}{
		"members policy, admin":  {pmtenancyv1alpha1.CreationMembers, "admin", true},
		"members policy, member": {pmtenancyv1alpha1.CreationMembers, "member", true},
		"members policy, viewer": {pmtenancyv1alpha1.CreationMembers, "viewer", false},
		"admin policy, admin":    {pmtenancyv1alpha1.CreationAdmin, "admin", true},
		"admin policy, member":   {pmtenancyv1alpha1.CreationAdmin, "member", false},
		"admin policy, viewer":   {pmtenancyv1alpha1.CreationAdmin, "viewer", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFleet(t,
				[]ctrlruntimeclient.Object{
					tenantWithPolicy("tenant", tc.policy),
					index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{
						TenantUUID: "tenant", TenantClusterID: "c1", Role: tc.role,
					}),
				},
				map[string][]ctrlruntimeclient.Object{"c1": nil},
			)

			_, err := f.storage(t, naming.StrategyUUID).Create(
				authenticated(testIssuer, testSubject, testEmail),
				&pmtenancyv1alpha1.Project{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{pmtenancyv1alpha1.LabelTenant: "tenant"}},
					Spec:       pmtenancyv1alpha1.ProjectSpec{DisplayName: "Team"},
				},
				nil, &metav1.CreateOptions{})

			if tc.wantAllow {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsForbidden(err), "expected a 403, got %v", err)
		})
	}
}

// Listing is deliberately cross-tenant — "where can I work" is the question
// a client asks after login — and each Project is stamped with the tenant it came
// from, because a flat list with no owner cannot be grouped.
func TestProjectListSpansTenantsAndStampsTheOwner(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(wantName(t),
				pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "tenant-a", TenantClusterID: "c1", Role: "admin"},
				pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "tenant-b", TenantClusterID: "c2", Role: "admin"},
			),
		},
		map[string][]ctrlruntimeclient.Object{
			"c1": {project("a1")},
			"c2": {project("b1")},
		},
	)

	obj, err := f.storage(t, naming.StrategyUUID).List(authenticated(testIssuer, testSubject, testEmail), nil)
	require.NoError(t, err)

	list := obj.(*pmtenancyv1alpha1.ProjectList)
	require.Len(t, list.Items, 2)

	owners := map[string]string{}
	for _, a := range list.Items {
		owners[a.Name] = a.Labels[pmtenancyv1alpha1.LabelTenant]
	}
	assert.Equal(t, "tenant-a", owners["a1"])
	assert.Equal(t, "tenant-b", owners["b1"])
}

// One unreachable Tenant must not fail the whole listing — the others are
// still valid answers, and a client that cannot list anything after login has no
// way forward.
func TestProjectListToleratesAnUnreachableTenant(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(wantName(t),
				pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "up", TenantClusterID: "c1", Role: "admin"},
				pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "down", TenantClusterID: "missing", Role: "admin"},
			),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {project("a1")}},
	)

	obj, err := f.storage(t, naming.StrategyUUID).List(authenticated(testIssuer, testSubject, testEmail), nil)
	require.NoError(t, err)
	assert.Len(t, obj.(*pmtenancyv1alpha1.ProjectList).Items, 1)
}

// A Tenant whose workspace is not Ready has no cluster to list from. A
// wait, not a failure.
func TestProjectListSkipsATenantWithNoWorkspaceYet(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{TenantUUID: "pending", Role: "admin"}),
		},
		nil,
	)

	obj, err := f.storage(t, naming.StrategyUUID).List(authenticated(testIssuer, testSubject, testEmail), nil)
	require.NoError(t, err)
	assert.Empty(t, obj.(*pmtenancyv1alpha1.ProjectList).Items)
}

// A project-scope Membership grants exactly the Projects it names; the others in
// the same Tenant stay invisible.
func TestProjectListHidesAccountsTheCallerHasNoMembershipIn(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", ProjectUUID: "mine", Role: "member",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {project("mine"), project("theirs")}},
	)

	obj, err := f.storage(t, naming.StrategyUUID).List(authenticated(testIssuer, testSubject, testEmail), nil)
	require.NoError(t, err)

	list := obj.(*pmtenancyv1alpha1.ProjectList)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "mine", list.Items[0].Name)
}

// 404 rather than 403, for the same reason tenants does it.
func TestAccountGetOfAnInvisibleAccountIs404(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", ProjectUUID: "mine", Role: "member",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": {project("mine"), project("theirs")}},
	)

	_, err := f.storage(t, naming.StrategyUUID).Get(
		authenticated(testIssuer, testSubject, testEmail), "theirs", &metav1.GetOptions{})
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err))
}

// Projects are unique only within one Tenant, so two tenants may hold the same
// name — and the retry path still has to converge inside each one.
func TestProjectCreateResolvesNameCollisionsWithinATenant(t *testing.T) {
	f := newFleet(t,
		[]ctrlruntimeclient.Object{
			tenantWithPolicy("tenant", pmtenancyv1alpha1.CreationMembers),
			index(wantName(t), pmtenancyv1alpha1.MembershipIndexEntry{
				TenantUUID: "tenant", TenantClusterID: "c1", Role: "admin",
			}),
		},
		map[string][]ctrlruntimeclient.Object{"c1": nil},
	)

	s := f.storage(t, naming.StrategyWords)
	ctx := authenticated(testIssuer, testSubject, testEmail)

	seen := map[string]bool{}
	for range 100 {
		obj, err := s.Create(ctx, &pmtenancyv1alpha1.Project{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{pmtenancyv1alpha1.LabelTenant: "tenant"}},
			Spec:       pmtenancyv1alpha1.ProjectSpec{DisplayName: "Team"},
		}, nil, &metav1.CreateOptions{})
		require.NoError(t, err)

		name := obj.(*pmtenancyv1alpha1.Project).Name
		assert.False(t, seen[name], "Create returned %q twice", name)
		seen[name] = true
	}
}

func TestProjectStorageRejectsAnUnauthenticatedCaller(t *testing.T) {
	f := newFleet(t, nil, nil)

	_, err := f.storage(t, naming.StrategyUUID).List(context.Background(), nil)
	assert.Error(t, err)
}
