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

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPendingWorkspaces(t *testing.T) {
	tests := map[string]struct {
		root, path   string
		wantParent   string
		wantSegments []string
	}{
		// The case this exists for. A module installs at root:modules:<name> with
		// a credential scoped to that subtree; walking up to root:modules is a
		// write it is denied, and kcp reports the denial as a discovery failure.
		"module subtree stops at the install root": {
			root:         "root:modules:tenancy-operator",
			path:         "root:modules:tenancy-operator:system:controllers",
			wantParent:   "root:modules:tenancy-operator",
			wantSegments: []string{"system", "controllers"},
		},
		"install root itself is never created": {
			root:       "root:modules:tenancy-operator",
			path:       "root:modules:tenancy-operator",
			wantParent: "root:modules:tenancy-operator",
		},
		"one level below the root": {
			root:         "root:modules:tenancy-operator",
			path:         "root:modules:tenancy-operator:tenants",
			wantParent:   "root:modules:tenancy-operator",
			wantSegments: []string{"tenants"},
		},
		"rooted at kcp's own root": {
			root:         "root",
			path:         "root:system:controllers",
			wantParent:   "root",
			wantSegments: []string{"system", "controllers"},
		},
		// --paths-exports and friends are free-form, so a path can legitimately
		// land outside the install root. Nothing else will create it, so the full
		// walk stays the behaviour there.
		"path outside the root walks from the top": {
			root:         "root:modules:tenancy-operator",
			path:         "root:elsewhere:controllers",
			wantParent:   "root",
			wantSegments: []string{"elsewhere", "controllers"},
		},
		// A root that is a string prefix of the path but not an ancestor of it:
		// root:mod is not the parent of root:modules, and treating it as one would
		// try to create a workspace named "ules".
		"prefix that is not an ancestor": {
			root:         "root:mod",
			path:         "root:modules:x",
			wantParent:   "root",
			wantSegments: []string{"modules", "x"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			parent, segments := pendingWorkspaces(tc.root, tc.path)
			assert.Equal(t, tc.wantParent, parent)
			assert.Equal(t, tc.wantSegments, segments)
		})
	}
}

// Two installs reach ensureWorkspaces and they need opposite walks, so the
// starting point is a decision preflight makes rather than a property of the
// layout. Getting it wrong is not a subtle failure in either direction:
//
//   - assume the root exists when it does not, and an install rooted at
//     root:tenancy dies probing a workspace it was supposed to create;
//   - assume it does not when it does, and a module install is denied in the
//     parent it was never given rights to.
func TestPendingWorkspacesBothWalks(t *testing.T) {
	// Pre-provisioned: the deployer made root:modules:tenancy-operator and the
	// credential is scoped to it. Everything above is off limits.
	t.Run("existing root is the starting point", func(t *testing.T) {
		parent, segments := pendingWorkspaces(
			"root:modules:tenancy-operator",
			"root:modules:tenancy-operator:system:controllers")
		assert.Equal(t, "root:modules:tenancy-operator", parent)
		assert.Equal(t, []string{"system", "controllers"}, segments)
	})

	// Self-made: nobody creates root:tenancy, so preflight reports the nearest
	// reachable ancestor — kcp's own root — and the install root is among the
	// workspaces built. This is the shape the chart install needs.
	t.Run("absent root is built from its nearest reachable ancestor", func(t *testing.T) {
		parent, segments := pendingWorkspaces("root", "root:tenancy:system:controllers")
		assert.Equal(t, "root", parent)
		assert.Equal(t, []string{"tenancy", "system", "controllers"}, segments,
			"the install root itself must be among the workspaces created")
	})

	// The ancestor need not be the top. An install at root:acme:platform whose
	// root:acme already exists starts THERE — walking from kcp's root instead
	// would write into a workspace this credential may have no rights in, which
	// is the failure the module case showed: kcp reports it as a discovery miss.
	t.Run("an intermediate ancestor is respected", func(t *testing.T) {
		parent, segments := pendingWorkspaces("root:acme", "root:acme:platform:system")
		assert.Equal(t, "root:acme", parent)
		assert.Equal(t, []string{"platform", "system"}, segments)
	})
}
