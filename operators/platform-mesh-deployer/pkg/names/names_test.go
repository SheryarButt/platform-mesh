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

package names_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.platform-mesh.io/platform-mesh-deployer/pkg/names"
)

// Limits kcp-operator's derived names are subject to.
const (
	// annotationNameMax is the limit on a label or annotation name part.
	// kcp-operator stamps "operator.kcp.io/cert-<certificate>-revision" onto
	// the Compiled* CRs, which is what binds most admin CR names.
	annotationNameMax = 63
	// commonNameMax is the RFC 5280 ub-common-name upper bound cert-manager enforces.
	commonNameMax = 64
	// dnsLabelMax is the DNS-1035 label limit on Service names.
	dnsLabelMax = 63
)

type derivation struct {
	format string
	limit  int
}

// revisionAnnotation is the annotation key kcp-operator derives from a
// certificate name; see internal/controller/{rootshard,shard,frontproxy}/controller.go.
func revisionAnnotation(certificate string) derivation {
	return derivation{"cert-" + certificate + "-revision", annotationNameMax}
}

// TestBudgets pins every Max* to the tightest name kcp-operator derives from
// it. The derivations are duplicated from kcp-operator's internal/resources,
// which is not importable; this test is what keeps the copies honest.
func TestBudgets(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind    string
		budget  int
		derived []derivation
	}{
		{
			kind:   "RootShard",
			budget: names.MaxRootShard,
			derived: []derivation{
				revisionAnnotation("%s-external-logical-cluster-admin"),
				revisionAnnotation("%s-logical-cluster-admin"),
				revisionAnnotation("%s-virtual-workspaces"),
				revisionAnnotation("%s-service-account"),
				{"%s-service-account", commonNameMax},
				{"%s-kcp", dnsLabelMax},
				{"%s-proxy", dnsLabelMax},
			},
		},
		{
			kind:   "Shard",
			budget: names.MaxShard,
			derived: []derivation{
				revisionAnnotation("%s-external-logical-cluster-admin"),
				revisionAnnotation("%s-logical-cluster-admin"),
				{"external-logical-cluster-admin-shard-%s", commonNameMax},
				{"%s-shard-kcp", dnsLabelMax},
			},
		},
		{
			kind:   "FrontProxy",
			budget: names.MaxFrontProxy,
			derived: []derivation{
				// A front proxy certificate is named after the root shard too,
				// so the budget only holds beside a root shard at its own.
				revisionAnnotation(strings.Repeat("x", names.MaxRootShard) + "-%s-requestheader"),
				revisionAnnotation(strings.Repeat("x", names.MaxRootShard) + "-%s-kubeconfig"),
				{"%s-front-proxy", dnsLabelMax},
			},
		},
		{
			kind:   "CacheServer",
			budget: names.MaxCacheServer,
			derived: []derivation{
				revisionAnnotation("%s-client-certificate"),
				revisionAnnotation("%s-server"),
				{"%s-ca", commonNameMax},
				{"%s-cache-server", dnsLabelMax},
			},
		},
		{
			kind:   "VirtualWorkspace",
			budget: names.MaxVirtualWorkspace,
			derived: []derivation{
				revisionAnnotation("%s-client"),
				revisionAnnotation("%s-server"),
				{"%s-virtual-workspace", commonNameMax},
				{"%s-virtual-workspace", dnsLabelMax},
			},
		},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			at := strings.Repeat("x", tc.budget)
			for _, d := range tc.derived {
				assert.LessOrEqualf(t, len(strings.Replace(d.format, "%s", at, 1)), d.limit,
					"%s budget %d exceeds the %d byte limit on %q", tc.kind, tc.budget, d.limit, d.format)
			}
			// The budget must be the tightest one, not merely a safe one.
			over := strings.Repeat("x", tc.budget+1)
			var tightest bool
			for _, d := range tc.derived {
				if len(strings.Replace(d.format, "%s", over, 1)) > d.limit {
					tightest = true
				}
			}
			assert.Truef(t, tightest, "%s budget %d is lower than necessary", tc.kind, tc.budget)
		})
	}
}

