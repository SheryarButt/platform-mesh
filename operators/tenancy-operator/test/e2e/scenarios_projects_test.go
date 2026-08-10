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
	"testing"

	"github.com/stretchr/testify/assert"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
)

// Scenario: access to ONE project rather than to a whole tenant.
func TestScenarioProjectScopedGrantReachesOneProject(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())

	admin := f.createUser(t, "owner@acme.example")
	tenant := f.provisionTenant(t, "scoped", "Scoped", admin.Name)
	prod := f.provisionProject(t, tenant, "scoped-prod", "production")
	staging := f.provisionProject(t, tenant, "scoped-staging", "staging")

	t.Log("granting a group access to staging only")
	f.grantGroup(t, tenant, "acme-contractors", pmtenancyv1alpha1.MembershipScopeProject, staging.Name,
		pmtenancyv1alpha1.MembershipRoleViewer)

	groups := []string{"pm:acme-contractors"}
	f.awaitAdmitted(t, staging.Status.ClusterID, "pm:contractor@example.com", groups,
		"a project-scope grant must reach the project it names")

	assert.False(t,
		allowed(t, prod.Status.ClusterID, "pm:contractor@example.com", groups, "get", "", "configmaps"),
		"a project-scope grant must reach NOTHING else, or scope means nothing")

	// A viewer, so `create` is refused: on some resources create is a write in
	// disguise, which is why viewer is not simply "every verb but delete".
	assert.False(t,
		allowed(t, staging.Status.ClusterID, "pm:contractor@example.com", groups, "create", "", "configmaps"),
		"a viewer must not create")
}

// Scenario: a project created AFTER the grant is still reachable.
func TestScenarioTenantScopeReachesProjectsCreatedLater(t *testing.T) {
	f := newFleet(t, withoutPersonalTenants())

	admin := f.createUser(t, "owner@later.example")
	tenant := f.provisionTenant(t, "later", "Later", admin.Name)
	first := f.provisionProject(t, tenant, "later-first", "first")

	t.Log("granting a group before the second project exists")
	f.grantGroup(t, tenant, "later-engineering", pmtenancyv1alpha1.MembershipScopeTenant, "",
		pmtenancyv1alpha1.MembershipRoleMember)

	groups := []string{"pm:later-engineering"}
	f.awaitAdmitted(t, first.Status.ClusterID, "pm:someone@later.example", groups,
		"the existing project is reachable")

	// The project nobody had when the grant was written.
	t.Log("creating a second project after the grant")
	second := f.provisionProject(t, tenant, "later-second", "second")

	f.awaitAdmitted(t, second.Status.ClusterID, "pm:someone@later.example", groups,
		"a tenant-scope grant must reach a project created after it")
}
