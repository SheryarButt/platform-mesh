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

package subroutine

import (
	"sort"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

func objectSchema(props map[string]apiextensionsv1.JSONSchemaProps) apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{Type: "object", Properties: props}
}

func TestCollectLeafFieldPathsNested(t *testing.T) {
	root := objectSchema(map[string]apiextensionsv1.JSONSchemaProps{
		"metadata": objectSchema(map[string]apiextensionsv1.JSONSchemaProps{
			"name": {Type: "string"},
		}),
		"spec": objectSchema(map[string]apiextensionsv1.JSONSchemaProps{
			"description": {Type: "string"},
			"count":       {Type: "integer"},
		}),
		"status": objectSchema(map[string]apiextensionsv1.JSONSchemaProps{
			// array node: emitted as a single leaf, NOT descended into
			"conditions": {
				Type: "array",
				Items: &apiextensionsv1.JSONSchemaPropsOrArray{
					Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"type": {Type: "string"},
						},
					},
				},
			},
			// map-typed node (additionalProperties, no Properties): emitted as a leaf
			"labels": {
				Type:                 "object",
				AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{Allows: true},
			},
		}),
	})

	got := collectLeafFieldPaths(&root)
	sort.Strings(got)

	want := []string{
		"metadata.name",
		"spec.count",
		"spec.description",
		"status.conditions",
		"status.labels",
	}
	if len(got) != len(want) {
		t.Fatalf("collectLeafFieldPaths() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("collectLeafFieldPaths()[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestCollectLeafFieldPathsDepthGuard(t *testing.T) {
	// Build a chain deeper than maxSchemaWalkDepth; the walk must terminate and emit the
	// node reached at the depth limit as a leaf rather than recursing forever.
	leaf := apiextensionsv1.JSONSchemaProps{Type: "string"}
	node := objectSchema(map[string]apiextensionsv1.JSONSchemaProps{"end": leaf})
	for range maxSchemaWalkDepth + 5 {
		node = objectSchema(map[string]apiextensionsv1.JSONSchemaProps{"child": node})
	}

	got := collectLeafFieldPaths(&node)
	if len(got) != 1 {
		t.Fatalf("collectLeafFieldPaths() returned %d paths, want 1: %v", len(got), got)
	}
}

func newFilter(excluded, semantic, exact []string) searchConfigFilter {
	return searchConfigFilter{
		excluded: sets.New[string](excluded...),
		semantic: sets.New[string](semantic...),
		exact:    sets.New[string](exact...),
	}
}

func TestSortIntoAnyOfClassification(t *testing.T) {
	f := newFilter(
		[]string{"metadata.name"},    // excluded
		[]string{"spec.description"}, // semantic
		[]string{"status.phase"},     // exact -> filterable
	)

	defaults := sets.New[string]()
	semantics := sets.New[string]()
	exacts := sets.New[string]()

	for _, p := range []string{"metadata.name", "spec.description", "status.phase", "spec.count"} {
		f.sortIntoAnyOf(p, defaults, semantics, exacts)
	}

	if defaults.Has("metadata.name") {
		t.Fatal("excluded field metadata.name should not appear in any set")
	}
	if semantics.Has("metadata.name") {
		t.Fatal("excluded field metadata.name leaked into semantics")
	}
	if !semantics.Has("spec.description") {
		t.Fatalf("spec.description should be semantic, semantics=%v", semantics)
	}
	if !exacts.Has("status.phase") {
		t.Fatalf("status.phase should be exact/filterable, exacts=%v", exacts)
	}
	if !defaults.Has("spec.count") {
		t.Fatalf("spec.count should be default, defaults=%v", defaults)
	}
}

// TestSortIntoAnyOfArgOrderRegression guards the previously-swapped semantic/exact arguments:
// a semantic field must land in the semantic set (not exact) and vice versa.
func TestSortIntoAnyOfArgOrderRegression(t *testing.T) {
	f := newFilter(nil, []string{"a"}, []string{"b"})

	defaults := sets.New[string]()
	semantics := sets.New[string]()
	exacts := sets.New[string]()

	f.sortIntoAnyOf("a", defaults, semantics, exacts)
	f.sortIntoAnyOf("b", defaults, semantics, exacts)

	if !semantics.Has("a") {
		t.Fatalf("semantic field a not in semantics: %v", semantics)
	}
	if exacts.Has("a") {
		t.Fatal("semantic field a wrongly landed in exacts")
	}
	if !exacts.Has("b") {
		t.Fatalf("exact field b not in exacts: %v", exacts)
	}
	if semantics.Has("b") {
		t.Fatal("exact field b wrongly landed in semantics")
	}
}
