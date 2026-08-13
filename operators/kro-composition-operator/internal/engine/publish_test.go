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

	krov1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCompositeExportName(t *testing.T) {
	t.Parallel()
	require.Equal(t, "kro-webpage", compositeExportName("webpage"))
}

func TestSchemaHash(t *testing.T) {
	t.Parallel()
	a := &apiextensionsv1.CustomResourceDefinition{Spec: apiextensionsv1.CustomResourceDefinitionSpec{Group: "a.example.com"}}
	b := &apiextensionsv1.CustomResourceDefinition{Spec: apiextensionsv1.CustomResourceDefinitionSpec{Group: "b.example.com"}}

	ha := schemaHash(a)
	require.Len(t, ha, 12)
	require.Equal(t, ha, schemaHash(a), "hash must be stable for the same spec")
	require.NotEqual(t, ha, schemaHash(b), "different specs must hash differently")
}

func TestOwnedByRGD(t *testing.T) {
	t.Parallel()
	rgd := &krov1alpha1.ResourceGraphDefinition{ObjectMeta: metav1.ObjectMeta{Name: "rgd", UID: "rgd-uid"}}
	owned := &metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{UID: "rgd-uid"}}}
	other := &metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{UID: "someone-else"}}}
	none := &metav1.ObjectMeta{}

	require.True(t, ownedByRGD(owned, rgd), "owner ref UID matches the RGD")
	require.False(t, ownedByRGD(other, rgd), "non-matching owner ref")
	require.False(t, ownedByRGD(none, rgd), "no owner refs")
}
