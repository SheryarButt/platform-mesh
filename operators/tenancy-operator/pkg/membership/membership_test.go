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

package membership_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/pkg/membership"
)

// Stable, not unique: a reconciler creates the Membership and then records a
// pointer to it, and a retry in between must land on the SAME object. Duplicate
// Memberships mean duplicate role bindings, and revoking one leaves the other
// live.
func TestNameIsDeterministic(t *testing.T) {
	first := membership.Name("user", "tenant", "")
	for range 10 {
		assert.Equal(t, first, membership.Name("user", "tenant", ""))
	}
}

// The key is exactly what makes two Memberships the same grant. Any component
// changing must change the name, or two different grants collide on one object.
func TestNameVariesWithEveryComponent(t *testing.T) {
	base := membership.Name("user", "project", "proj-1")

	assert.NotEqual(t, base, membership.Name("other", "project", "proj-1"))
	assert.NotEqual(t, base, membership.Name("user", "tenant", "proj-1"))
	assert.NotEqual(t, base, membership.Name("user", "project", "proj-2"))
}

// The components are joined with a separator that cannot appear inside any of
// them. With a naive join, ("a", "b", "c") and ("a", "bc", "") — or ("ab", "c")
// — would hash to one name and silently merge two grants.
func TestNameCannotBeConfusedAcrossComponentBoundaries(t *testing.T) {
	assert.NotEqual(t, membership.Name("a", "b", "c"), membership.Name("ab", "", "c"))
	assert.NotEqual(t, membership.Name("a", "b", "c"), membership.Name("a", "bc", ""))
	assert.NotEqual(t, membership.Name("a:b", "c", ""), membership.Name("a", "b:c", ""))
}

// It becomes an object name, so it has to be one.
func TestNameIsAUsableObjectName(t *testing.T) {
	name := membership.Name("a-very-long-user-digest-"+string(make([]byte, 64)), "project", "proj-1")
	assert.NotEmpty(t, name)
	assert.LessOrEqual(t, len(name), 253)
	assert.NotContains(t, name, "\n")
}

// The formula that names user grants is FROZEN. These are the names every
// Membership in every existing installation already has, and the object's name is
// how the reconciler finds it again — so a change here does not rename anything,
// it orphans the old object and has the reconciler create a second one granting
// the same access.
func TestUserNamesAreFrozen(t *testing.T) {
	// Computed by the formula as it shipped: uuidv5(ns, "u\nscope\nproject").
	assert.Equal(t,
		"a8726d9a-277c-5e99-b8d0-cd7a8d198bb5",
		membership.Name("f00d", "tenant", ""))
	assert.Equal(t,
		"b32bae79-0cb9-5c1f-bc91-7c4a831fac7b",
		membership.Name("f00d", "project", "proj-1"))
}

// A group grant and a user grant must never land on the same name: one object
// cannot be two grants, and the reconciler would bind whichever spec it read.
func TestGroupNamesCannotCollideWithUserNames(t *testing.T) {
	// The adversarial case: a group named exactly like the three components of a
	// user key, which is what a naive prefix scheme would fold together.
	assert.NotEqual(t,
		membership.Name("admins", "tenant", ""),
		membership.NameForGroup("admins", "tenant", ""))

	// And the one that would break a "prepend a literal" scheme: a user whose name
	// IS the marker. Users are always digests so this cannot occur, but the key
	// shape must not be the only thing standing between the two spaces.
	assert.NotEqual(t,
		membership.Name("group", "tenant", ""),
		membership.NameForGroup("", "tenant", ""))
}

func TestNameForDispatchesOnKind(t *testing.T) {
	assert.Equal(t,
		membership.NameForGroup("acme", "tenant", ""),
		membership.NameFor(pmtenancyv1alpha1.SubjectKindGroup, "acme", "tenant", ""))
	assert.Equal(t,
		membership.Name("acme", "tenant", ""),
		membership.NameFor(pmtenancyv1alpha1.SubjectKindUser, "acme", "tenant", ""))
}
