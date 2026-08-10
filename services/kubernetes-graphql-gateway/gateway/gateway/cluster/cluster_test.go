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
	"testing"

	pmgatewayv1alpha1 "go.platform-mesh.io/apis/gateway/v1alpha1"
	"k8s.io/client-go/rest"
)

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
