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

package celtemplate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEval(t *testing.T) {
	ctx := Context{PlatformMesh: "customer-a", Component: "rootshard", ShardGroup: "default", Cluster: "east"}

	tests := []struct {
		name    string
		expr    string
		want    string
		wantErr bool
	}{
		{
			name: "etcd endpoint from platformMesh",
			expr: `"https://etcd-" + platformMesh + ".platform-mesh-system:2379"`,
			want: "https://etcd-customer-a.platform-mesh-system:2379",
		},
		{
			name: "shardGroup and cluster",
			expr: `platformMesh + "-" + shardGroup + "-" + cluster`,
			want: "customer-a-default-east",
		},
		{
			name: "cel function",
			expr: `"kcp-" + cluster.upperAscii()`,
			want: "kcp-EAST",
		},
		{
			name:    "non-string result",
			expr:    `1 + 2`,
			wantErr: true,
		},
		{
			name:    "unknown variable",
			expr:    `unknownVar`,
			wantErr: true,
		},
		{
			name:    "syntax error",
			expr:    `"unterminated`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Eval(tt.expr, ctx)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEvalCachesProgram(t *testing.T) {
	const expr = `"x-" + cluster`
	a, err := Eval(expr, Context{Cluster: "a"})
	require.NoError(t, err)
	assert.Equal(t, "x-a", a)

	b, err := Eval(expr, Context{Cluster: "b"})
	require.NoError(t, err)
	assert.Equal(t, "x-b", b)
}
