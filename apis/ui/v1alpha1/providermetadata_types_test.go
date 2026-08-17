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

package v1alpha1

import "testing"

func TestProviderMetadataDeepCopyPreservesDetailViewExtensions(t *testing.T) {
	original := &ProviderMetadata{
		Spec: ProviderMetadataSpec{
			DisplayName: "Example",
			DetailViewExtensions: []DetailViewExtension{
				{URL: "https://provider.example/details"},
				{URL: "https://provider.example/compatibility"},
			},
		},
	}

	copied := original.DeepCopy()
	copied.Spec.DetailViewExtensions[0].URL = "https://changed.example"

	if got, want := original.Spec.DetailViewExtensions[0].URL, "https://provider.example/details"; got != want {
		t.Fatalf("DeepCopy changed the original URL: got %q, want %q", got, want)
	}
}
