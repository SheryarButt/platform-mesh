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

package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"

	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	kcpapisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

// Scenario: the installer builds the API surface, split across four exports.
func TestScenarioInstallBuildsTheFourExports(t *testing.T) {
	layout := install(t)
	exports := clusterClient(layout.Exports)

	list := &kcpapisv1alpha2.APIExportList{}
	require.NoError(t, exports.List(context.Background(), list))

	// Keyed for EVERY export, including the ones with no resources — those are
	// half the point of the split, and a map built only from resource names drops
	// them silently.
	got := map[string][]string{}
	for i := range list.Items {
		e := &list.Items[i]
		got[e.Name] = []string{}
		for _, r := range e.Spec.Resources {
			got[e.Name] = append(got[e.Name], r.Name)
		}
	}

	require.Contains(t, got, "tenancy")
	require.Contains(t, got, "tenancy-platform")
	require.Contains(t, got, "tenancy-provisioner")
	require.Contains(t, got, "tenancy-access")

	// The audience split. `memberships` is servable in a Tenant; `users` and
	// `tenants` are not, and that is what stops a tenant workspace being a
	// directory of the platform's people.
	assert.ElementsMatch(t, []string{"memberships", "projects"}, got["tenancy"])

	// Both indices ride with the platform export. The VW resolves a caller by
	// reading BOTH — their own grants and the ones their groups carry — so an
	// export serving one without the other answers half of every request.
	assert.ElementsMatch(t,
		[]string{"users", "tenants", "usermembershipindices", "groupmembershipindices"},
		got["tenancy-platform"])

	// The capability split: neither carries a schema. They exist to name a
	// permission, so a reader of `kubectl get apibindings` can see which workspace
	// was granted the power to make workspaces and which to reach inside one.
	assert.Empty(t, got["tenancy-provisioner"])
	assert.Empty(t, got["tenancy-access"])
}

// Scenario: the directory really serves the group index, not merely declares it.
func TestScenarioGroupIndexIsServableInTheDirectory(t *testing.T) {
	layout := install(t)
	directory := clusterClient(layout.Directory)

	gmi := &pmtenancyv1alpha1.GroupMembershipIndex{}
	gmi.Name = "0000000000000000000000000000000000000000000000000000000000000000"
	gmi.Spec.Group = "acme-engineering"
	gmi.Spec.Entries = []pmtenancyv1alpha1.MembershipIndexEntry{{
		TenantUUID: "t1", TenantClusterID: "c1", Role: pmtenancyv1alpha1.MembershipRoleMember,
	}}

	// The installer binds tenancy-platform here, but becoming servable is
	// asynchronous — a bound APIBinding and a resource in discovery are two
	// different moments.
	servable(t, layout.Directory, gmi)

	got := &pmtenancyv1alpha1.GroupMembershipIndex{}
	require.NoError(t, directory.Get(context.Background(),
		ctrlruntimeclient.ObjectKey{Name: gmi.Name}, got))
	assert.Equal(t, "acme-engineering", got.Spec.Group)
}

// Scenario: the server enforces the subject XOR, through the CRD's own CEL rule.
func TestScenarioMembershipSubjectIsValidatedByTheServer(t *testing.T) {
	layout := install(t)

	// A stand-in for a Tenant workspace.
	path := workspace(t, "tenant-like")
	bindExport(t, path, layout.Exports, "tenancy")
	tenantWS := clusterClient(path)

	t.Run("a group subject is accepted", func(t *testing.T) {
		m := &pmtenancyv1alpha1.Membership{}
		m.Name = "group-grant"
		m.Spec = pmtenancyv1alpha1.MembershipSpec{
			Group: "acme-engineering",
			Scope: pmtenancyv1alpha1.MembershipScopeTenant,
			Role:  pmtenancyv1alpha1.MembershipRoleMember,
		}
		servable(t, path, m)

		got := &pmtenancyv1alpha1.Membership{}
		require.NoError(t, tenantWS.Get(context.Background(),
			ctrlruntimeclient.ObjectKey{Name: "group-grant"}, got))
		assert.Equal(t, "acme-engineering", got.Spec.Group)
		assert.Empty(t, got.Spec.User, "spec.user has to be optional now, or no group grant can be stored")
	})

	// Both halves of the XOR. A CEL rule that does not compile is an error kcp
	// reports when the schema is applied; one that compiles but never matches is a
	// validation that silently does nothing. Neither is reachable from a unit test.
	t.Run("two subjects are refused", func(t *testing.T) {
		m := &pmtenancyv1alpha1.Membership{}
		m.Name = "both-subjects"
		m.Spec = pmtenancyv1alpha1.MembershipSpec{
			User:  "0000",
			Group: "acme-engineering",
			Scope: pmtenancyv1alpha1.MembershipScopeTenant,
			Role:  pmtenancyv1alpha1.MembershipRoleMember,
		}
		err := tenantWS.Create(context.Background(), m)
		require.Error(t, err, "the CRD's XOR rule must reject two subjects")
		assert.Contains(t, err.Error(), "exactly one of spec.user or spec.group")
	})

	t.Run("no subject is refused", func(t *testing.T) {
		m := &pmtenancyv1alpha1.Membership{}
		m.Name = "no-subject"
		m.Spec = pmtenancyv1alpha1.MembershipSpec{
			Scope: pmtenancyv1alpha1.MembershipScopeTenant,
			Role:  pmtenancyv1alpha1.MembershipRoleMember,
		}
		err := tenantWS.Create(context.Background(), m)
		require.Error(t, err, "a Membership with no subject grants nobody anything")
		assert.Contains(t, err.Error(), "exactly one of spec.user or spec.group")
	})
}

// Scenario: the installer converges twice, as every pod start and upgrade does.
func TestScenarioInstallIsIdempotent(t *testing.T) {
	layout := install(t)

	before := &kcpapisv1alpha2.APIExportList{}
	require.NoError(t, clusterClient(layout.Exports).List(context.Background(), before))

	// The same layout again, which is what a restart does.
	reinstall(t, layout)

	after := &kcpapisv1alpha2.APIExportList{}
	require.NoError(t, clusterClient(layout.Exports).List(context.Background(), after))
	assert.Len(t, after.Items, len(before.Items), "a second install must not add exports")
}