// TestRootShardFrontProxyBudget covers the one derivation naming two admin CRs
// at once, which no per-kind budget can express.
func TestRootShardFrontProxyBudget(t *testing.T) {
	t.Parallel()

	// resources.go GetFrontProxyCertificateName: "<rootShard>-<frontProxy>-<certKind>",
	// longest front proxy certKind being "requestheader".
	key := func(rootShard, frontProxy int) string {
		return "cert-" + strings.Repeat("x", rootShard) + "-" + strings.Repeat("x", frontProxy) + "-requestheader-revision"
	}
	at := names.MaxRootShardFrontProxy
	assert.LessOrEqual(t, len(key(at/2, at-at/2)), annotationNameMax)
	assert.Greater(t, len(key(at/2, at+1-at/2)), annotationNameMax,
		"MaxRootShardFrontProxy is lower than necessary")

	assert.LessOrEqual(t, names.MaxRootShard+names.MaxFrontProxy, names.MaxRootShardFrontProxy,
		"a root shard and a front proxy at their budgets must fit the shared one")
}

func TestScoped(t *testing.T) {
	t.Parallel()

	t.Run("keeps a readable stub", func(t *testing.T) {
		t.Parallel()
		got := names.Shard("customer-a", "default", "172-18-0-2")
		assert.True(t, strings.HasPrefix(got, "cust"), got)
		assert.Contains(t, got, "def")
	})

	t.Run("bounded for every budget", func(t *testing.T) {
		t.Parallel()
		long := strings.Repeat("long", 40)
		for name, budget := range map[string]int{
			"RootShard":        names.MaxRootShard,
			"Shard":            names.MaxShard,
			"FrontProxy":       names.MaxFrontProxy,
			"CacheServer":      names.MaxCacheServer,
			"VirtualWorkspace": names.MaxVirtualWorkspace,
		} {
			for _, in := range [][3]string{
				{long, long, long},
				{"a", "b", "c"},
				{"", "", ""},
				{"customer-a", "default", "172-18-0-2"},
				{"a-", "-b", "c"},
			} {
				got := names.Scoped(budget, in[0], in[1], in[2])
				assert.LessOrEqualf(t, len(got), budget, "%s: %v", name, in)
				assert.Regexpf(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, got, "%s: %v", name, in)
			}
		}
	})

	t.Run("cluster id length does not consume budget", func(t *testing.T) {
		t.Parallel()
		short := names.Shard("customer-a", "default", "east")
		long := names.Shard("customer-a", "default", strings.Repeat("cluster", 20))
		assert.Equal(t, len(short), len(long))
		assert.NotEqual(t, short, long)
	})

	t.Run("distinct inputs stay distinct", func(t *testing.T) {
		t.Parallel()
		seen := map[string]string{}
		for _, in := range [][3]string{
			{"customer-a", "default", "east"},
			{"customer-a", "default", "west"},
			{"customer-b", "default", "east"},
			{"customer-a", "other", "east"},
			// Identical once truncated, differing only in the hashed identity.
			{"customer-aaaaaaaaaa", "default", "east"},
			{"customer-abbbbbbbbb", "default", "east"},
		} {
			got := names.Shard(in[0], in[1], in[2])
			prev, dup := seen[got]
			require.Falsef(t, dup, "%v and %s both produced %q", in, prev, got)
			seen[got] = strings.Join(in[:], "/")
		}
	})

	t.Run("is deterministic", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t,
			names.Shard("customer-a", "default", "east"),
			names.Shard("customer-a", "default", "east"))
	})

	t.Run("a root shard and front proxy pair fits the shared budget", func(t *testing.T) {
		t.Parallel()
		long := strings.Repeat("long", 40)
		rs := names.RootShard(long, long, long)
		fp := names.FrontProxy(long, long, long)
		assert.LessOrEqual(t, len(rs)+len(fp), names.MaxRootShardFrontProxy)
	})
}
