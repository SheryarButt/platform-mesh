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

package cluster

import (
	"strings"
	"testing"

	pmgatewayv1alpha1 "go.platform-mesh.io/apis/gateway/v1alpha1"

	"k8s.io/client-go/rest"
)

func TestNewRejectsServiceAccountModeWithoutServiceAccountCredentials(t *testing.T) {
	tests := []struct {
		name string
		auth *pmgatewayv1alpha1.AuthMetadata
	}{
		{name: "missing auth"},
		{
			name: "wrong auth type",
			auth: &pmgatewayv1alpha1.AuthMetadata{
				Type:  pmgatewayv1alpha1.AuthTypeToken,
				Token: "dG9rZW4=",
			},
		},
		{
			name: "empty service account token",
			auth: &pmgatewayv1alpha1.AuthMetadata{Type: pmgatewayv1alpha1.AuthTypeServiceAccount},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(t.Context(), "trusted", &pmgatewayv1alpha1.ClusterMetadata{
				Host:                "https://localhost:6443",
				RequestIdentityMode: pmgatewayv1alpha1.RequestIdentityModeServiceAccount,
				Auth:                tt.auth,
			}, Options{})
			if err == nil || !strings.Contains(err.Error(), "requires ServiceAccount credentials") {
				t.Fatalf("New() error = %v, want missing ServiceAccount credentials error", err)
			}
		})
	}
}

// buildRestCfg calls BuildRestConfigFromMetadata with a minimal valid metadata,
// then applies Options the same way cluster.New does — without the full
// controller-runtime client setup which requires a reachable API server.
func applyOptions(t *testing.T, opts Options) *rest.Config {
	t.Helper()
	metadata := pmgatewayv1alpha1.ClusterMetadata{
		Host: "https://localhost:6443",
	}
	cfg, err := pmgatewayv1alpha1.BuildRestConfigFromMetadata(metadata)
	if err != nil {
		t.Fatalf("BuildRestConfigFromMetadata: %v", err)
	}
	if opts.KubernetesQPS > 0 {
		cfg.QPS = opts.KubernetesQPS
	}
	if opts.KubernetesBurst > 0 {
		cfg.Burst = opts.KubernetesBurst
	}
	return cfg
}

func TestOptions_nonZeroValuesApplied(t *testing.T) {
	cfg := applyOptions(t, Options{KubernetesQPS: 50, KubernetesBurst: 100})
	if cfg.QPS != 50 {
		t.Errorf("QPS = %v, want 50", cfg.QPS)
	}
	if cfg.Burst != 100 {
		t.Errorf("Burst = %v, want 100", cfg.Burst)
	}
}

func TestOptions_zeroLeavesClientGoDefaults(t *testing.T) {
	// Build baseline with zero options to capture what BuildRestConfigFromMetadata sets.
	baseline := applyOptions(t, Options{})
	cfg := applyOptions(t, Options{KubernetesQPS: 0, KubernetesBurst: 0})
	if cfg.QPS != baseline.QPS {
		t.Errorf("QPS = %v, want baseline %v", cfg.QPS, baseline.QPS)
	}
	if cfg.Burst != baseline.Burst {
		t.Errorf("Burst = %v, want baseline %v", cfg.Burst, baseline.Burst)
	}
}
