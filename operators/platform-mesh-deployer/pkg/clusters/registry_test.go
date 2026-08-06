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

package clusters

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

func TestClustersForFiltersByComponentAndPlatformMesh(t *testing.T) {
	r := NewRegistry()
	ctx := t.Context()

	names := []multicluster.ClusterName{
		"rootshard#customer-a--east",
		"frontproxy#customer-a--east",
		"shards-default#customer-a--west",
		"rootshard#customer-b--east",
		"rootshard#customer-a-b--east", // different pm whose name contains '-'
	}
	for _, name := range names {
		require.NoError(t, r.Engage(ctx, name, nil))
	}

	tests := []struct {
		platformMesh string
		component    string
		wantIDs      []string
	}{
		{"customer-a", "rootshard", []string{"east"}},
		{"customer-a", "frontproxy", []string{"east"}},
		{"customer-a", "shards-default", []string{"west"}},
		{"customer-b", "rootshard", []string{"east"}},
		{"customer-a-b", "rootshard", []string{"east"}},
		{"customer-a", "cacheserver", nil},
	}
	for _, tt := range tests {
		got := r.ClustersFor(tt.platformMesh, tt.component)
		var ids []string
		for _, c := range got {
			ids = append(ids, c.ClusterID)
		}
		assert.Equalf(t, tt.wantIDs, ids, "ClustersFor(%q, %q)", tt.platformMesh, tt.component)
	}
}

func TestEngageDisengageEmitEvents(t *testing.T) {
	r := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = r.Start(ctx) }()

	require.NoError(t, r.Engage(ctx, "rootshard#pm--a", nil))
	assert.Equal(t, "pm", assertEvent(t, r))

	if _, ok := r.Get("rootshard#pm--a"); !ok {
		t.Fatal("cluster not found after engage")
	}

	clusterCtx, disengage := context.WithCancel(ctx)
	require.NoError(t, r.Engage(clusterCtx, "frontproxy#pm--a", nil))
	assert.Equal(t, "pm", assertEvent(t, r))

	disengage()
	assert.Equal(t, "pm", assertEvent(t, r))

	require.Eventually(t, func() bool {
		_, ok := r.Get("frontproxy#pm--a")
		return !ok
	}, time.Second, 10*time.Millisecond, "cluster still present after disengage")
}

func assertEvent(t *testing.T, r *Registry) string {
	t.Helper()
	select {
	case evt := <-r.Events():
		return evt.Object.GetName()
	case <-time.After(time.Second):
		t.Fatal("expected an event")
		return ""
	}
}

func TestAllClustersForDedupsByClusterID(t *testing.T) {
	r := NewRegistry()
	ctx := t.Context()

	names := []multicluster.ClusterName{
		"rootshard#customer-a--east",
		"frontproxy#customer-a--east", // same cluster, second role
		"shards-default#customer-a--west",
		"rootshard#customer-b--other",
		"rootshard#customer-a-b--east", // different pm whose name contains '-'
	}
	for _, name := range names {
		require.NoError(t, r.Engage(ctx, name, nil))
	}

	all := r.AllClustersFor("customer-a")
	ids := make([]string, 0, len(all))
	for _, c := range all {
		ids = append(ids, c.ClusterID)
	}
	assert.Equal(t, []string{"east", "west"}, ids)

	assert.Empty(t, r.AllClustersFor("unknown"))
}

func TestShardGroups(t *testing.T) {
	r := NewRegistry()
	ctx := t.Context()

	names := []multicluster.ClusterName{
		"shards-default#customer-a--s1",
		"shards-default#customer-a--s2",
		"shards-eu#customer-a--s3",
		"rootshard#customer-a--east",
		"shards-default#customer-b--s4",
	}
	for _, name := range names {
		require.NoError(t, r.Engage(ctx, name, nil))
	}

	assert.Equal(t, []string{"default", "eu"}, r.ShardGroups("customer-a"))
	assert.Equal(t, []string{"default"}, r.ShardGroups("customer-b"))
	assert.Empty(t, r.ShardGroups("unknown"))
}
