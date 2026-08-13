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

package workspace

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClusterHost(t *testing.T) {
	t.Parallel()
	const cluster = "abcd1234"
	cases := []struct {
		name, baseHost, want string
	}{
		{
			name:     "plain shard base",
			baseHost: "https://shard.example.com:6443",
			want:     "https://shard.example.com:6443/clusters/" + cluster,
		},
		{
			name:     "trailing slash trimmed",
			baseHost: "https://shard.example.com:6443/",
			want:     "https://shard.example.com:6443/clusters/" + cluster,
		},
		{
			name:     "existing /clusters/<path> suffix is replaced",
			baseHost: "https://shard.example.com:6443/clusters/root:providers:kro-provider",
			want:     "https://shard.example.com:6443/clusters/" + cluster,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, clusterHost(tc.baseHost, cluster))
		})
	}
}
