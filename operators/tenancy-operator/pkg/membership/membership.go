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

// Package membership derives the names of Membership objects.
//
// Deliberately not pkg/naming, which does the opposite job. There a name must be
// UNIQUE, so a taken one is a collision to retry past with a different candidate.
// Here it must be STABLE, so a taken one means the grant already exists.
package membership

import (
	"strings"

	"github.com/google/uuid"
)

// nameNamespace seeds the derived names below. Arbitrary but fixed — changing it
// makes every existing Membership unreachable by name, so a reconciler would
// create a second one granting the same access.
var nameNamespace = uuid.MustParse("2b8f4c17-9d3e-4a62-b0f5-7c1e8a94d206")

// Name derives a Membership's name from what it grants.
//
// Deterministic: a reconciler that creates the object and then records a pointer
// cannot do both atomically, so a retry in between must land on the SAME object.
// Duplicate Memberships mean duplicate role bindings, and revoking one would leave
// the other live.
//
// The key is (user, scope, project) — what makes two Memberships the same grant.
// project is empty for scope=tenant.
func Name(user, scope, project string) string {
	key := strings.Join([]string{user, scope, project}, "\n")
	return uuid.NewSHA1(nameNamespace, []byte(key)).String()
}
