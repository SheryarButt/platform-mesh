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

package ocmmodule

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/celtemplate"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/names"
	pmocmmodule "go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	operatorv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/operator/v1alpha1"
)

func internalScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, pmdeployv1alpha1.AddToScheme(s))
	require.NoError(t, operatorv1alpha1.AddToScheme(s))
	return s
}

// internalReconciler builds a reconciler with the PlatformMesh already
// resolved, which is all the naming helpers below read.
func internalReconciler(t *testing.T, engaged ...string) *reconciler {
	t.Helper()
	reg := clusters.NewRegistry()
	for _, n := range engaged {
		require.NoError(t, reg.Engage(context.Background(), multicluster.ClusterName(n), nil))
	}
	cl := fake.NewClientBuilder().WithScheme(internalScheme(t)).Build()
	r := newReconciler(t, cl, reg, nil, &pmdeployv1alpha1.OCMModule{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", Namespace: "pm"},
	})
	r.pm = &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm"},
		Spec: pmdeployv1alpha1.PlatformMeshSpec{
			Topology: pmdeployv1alpha1.Topology{
				RootShard:  pmdeployv1alpha1.RootShard{Name: "root"},
				FrontProxy: pmdeployv1alpha1.FrontProxy{Name: "fp"},
			},
		},
	}
	return r
}

func instance(component string, placement pmdeployv1alpha1.Placement, clusterID, shardGroup string) pmocmmodule.Instance {
	return pmocmmodule.Instance{
		Component: pmdeployv1alpha1.OCMModuleComponent{
			Name: component, Placement: placement, Namespace: "acme-system",
		},
		Cluster:    clusters.Cluster{ClusterID: clusterID},
		ShardGroup: shardGroup,
	}
}

// A kubeconfig is minted against the topology object of its target, named
// "<spec name>-<cluster ID>".
func TestKubeconfigTarget(t *testing.T) {
	r := internalReconciler(t, "rootshard#customer-a--east", "frontproxy#customer-a--fp1")

	front, err := r.kubeconfigTarget(
		pmdeployv1alpha1.OCMModuleKubeconfig{Name: "kcp", Target: pmdeployv1alpha1.KubeconfigTargetFrontProxy},
		instance("app", pmdeployv1alpha1.PlacementRootShard, "east", ""))
	require.NoError(t, err)
	require.NotNil(t, front.FrontProxyRef)
	assert.Equal(t, names.FrontProxy("customer-a", "fp", "fp1"), front.FrontProxyRef.Name)

	root, err := r.kubeconfigTarget(
		pmdeployv1alpha1.OCMModuleKubeconfig{Name: "kcp", Target: pmdeployv1alpha1.KubeconfigTargetRootShard},
		instance("app", pmdeployv1alpha1.PlacementRootShard, "east", ""))
	require.NoError(t, err)
	require.NotNil(t, root.RootShardRef)
	assert.Equal(t, names.RootShard("customer-a", "root", "east"), root.RootShardRef.Name)

	shard, err := r.kubeconfigTarget(
		pmdeployv1alpha1.OCMModuleKubeconfig{Name: "kcp", Target: pmdeployv1alpha1.KubeconfigTargetShard},
		instance("agent", pmdeployv1alpha1.PlacementPerShard, "s1", "default"))
	require.NoError(t, err)
	require.NotNil(t, shard.ShardRef)
	assert.Equal(t, names.Shard("customer-a", "default", "s1"), shard.ShardRef.Name)
}

