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

package kcp

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/names"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	operatorv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/operator/v1alpha1"
	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// adminKubeconfig is what kcp-operator mints: a workspace-scoped server URL.
const adminKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: kcp
  cluster:
    server: https://fp.example.com:6443/clusters/root
contexts:
- name: kcp
  context:
    cluster: kcp
    user: kcp
current-context: kcp
users:
- name: kcp
  user:
    token: secret
`

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, pmdeployv1alpha1.AddToScheme(s))
	require.NoError(t, operatorv1alpha1.AddToScheme(s))
	require.NoError(t, kcptenancyv1alpha1.AddToScheme(s))
	require.NoError(t, kcpcorev1alpha1.AddToScheme(s))
	return s
}

func testPlatformMesh() *pmdeployv1alpha1.PlatformMesh {
	return &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm"},
		Spec: pmdeployv1alpha1.PlatformMeshSpec{
			Topology: pmdeployv1alpha1.Topology{
				FrontProxy: pmdeployv1alpha1.FrontProxy{Name: "fp"},
			},
		},
	}
}

func registryWith(t *testing.T, names ...string) *clusters.Registry {
	t.Helper()
	r := clusters.NewRegistry()
	for _, n := range names {
		require.NoError(t, r.Engage(context.Background(), multicluster.ClusterName(n), nil))
	}
	return r
}

func TestKubeconfigName(t *testing.T) {
	assert.Equal(t, "customer-a-provisioner", KubeconfigName("customer-a"))
}

// The first pass mints the Kubeconfig and reports pending; kcp-operator writes
// the secret asynchronously.
func TestConfigMintsKubeconfigAndWaits(t *testing.T) {
	pm := testPlatformMesh()
	s := testScheme(t)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(pm).Build()
	access := New(cl, registryWith(t, "frontproxy#customer-a--fp1"), s, nil)

	_, err := access.Config(t.Context(), pm)
	require.ErrorIs(t, err, ErrPending)

	kc := &operatorv1alpha1.Kubeconfig{}
	require.NoError(t, cl.Get(t.Context(),
		ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: "customer-a-provisioner"}, kc))
	assert.Equal(t, "root", kc.Spec.TargetWorkspace)
	assert.Equal(t, []string{"system:kcp:admin"}, kc.Spec.Groups)
	require.NotNil(t, kc.Spec.Target.FrontProxyRef)
	assert.Equal(t, names.FrontProxy("customer-a", "fp", "fp1"), kc.Spec.Target.FrontProxyRef.Name)
}

// Once the secret exists the config points at the front proxy origin, with the
// workspace path stripped so callers can append their own.
func TestConfigStripsWorkspacePath(t *testing.T) {
	pm := testPlatformMesh()
	s := testScheme(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "pm", Name: "customer-a-provisioner"},
		Data:       map[string][]byte{"kubeconfig": []byte(adminKubeconfig)},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(pm, secret).Build()
	access := New(cl, registryWith(t, "frontproxy#customer-a--fp1"), s, nil)

	cfg, err := access.Config(t.Context(), pm)
	require.NoError(t, err)
	assert.Equal(t, "https://fp.example.com:6443", cfg.Host)
	assert.Nil(t, cfg.Dial, "no dialer unless one was configured")
}

// The dialer exists because the e2e reaches kcp from outside the cluster.
func TestConfigAppliesDialer(t *testing.T) {
	pm := testPlatformMesh()
	s := testScheme(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "pm", Name: "customer-a-provisioner"},
		Data:       map[string][]byte{"kubeconfig": []byte(adminKubeconfig)},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(pm, secret).Build()

	dial := func(context.Context, string, string) (net.Conn, error) { return nil, nil }
	access := New(cl, registryWith(t, "frontproxy#customer-a--fp1"), s, dial)

	cfg, err := access.Config(t.Context(), pm)
	require.NoError(t, err)
	assert.NotNil(t, cfg.Dial)
}

func TestConfigRequiresExactlyOneFrontProxy(t *testing.T) {
	tests := []struct {
		name    string
		engaged []string
	}{
		{name: "none engaged"},
		{name: "two engaged", engaged: []string{"frontproxy#customer-a--a", "frontproxy#customer-a--b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := testPlatformMesh()
			s := testScheme(t)
			cl := fake.NewClientBuilder().WithScheme(s).WithObjects(pm).Build()
			access := New(cl, registryWith(t, tt.engaged...), s, nil)

			_, err := access.Config(t.Context(), pm)
			require.ErrorContains(t, err, "exactly one front proxy")
		})
	}
}

func TestConfigRejectsUnparsableKubeconfig(t *testing.T) {
	pm := testPlatformMesh()
	s := testScheme(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "pm", Name: "customer-a-provisioner"},
		Data:       map[string][]byte{"kubeconfig": []byte("not a kubeconfig")},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(pm, secret).Build()
	access := New(cl, registryWith(t, "frontproxy#customer-a--fp1"), s, nil)

	_, err := access.Config(t.Context(), pm)
	require.ErrorContains(t, err, "parsing admin kubeconfig")
}
