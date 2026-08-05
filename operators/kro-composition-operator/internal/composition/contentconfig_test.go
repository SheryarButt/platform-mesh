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

package composition

import (
	"encoding/json"
	"testing"

	"github.com/gobuffalo/flect"
	"github.com/stretchr/testify/require"
)

// The portal queries the generated type as a GraphQL field the gateway names
// flect.Pluralize(Kind) — NOT the lowercase resource plural. This regressed once
// (Webpages vs WebPages), so pin it.
func TestBuildContentConfig_EntityCollectionMatchesFlect(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ kind, plural string }{
		{"WebPage", "webpages"},
		{"Policy", "policies"},
		{"Bundle", "bundles"},
	} {
		cc := BuildContentConfig("apps.example.com", "v1alpha1", tc.kind, tc.plural, true, nil)
		require.Contains(t, cc, `"entityCollection":"`+flect.Pluralize(tc.kind)+`"`, "kind %q", tc.kind)
		if flect.Pluralize(tc.kind) != tc.plural {
			require.NotContains(t, cc, `"entityCollection":"`+tc.plural+`"`, "kind %q must not use the resource plural", tc.kind)
		}
	}
}

func TestBuildContentConfig_ScopeAndFields(t *testing.T) {
	t.Parallel()
	// Namespaced type with a spec field: valid JSON, Namespaced scope, spec field present.
	cc := BuildContentConfig("apps.example.com", "v1alpha1", "WebPage", "webpages", true, []string{"url"})
	require.True(t, json.Valid([]byte(cc)), "content config must be valid JSON")
	for _, want := range []string{`"scope":"Namespaced"`, `"spec.url"`, "WebPage (kro)", "metadata.namespace"} {
		require.Contains(t, cc, want)
	}

	// Cluster-scoped type: Cluster scope and no namespace anywhere.
	ccCluster := BuildContentConfig("apps.example.com", "v1alpha1", "WebPage", "webpages", false, nil)
	require.Contains(t, ccCluster, `"scope":"Cluster"`)
	require.NotContains(t, ccCluster, "metadata.namespace", "cluster-scoped type must not reference a namespace")
}

func TestTitle(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"": "", "url": "Url", "Already": "Already"} {
		require.Equal(t, want, title(in), "title(%q)", in)
	}
}