func TestKubeconfigTargetErrors(t *testing.T) {
	tests := []struct {
		name    string
		engaged []string
		kc      pmdeployv1alpha1.OCMModuleKubeconfig
		inst    pmocmmodule.Instance
		wantErr string
	}{
		{
			name:    "shard target on a component that is not per shard",
			engaged: []string{"rootshard#customer-a--east"},
			kc:      pmdeployv1alpha1.OCMModuleKubeconfig{Name: "kcp", Target: pmdeployv1alpha1.KubeconfigTargetShard},
			inst:    instance("app", pmdeployv1alpha1.PlacementRootShard, "east", ""),
			wantErr: "not placed per shard",
		},
		{
			name:    "no front proxy engaged",
			kc:      pmdeployv1alpha1.OCMModuleKubeconfig{Name: "kcp", Target: pmdeployv1alpha1.KubeconfigTargetFrontProxy},
			inst:    instance("app", pmdeployv1alpha1.PlacementRootShard, "east", ""),
			wantErr: "no frontproxy cluster engaged",
		},
		{
			name:    "several front proxies",
			engaged: []string{"frontproxy#customer-a--a", "frontproxy#customer-a--b"},
			kc:      pmdeployv1alpha1.OCMModuleKubeconfig{Name: "kcp", Target: pmdeployv1alpha1.KubeconfigTargetFrontProxy},
			inst:    instance("app", pmdeployv1alpha1.PlacementRootShard, "east", ""),
			wantErr: "not supported yet",
		},
		{
			name:    "unknown target",
			kc:      pmdeployv1alpha1.OCMModuleKubeconfig{Name: "kcp", Target: pmdeployv1alpha1.KubeconfigTarget("nowhere")},
			inst:    instance("app", pmdeployv1alpha1.PlacementRootShard, "east", ""),
			wantErr: "unknown target",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := internalReconciler(t, tt.engaged...)
			_, err := r.kubeconfigTarget(tt.kc, tt.inst)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// The front proxy verifies the certificate the backend presents against the name
// it dialled, so the names on the certificate are decided by which kind of
// backend the mapping names.
func TestBackendDNSNames(t *testing.T) {
	// In-cluster: all four spellings, because the authority the mapping resolves
	// to is only one of them and any caller inside the cluster may use another.
	t.Run("a Service backend covers every in-cluster spelling", func(t *testing.T) {
		m := &pmdeployv1alpha1.Mapping{Service: "acme-vw", Port: 6443}
		assert.Equal(t, []string{
			"acme-vw",
			"acme-vw.acme-system",
			"acme-vw.acme-system.svc",
			"acme-vw.acme-system.svc.cluster.local",
		}, backendDNSNames(m, "acme-vw.acme-system.svc"))
	})

	// Out-of-cluster: that name alone. The in-cluster spellings do not resolve
	// where this backend is reached from, and putting them on the certificate
	// would only widen what it vouches for.
	t.Run("a host backend covers only the host", func(t *testing.T) {
		m := &pmdeployv1alpha1.Mapping{Host: "tenancy.example.com", Port: 6443}
		assert.Equal(t, []string{"tenancy.example.com"}, backendDNSNames(m, "tenancy.example.com"))
	})
}

func TestRootShardIssuer(t *testing.T) {
	r := internalReconciler(t, "rootshard#customer-a--east")
	issuer, err := r.rootShardIssuer()
	require.NoError(t, err)
	assert.Equal(t, names.RootShard("customer-a", "root", "east")+"-server-ca", issuer)

	r = internalReconciler(t)
	_, err = r.rootShardIssuer()
	require.ErrorContains(t, err, "exactly one root shard")
}

// The requestheader CA is a kcp-operator secret named after the root shard, so
// waiting for it is ordinary progress rather than a failure.
func TestRequestHeaderCAPending(t *testing.T) {
	r := internalReconciler(t, "rootshard#customer-a--east")
	inst := instance("vw", pmdeployv1alpha1.PlacementPerFrontProxy, "fp1", "")

	err := r.ensureRequestHeaderCA(context.Background(), inst)
	require.ErrorIs(t, err, errRequestHeaderCAPending)
	assert.Contains(t, err.Error(), names.RootShard("customer-a", "root", "east")+"-requestheader-client-ca")
}

// The mapping is templated per instance, so the backend is only known after
// interpolation.
func TestResolveMapping(t *testing.T) {
	inst := instance("vw", pmdeployv1alpha1.PlacementPerFrontProxy, "fp1", "")
	inst.Component.Mapping = &pmdeployv1alpha1.Mapping{
		Path:    "/services/acme/",
		Service: `${module + "-" + component}`,
		Port:    8443,
	}
	celCtx := celtemplate.Context{OCMModule: "acme", Component: "vw"}

	got, err := resolveMapping(inst, celCtx)
	require.NoError(t, err)
	assert.Equal(t, "/services/acme/", got.Path)
	assert.Equal(t, "https://acme-vw.acme-system.svc:8443", got.Backend)
}

// A component placed on a cluster the front proxy cannot reach by Service DNS
// names the address it IS reachable at. The component's namespace is not part of
// that address — it is a fact about the other cluster, and appending it produced
// a backend that resolves nowhere.
func TestResolveMappingHost(t *testing.T) {
	inst := instance("vw", pmdeployv1alpha1.PlacementPerFrontProxy, "fp1", "")
	inst.Component.Mapping = &pmdeployv1alpha1.Mapping{
		Path: "/services/acme/",
		Host: `${"tenancy." + cluster + ".example.com"}`,
		Port: 6443,
	}

	got, err := resolveMapping(inst, celtemplate.Context{Cluster: "east"})
	require.NoError(t, err)
	assert.Equal(t, "/services/acme/", got.Path)
	assert.Equal(t, "https://tenancy.east.example.com:6443", got.Backend)
}

// An expression that resolves to nothing would otherwise build
// "https://.acme-system.svc:8443" — a backend the front proxy accepts and can
// never dial.
func TestResolveMappingRejectsEmptyBackend(t *testing.T) {
	inst := instance("vw", pmdeployv1alpha1.PlacementPerFrontProxy, "fp1", "")
	inst.Component.Mapping = &pmdeployv1alpha1.Mapping{
		Path: "/services/acme/", Service: "${values.missing}", Port: 8443,
	}

	_, err := resolveMapping(inst, celtemplate.Context{Values: map[string]any{"missing": ""}})
	require.ErrorContains(t, err, "empty string")
}

func TestResolveMappingRejectsBadTemplates(t *testing.T) {
	tests := []struct{ name, service, path string }{
		{name: "bad service", service: "${nope}", path: "/services/acme/"},
		{name: "bad path", service: "svc", path: "${nope}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := instance("vw", pmdeployv1alpha1.PlacementPerFrontProxy, "fp1", "")
			inst.Component.Mapping = &pmdeployv1alpha1.Mapping{
				Path: tt.path, Service: tt.service, Port: 8443,
			}
			_, err := resolveMapping(inst, celtemplate.Context{})
			require.Error(t, err)
		})
	}
}

func TestToAnySlice(t *testing.T) {
	assert.Equal(t, []any{"a", "b"}, toAnySlice([]string{"a", "b"}))
	assert.Empty(t, toAnySlice(nil))
}

// "Exactly one of service/host" is enforced at admission, and again here — a CEL
// rule is evaluated on write, so an object stored before the rule existed, or a
// binary running against a CRD that predates it, reaches this code unchecked.
func TestResolveMappingRequiresExactlyOneBackend(t *testing.T) {
	tests := map[string]struct {
		mapping *pmdeployv1alpha1.Mapping
		wantErr string
	}{
		// Neither is silently preferred, because either choice routes to a
		// backend the module author did not name.
		"both": {
			mapping: &pmdeployv1alpha1.Mapping{
				Path: "/services/acme/", Service: "acme-vw", Host: "acme.example.com", Port: 8443,
			},
			wantErr: "sets both service",
		},
		"neither": {
			mapping: &pmdeployv1alpha1.Mapping{Path: "/services/acme/", Port: 8443},
			wantErr: "sets neither service nor host",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			inst := instance("vw", pmdeployv1alpha1.PlacementPerFrontProxy, "fp1", "")
			inst.Component.Mapping = tc.mapping

			_, err := resolveMapping(inst, celtemplate.Context{})
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// An IPv6 host has to be bracketed before the port is appended, or the address
// and the port run together into something no client can dial. This is what
// net.JoinHostPort is for, and why the backend is not built with a format
// string.
func TestResolveMappingIPv6Host(t *testing.T) {
	inst := instance("vw", pmdeployv1alpha1.PlacementPerFrontProxy, "fp1", "")
	inst.Component.Mapping = &pmdeployv1alpha1.Mapping{
		Path: "/services/acme/", Host: "2001:db8::1", Port: 6443,
	}

	got, err := resolveMapping(inst, celtemplate.Context{})
	require.NoError(t, err)
	assert.Equal(t, "https://[2001:db8::1]:6443", got.Backend)
}
