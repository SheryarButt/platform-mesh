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

// Naming scenarios.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/pkg/naming"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Scenario: every registered naming strategy produces a workspace that works.
func TestScenarioEveryNamingStrategyProducesAUsableWorkspace(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())
	admin := f.createUser(t, "owner@naming.example")

	for _, strategyName := range naming.Registered() {
		t.Run(strategyName, func(t *testing.T) {
			strategy, err := naming.Get(strategyName)
			require.NoError(t, err)

			// Minted the same way the virtual workspace mints it: through
			// naming.Apply, which retries on collision and gives up when the
			// strategy runs out of candidates.
			displayName := "Naming " + strategyName
			var tenantName string
			tenantName, err = naming.Apply(strategy,
				naming.Request{Kind: naming.KindTenant, DisplayName: displayName},
				func(name string) error {
					tenantName = name
					return nil
				},
				func(error) bool { return false },
			)
			require.NoError(t, err, "the strategy must be able to mint a name at all")
			require.NoError(t, naming.Validate(tenantName))

			t.Logf("%s minted %q", strategyName, tenantName)

			// The part no unit test reaches: kcp has to accept it as a workspace.
			tenant := f.provisionTenant(t, tenantName, displayName, admin.Name)
			project := f.provisionProject(t, tenant, tenantName+"-p", "production")

			// And the rest of the chain has to keep working with it — a name that is
			// merely storable but not addressable would still break every grant.
			f.grantGroup(t, tenant, "naming-team", pmtenancyv1alpha1.MembershipScopeTenant, "",
				pmtenancyv1alpha1.MembershipRoleMember)
			f.awaitAdmitted(t, project.Status.ClusterID, "pm:someone@naming.example",
				[]string{"pm:naming-team"},
				"a tenant named by %s must still be somewhere access can be granted", strategyName)
		})
	}
}

// Scenario: two tenants ask for the same display name.
func TestScenarioDisplayNameCollisionsGetDistinctWorkspaces(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())
	admin := f.createUser(t, "owner@collide.example")

	strategy, err := naming.Get(naming.StrategyDisplayName)
	require.NoError(t, err)

	// The same display name, twice, with the second told the first is taken —
	// which is exactly what naming.Apply does on AlreadyExists.
	const shared = "Shared Name"
	taken := map[string]bool{}
	mint := func() string {
		name, err := naming.Apply(strategy,
			naming.Request{Kind: naming.KindTenant, DisplayName: shared},
			func(candidate string) error {
				if taken[candidate] {
					return apierrorsAlreadyExists(candidate)
				}
				taken[candidate] = true
				return nil
			},
			apierrorsIsAlreadyExists,
		)
		require.NoError(t, err)
		return name
	}

	first, second := mint(), mint()
	require.NotEqual(t, first, second,
		"a taken display name must yield another candidate, not the same path twice")
	t.Logf("%q became %q and %q", shared, first, second)

	// Both are real, separate tenants, and neither can see the other. The failure
	// this rules out is the second create silently resolving onto the first's
	// workspace, which would hand one tenant's admin the other tenant's data.
	tenantA := f.provisionTenant(t, first, shared, admin.Name)
	projectA := f.provisionProject(t, tenantA, first+"-p", "production")
	tenantB := f.provisionTenant(t, second, shared, admin.Name)
	projectB := f.provisionProject(t, tenantB, second+"-p", "production")

	require.NotEqual(t, projectA.Status.ClusterID, projectB.Status.ClusterID,
		"two tenants sharing a display name must not share a workspace")

	f.grantGroup(t, tenantA, "collide-a", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleAdmin)
	f.awaitAdmitted(t, projectA.Status.ClusterID, "pm:a@collide.example", []string{"pm:collide-a"},
		"the first tenant is reachable")
	assert.False(t,
		allowed(t, projectB.Status.ClusterID, "pm:a@collide.example", []string{"pm:collide-a"}, "get", "", "configmaps"),
		"a grant in one must not reach the other, however alike their display names")
}

