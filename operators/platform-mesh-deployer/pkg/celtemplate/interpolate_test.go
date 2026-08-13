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

func moduleContext() Context {
	return Context{
		PlatformMesh:      "customer-a",
		Component:         "agent",
		ShardGroup:        "default",
		Cluster:           "172-18-0-5",
		OCMModule:         "acme",
		Placement:         "per-shard",
		TargetNamespace:   "acme-system",
		ConfigMap:         "acme-agent",
		Workspace:         "root:modules:acme",
		Workspaces:        map[string]string{"validation": "root:modules:acme:validation"},
		KubeconfigSecrets: map[string]string{"kcp": "acme-kcp"},
		Endpoints:         map[string]string{"api": "https://kcp.example.com"},
		Values:            map[string]any{"replicas": int64(2), "debug": true, "image": "acme:1.0"},
	}
}

func TestInterpolate(t *testing.T) {
	ctx := moduleContext()

	tests := []struct {
		name    string
		in      string
		want    any
		wantErr bool
	}{
		{name: "no expression", in: "plain-text", want: "plain-text"},
		{name: "empty string", in: "", want: ""},
		{name: "whole leaf string", in: "${module}", want: "acme"},
		{name: "mixed literal and expression", in: "${module}-agent", want: "acme-agent"},
		{name: "multiple expressions", in: "${module}-${component}", want: "acme-agent"},
		{name: "map index", in: "${kubeconfigSecrets.kcp}", want: "acme-kcp"},
		{name: "workspace child", in: "${workspaces.validation}", want: "root:modules:acme:validation"},
		{name: "endpoint", in: "${endpoints.api}", want: "https://kcp.example.com"},
		{name: "targetNamespace and configMap", in: "${targetNamespace}/${configMap}", want: "acme-system/acme-agent"},

		// A whole-leaf expression keeps the native type so non-string
		// manifest fields such as replicas stay numeric.
		{name: "whole leaf int", in: "${values.replicas}", want: int64(2)},
		{name: "whole leaf bool", in: "${values.debug}", want: true},

		{name: "cel string function", in: "${cluster.upperAscii()}", want: "172-18-0-5"},
		{name: "cel expression with braces", in: `${{"a": "1"}["a"]}`, want: "1"},
		{name: "quoted brace inside expression", in: `${"}" + module}`, want: "}acme"},
		{name: "single quoted brace inside expression", in: `${'}' + module}`, want: "}acme"},
		{name: "whitespace around expression", in: "${ module }", want: "acme"},

		// A mixed leaf becomes a CEL concatenation, so embedding a
		// non-string needs an explicit conversion.
		{name: "int embedded in text needs conversion", in: "replicas=${values.replicas}", wantErr: true},
		{name: "converted int embedded in text", in: `${"replicas=" + string(values.replicas)}`, want: "replicas=2"},

		// "$${" keeps shell-style ${VAR} in payloads verbatim, while a
		// bare "$$" stays untouched so shell PIDs survive.
		{name: "escaped expression", in: "$${HOME}", want: "${HOME}"},
		{name: "escaped expression mixed with a real one", in: "$${HOME}/${module}", want: "${HOME}/acme"},
		{name: "bare double dollar is not an escape", in: "echo $$", want: "echo $$"},
		{name: "dollar without brace", in: "cost $5", want: "cost $5"},

		{name: "unterminated expression", in: "${module", want: "${module"},
		{name: "empty expression", in: "${}", wantErr: true},
		{name: "unknown variable", in: "${nope}", wantErr: true},
		{name: "unterminated string literal", in: `${"abc}`, want: `${"abc}`},
		{name: "nested expression", in: `${outer(${module})}`, wantErr: true},
		{name: "nested expression inside a string literal", in: `${"${x}" + module}`, want: "${x}acme"},
		{name: "map embedded in text", in: `x${{"a": "1"}}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Interpolate(tt.in, ctx)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInterpolateValue(t *testing.T) {
	ctx := moduleContext()

	in := map[string]any{
		"metadata": map[string]any{
			"name":      "${module}-agent",
			"namespace": "${targetNamespace}",
			"labels":    map[string]any{"${module}": "${placement}"},
		},
		"spec": map[string]any{
			"replicas": "${values.replicas}",
			"paused":   "${values.debug}",
			"template": map[string]any{
				"volumes": []any{
					map[string]any{"secret": map[string]any{"secretName": "${kubeconfigSecrets.kcp}"}},
				},
				"args": []any{"--workspace=${workspace}", "--literal"},
			},
			"port": int64(8443),
		},
	}

	got, err := InterpolateValue(in, ctx)
	require.NoError(t, err)

	want := map[string]any{
		"metadata": map[string]any{
			"name":      "acme-agent",
			"namespace": "acme-system",
			"labels":    map[string]any{"acme": "per-shard"},
		},
		"spec": map[string]any{
			"replicas": int64(2),
			"paused":   true,
			"template": map[string]any{
				"volumes": []any{
					map[string]any{"secret": map[string]any{"secretName": "acme-kcp"}},
				},
				"args": []any{"--workspace=root:modules:acme", "--literal"},
			},
			"port": int64(8443),
		},
	}
	assert.Equal(t, want, got)
}

func TestInterpolateValueErrors(t *testing.T) {
	ctx := moduleContext()

	tests := []struct {
		name string
		in   any
	}{
		{name: "bad expression in nested map", in: map[string]any{"a": map[string]any{"b": "${nope}"}}},
		{name: "bad expression in list", in: map[string]any{"a": []any{"${nope}"}}},
		{name: "bad expression in key", in: map[string]any{"${nope}": "v"}},
		{name: "non-string key result", in: map[string]any{"${values.replicas}": "v"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := InterpolateValue(tt.in, ctx)
			require.Error(t, err)
		})
	}
}

func TestInterpolateValuePassesThroughNonStrings(t *testing.T) {
	got, err := InterpolateValue(int64(3), Context{})
	require.NoError(t, err)
	assert.Equal(t, int64(3), got)

	got, err = InterpolateValue(nil, Context{})
	require.NoError(t, err)
	assert.Nil(t, got)
}

// Unset module fields must evaluate rather than fail, so topology callers can
// share the environment.
func TestInterpolateWithEmptyContext(t *testing.T) {
	got, err := Interpolate("${module}-${workspace}", Context{})
	require.NoError(t, err)
	assert.Equal(t, "-", got)

	_, err = Interpolate("${values.missing}", Context{})
	require.Error(t, err)
}
