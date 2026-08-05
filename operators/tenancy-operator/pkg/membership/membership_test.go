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

	"go.platform-mesh.io/tenancy-operator/pkg/membership"
)

// Stable, not unique: a reconciler creates the Membership and then records a
// pointer to it, and a retry in between must land on the SAME object. Duplicate
// Memberships mean duplicate role bindings, and revoking one leaves the other
// live.
func TestNameIsDeterministic(t *testing.T) {
	first := membership.Name("user", "tenant", "")
	for i := 0; i < 10; i++ {
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