// Scenario: a display name that is not a name.
func TestScenarioHostileDisplayNamesAreSlugifiedOrRefused(t *testing.T) {
	strategy, err := naming.Get(naming.StrategyDisplayName)
	require.NoError(t, err)

	for _, displayName := range []string{
		"ACME Corp",                 // spaces and capitals
		"../../etc/passwd",          // path traversal
		"a:b:c",                     // the workspace path separator
		"Ünïcødé Ltd",               // non-ASCII
		"---",                       // punctuation only
		"very-long-" + longRun(200), // longer than a name may be
	} {
		t.Run(displayName, func(t *testing.T) {
			name, err := naming.Apply(strategy,
				naming.Request{Kind: naming.KindTenant, DisplayName: displayName},
				func(string) error { return nil },
				func(error) bool { return false },
			)
			if err != nil {
				// Refusing is a correct answer: there is no obligation to find a
				// name in every string, only never to produce a bad one.
				t.Logf("refused, which is fine: %v", err)
				return
			}

			// If it produced something, it has to BE a name — this is the assertion
			// that stops a traversal or a path separator reaching a workspace path.
			require.NoError(t, naming.Validate(name),
				"a minted name must be valid, or it becomes an unusable workspace path")
			assert.NotContains(t, name, ":", "the workspace path separator must never appear in a name")
			assert.NotContains(t, name, "/", "a name must not be able to traverse")
			assert.LessOrEqual(t, len(name), 63, "a workspace name is a DNS label")
			t.Logf("%q became %q", displayName, name)
		})
	}
}

// Scenario: names outlive a switch to another strategy.
func TestScenarioExistingNamesSurviveAStrategySwitch(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())
	admin := f.createUser(t, "owner@switch.example")

	// A tenant named the way `words` would name it.
	words, err := naming.Get(naming.StrategyWords)
	require.NoError(t, err)
	wordsName, err := words.Generate(naming.Request{Kind: naming.KindTenant, DisplayName: "Old"})
	require.NoError(t, err)

	tenant := f.provisionTenant(t, wordsName, "Old", admin.Name)
	project := f.provisionProject(t, tenant, wordsName+"-p", "production")

	f.grantGroup(t, tenant, "switch-team", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleMember)
	f.awaitAdmitted(t, project.Status.ClusterID, "pm:someone@switch.example", []string{"pm:switch-team"},
		"the tenant works under the strategy it was named by")

	// A second tenant named the way `uuid` would, alongside it. Nothing in the
	// model reads a name to decide anything, which is what makes the mix safe.
	uuid, err := naming.Get(naming.StrategyUUID)
	require.NoError(t, err)
	uuidName, err := uuid.Generate(naming.Request{Kind: naming.KindTenant, DisplayName: "New"})
	require.NoError(t, err)

	newTenant := f.provisionTenant(t, uuidName, "New", admin.Name)
	newProject := f.provisionProject(t, newTenant, uuidName[:8]+"-p", "production")

	f.grantGroup(t, newTenant, "switch-team", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleMember)
	f.awaitAdmitted(t, newProject.Status.ClusterID, "pm:someone@switch.example", []string{"pm:switch-team"},
		"a tenant named by a different strategy works the same way")

	// And the original is untouched by the arrival of the new one.
	assert.True(t,
		allowed(t, project.Status.ClusterID, "pm:someone@switch.example", []string{"pm:switch-team"}, "get", "", "configmaps"),
		"the older naming scheme keeps working: a switch is not a migration")

	// Two tenants, one group, two separate grants — the group reaches both because
	// it was granted in both, not because a group is global.
	assert.NotEqual(t, tenant.Status.ClusterID, newTenant.Status.ClusterID)
}

// longRun builds a string of n 'a's, for the length checks above.
func longRun(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

// The two helpers below let the collision test drive naming.Apply's retry loop
// without a live API server: Apply only needs "did this create fail because the
// name was taken", and an API error is the shape it expects.
func apierrorsAlreadyExists(name string) error {
	return apierrors.NewAlreadyExists(pmtenancyv1alpha1.Resource("tenants"), name)
}

func apierrorsIsAlreadyExists(err error) bool { return apierrors.IsAlreadyExists(err) }
