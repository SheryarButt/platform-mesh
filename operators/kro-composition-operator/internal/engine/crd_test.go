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

package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestStorageVersion(t *testing.T) {
	t.Parallel()
	crd := &apiextensionsv1.CustomResourceDefinition{Spec: apiextensionsv1.CustomResourceDefinitionSpec{
		Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
			{Name: "v1alpha1", Storage: false},
			{Name: "v1", Storage: true},
		},
	}}
	require.Equal(t, "v1", storageVersion(crd))

	crd.Spec.Versions[1].Storage = false // no version flagged storage → first
	require.Equal(t, "v1alpha1", storageVersion(crd))

	require.Empty(t, storageVersion(&apiextensionsv1.CustomResourceDefinition{}), "no versions → empty")
}

func TestSpecFieldsFromCRD(t *testing.T) {
	t.Parallel()
	crd := &apiextensionsv1.CustomResourceDefinition{Spec: apiextensionsv1.CustomResourceDefinitionSpec{
		Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
			Name: "v1alpha1",
			Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"spec": {Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"zebra": {Type: "string"},
						"apple": {Type: "string"},
					}},
				},
			}},
		}},
	}}
	require.Equal(t, []string{"apple", "zebra"}, specFieldsFromCRD(crd, "v1alpha1"), "sorted spec keys")
	require.Nil(t, specFieldsFromCRD(crd, "nonexistent"), "unknown version → nil")
}

func TestContentConfigName(t *testing.T) {
	t.Parallel()
	require.Equal(t, "kro-bundle", contentConfigName("bundle"))
}

func TestInstanceGVR(t *testing.T) {
	t.Parallel()
	crd := &apiextensionsv1.CustomResourceDefinition{Spec: apiextensionsv1.CustomResourceDefinitionSpec{
		Group: "apps.example.com",
		Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "widgets"},
		Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
			{Name: "v1alpha1", Storage: false},
			{Name: "v1", Storage: true},
		},
	}}
	want := schema.GroupVersionResource{Group: "apps.example.com", Version: "v1", Resource: "widgets"}
	require.Equal(t, want, instanceGVR(crd), "uses the storage version")
}

func TestGraphKey(t *testing.T) {
	t.Parallel()
	gvr := schema.GroupVersionResource{Group: "apps.example.com", Version: "v1", Resource: "widgets"}
	require.Equal(t, "cluster-1|"+gvr.String(), graphKey("cluster-1", gvr))
}
