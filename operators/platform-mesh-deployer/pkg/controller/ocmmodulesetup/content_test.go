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

package ocmmodulesetup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestDecode_skipsEmptyDocuments(t *testing.T) {
	t.Parallel()
	objs, err := decode([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: b\n"))
	require.NoError(t, err)
	require.Len(t, objs, 2)
	assert.Equal(t, "ConfigMap", objs[0].GetKind())
	assert.Equal(t, "Secret", objs[1].GetKind())
}

func TestDecode_rejectsMalformedYAML(t *testing.T) {
	t.Parallel()
	_, err := decode([]byte("\tnot: [valid"))
	require.Error(t, err)
}

func TestApplyContent_isANoOpWithoutContent(t *testing.T) {
	t.Parallel()
	r := newReconciler(t, moduleSetup(), platformMesh(true), Options{
		ResolveModule: func(context.Context, *pmdeployv1alpha1.OCMModule, *pmdeployv1alpha1.OCMRepository) (*ocmmodule.Resolved, error) {
			t.Fatal("a workspace without content must not resolve the component version")
			return nil, nil
		},
	})

	require.NoError(t, r.applyContent(t.Context(), nil, pmdeployv1alpha1.OCMModuleSetupWorkspace{Path: "root:modules:acme"}))
}

func TestApplyContent_appliesEveryDocumentOfEveryResource(t *testing.T) {
	t.Parallel()
	var applied []string
	r := newReconciler(t, moduleSetup(), platformMesh(true), Options{
		GetModule: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModule, error) {
			return &pmdeployv1alpha1.OCMModule{}, nil
		},
		ResolveModule: func(context.Context, *pmdeployv1alpha1.OCMModule, *pmdeployv1alpha1.OCMRepository) (*ocmmodule.Resolved, error) {
			return &ocmmodule.Resolved{}, nil
		},
		DownloadResource: func(_ context.Context, _ *ocmmodule.Resolved, name string) ([]byte, error) {
			return []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: " + name + "\n"), nil
		},
		ApplyObject: func(_ context.Context, _ ctrlruntimeclient.Client, obj *unstructured.Unstructured) error {
			applied = append(applied, obj.GetKind()+"/"+obj.GetName())
			return nil
		},
	})

	ws := pmdeployv1alpha1.OCMModuleSetupWorkspace{
		Path:    "root:modules:acme",
		Content: []pmdeployv1alpha1.ResourceRef{{Name: "a"}, {Name: "b"}},
	}
	require.NoError(t, r.applyContent(t.Context(), nil, ws))

	assert.Equal(t, []string{"ConfigMap/a", "Secret/a", "ConfigMap/b", "Secret/b"}, applied)
}

func TestApplyContent_namesTheWorkspaceWhenAnApplyFails(t *testing.T) {
	t.Parallel()
	r := newReconciler(t, moduleSetup(), platformMesh(true), Options{
		GetModule: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModule, error) {
			return &pmdeployv1alpha1.OCMModule{}, nil
		},
		ResolveModule: func(context.Context, *pmdeployv1alpha1.OCMModule, *pmdeployv1alpha1.OCMRepository) (*ocmmodule.Resolved, error) {
			return &ocmmodule.Resolved{}, nil
		},
		DownloadResource: func(context.Context, *ocmmodule.Resolved, string) ([]byte, error) {
			return []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n"), nil
		},
		ApplyObject: func(context.Context, ctrlruntimeclient.Client, *unstructured.Unstructured) error {
			return assert.AnError
		},
	})

	ws := pmdeployv1alpha1.OCMModuleSetupWorkspace{
		Path:    "root:modules:acme",
		Content: []pmdeployv1alpha1.ResourceRef{{Name: "manifests"}},
	}
	err := r.applyContent(t.Context(), nil, ws)
	require.ErrorIs(t, err, assert.AnError)
	assert.ErrorContains(t, err, "root:modules:acme")
}

func TestResolveModule_wrapsAMissingModule(t *testing.T) {
	t.Parallel()
	r := newReconciler(t, moduleSetup(), platformMesh(true), Options{
		GetModule: func(context.Context, ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModule, error) {
			return nil, assert.AnError
		},
	})

	_, err := r.resolveModule(t.Context())
	require.ErrorIs(t, err, assert.AnError)
	assert.ErrorContains(t, err, `getting OCMModule "acme"`)
}
