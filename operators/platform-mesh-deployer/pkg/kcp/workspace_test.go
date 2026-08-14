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

package kcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

func workspace(name string, phase kcpcorev1alpha1.LogicalClusterPhaseType) *kcptenancyv1alpha1.Workspace {
	return &kcptenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     kcptenancyv1alpha1.WorkspaceStatus{Phase: phase},
	}
}

// A freshly created workspace is not usable yet, so the caller has to come back.
func TestEnsureWorkspaceCreatesAndWaits(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()

	err := EnsureWorkspace(t.Context(), cl, "modules")
	require.ErrorIs(t, err, ErrWorkspacePending)

	ws := &kcptenancyv1alpha1.Workspace{}
	require.NoError(t, cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Name: "modules"}, ws))
	assert.Equal(t, kcptenancyv1alpha1.WorkspaceTypeName(WorkspaceType), ws.Spec.Type.Name)
}

func TestEnsureWorkspaceWaitsWhileNotReady(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(workspace("modules", kcpcorev1alpha1.LogicalClusterPhaseInitializing)).Build()

	require.ErrorIs(t, EnsureWorkspace(t.Context(), cl, "modules"), ErrWorkspacePending)
}

func TestEnsureWorkspaceReady(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(workspace("modules", kcpcorev1alpha1.LogicalClusterPhaseReady)).Build()

	require.NoError(t, EnsureWorkspace(t.Context(), cl, "modules"))
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		path       string
		parent     string
		name       string
		splittable bool
	}{
		{path: "root:modules", parent: "root", name: "modules", splittable: true},
		{path: "root:modules:acme", parent: "root:modules", name: "acme", splittable: true},
		{path: "root", splittable: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			parent, name, ok := splitPath(tt.path)
			assert.Equal(t, tt.splittable, ok)
			if tt.splittable {
				assert.Equal(t, tt.parent, parent)
				assert.Equal(t, tt.name, name)
			}
		})
	}
}

// Deleting is asynchronous, so the first pass issues the delete and reports
// pending; once the object is gone the caller is done.
func TestDeleteWorkspace(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(workspace("acme", kcpcorev1alpha1.LogicalClusterPhaseReady)).Build()

	err := DeleteWorkspace(t.Context(), cl, "acme")
	require.ErrorIs(t, err, ErrWorkspacePending)

	require.NoError(t, DeleteWorkspace(t.Context(), cl, "acme"))
}

// A path without a parent cannot be deleted; root is not ours to remove.
func TestDeletePathRejectsRoot(t *testing.T) {
	s := testScheme(t)
	access := New(fake.NewClientBuilder().WithScheme(s).Build(), nil, s, nil)
	require.ErrorContains(t, access.DeletePath(t.Context(), nil, "root"), "no parent")
}
