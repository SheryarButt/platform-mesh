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

package platformmesh

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/names"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	operatorv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/operator/v1alpha1"
)

func platformMesh() *pmdeployv1alpha1.PlatformMesh {
	return &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm"},
		Spec: pmdeployv1alpha1.PlatformMeshSpec{
			Topology: pmdeployv1alpha1.Topology{
				RootShard: pmdeployv1alpha1.RootShard{
					Name:        "root",
					TemplateRef: &pmdeployv1alpha1.TemplateReference{Name: "root"},
					Exposure: &pmdeployv1alpha1.Exposure{
						HostnameTemplate: `"kcp." + platformMesh + ".example.com"`,
						Port:             6443,
					},
				},
				FrontProxy: pmdeployv1alpha1.FrontProxy{
					Name: "fp",
					Exposure: &pmdeployv1alpha1.Exposure{
						HostnameTemplate: `"fp." + platformMesh + ".example.com"`,
						Port:             6443,
					},
				},
			},
		},
	}
}

func rootShardTemplate() *pmdeployv1alpha1.RootShardTemplate {
	return &pmdeployv1alpha1.RootShardTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "root", Namespace: "pm"},
		Spec: operatorv1alpha1.RootShardSpec{
			CommonShardSpec: operatorv1alpha1.CommonShardSpec{
				Etcd: operatorv1alpha1.EtcdConfig{
					Endpoints: []string{`"https://etcd-" + platformMesh + ".pm:2379"`},
					Prefix:    `"/" + platformMesh + "/" + cluster`,
				},
			},
		},
	}
}

func shardTemplate() *pmdeployv1alpha1.ShardTemplate {
	return &pmdeployv1alpha1.ShardTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "eu", Namespace: "pm"},
		Spec: operatorv1alpha1.ShardSpec{
			CommonShardSpec: operatorv1alpha1.CommonShardSpec{
				Etcd: operatorv1alpha1.EtcdConfig{
					Endpoints: []string{`"https://etcd-" + platformMesh + ".pm:2379"`},
					Prefix:    `"/" + platformMesh + "/" + shardGroup + "/" + cluster`,
				},
			},
		},
	}
}

func TestReconcileRootShard(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), shardTemplate()).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")

	r := newReconciler(t, cl, reg, pm)
	_, err := r.reconcileTopology(t.Context())
	require.NoError(t, err)

	cond := meta.FindStatusCondition(pm.Status.Conditions, ConditionTopologyReady)
	require.NotNil(t, cond, "a rendered topology has to say so on the status")
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "Rendered", cond.Reason)
	assert.Equal(t, pm.Generation, cond.ObservedGeneration)

	rs := &operatorv1alpha1.RootShard{}
	require.NoError(t, cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: names.RootShard("customer-a", "root", "east")}, rs))
	require.Len(t, rs.Spec.Etcd.Endpoints, 1)
	assert.Equal(t, "https://etcd-customer-a.pm:2379", rs.Spec.Etcd.Endpoints[0])
	assert.Equal(t, "/customer-a/east", rs.Spec.Etcd.Prefix)
	assert.Equal(t, "fp.customer-a.example.com", rs.Spec.External.Hostname)
	assert.Equal(t, uint32(6443), rs.Spec.External.Port)
	assert.Equal(t, "https://kcp.customer-a.example.com:6443", rs.Spec.ShardBaseURL)
	assert.Equal(t, "customer-a", rs.Labels[components.LabelPlatformMesh])
	assert.Equal(t, components.RootShard, rs.Labels[components.LabelComponent])
	assert.Equal(t, "east", rs.Labels[components.LabelCluster])
	require.Len(t, rs.OwnerReferences, 1)
	assert.Equal(t, "customer-a", rs.OwnerReferences[0].Name)
}

func TestReconcileRootShardRejectsSecondCluster(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), shardTemplate()).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "rootshard#customer-a--west")

	r := newReconciler(t, cl, reg, pm)
	_, err := r.reconcileTopology(t.Context())
	require.Error(t, err)

	cond := meta.FindStatusCondition(pm.Status.Conditions, ConditionTopologyReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "RenderFailed", cond.Reason)
	assert.Contains(t, cond.Message, "root shard must be a single cluster")
	assert.Equal(t, pm.Generation, cond.ObservedGeneration)
}

func TestReconcileRootShardTeardownStale(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	stale := &operatorv1alpha1.RootShard{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.RootShard("customer-a", "root", "west"),
			Namespace: "pm",
			Labels: map[string]string{
				components.LabelPlatformMesh: "customer-a",
				components.LabelComponent:    components.RootShard,
				components.LabelCluster:      "west",
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, stale, rootShardTemplate(), shardTemplate()).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")

	r := newReconciler(t, cl, reg, pm)
	_, err := r.reconcileTopology(t.Context())
	require.NoError(t, err)

	require.NoError(t, cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: names.RootShard("customer-a", "root", "east")}, &operatorv1alpha1.RootShard{}))
	err = cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: names.RootShard("customer-a", "root", "west")}, &operatorv1alpha1.RootShard{})
	assert.True(t, apierrors.IsNotFound(err), "expected stale RootShard torn down, got %v", err)
}

func TestReconcileShard(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	pm.Spec.Topology.ShardGroups = []pmdeployv1alpha1.ShardGroup{{
		Name:           "eu",
		TemplateRef:    &pmdeployv1alpha1.TemplateReference{Name: "eu"},
		CacheServerRef: "cache",
		Exposure: &pmdeployv1alpha1.Exposure{
			HostnameTemplate: `component + "." + cluster + ".sslip.io"`,
			Port:             31443,
		},
	}}
	pm.Spec.Topology.CacheServer = &pmdeployv1alpha1.CacheServer{Name: "cache"}
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), shardTemplate()).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")
	engage(t, reg, "shards-eu#customer-a--west")
	engage(t, reg, "cacheserver#customer-a--cache1")

	r := newReconciler(t, cl, reg, pm)
	_, err := r.reconcileTopology(t.Context())
	require.NoError(t, err)

	sh := &operatorv1alpha1.Shard{}
	require.NoError(t, cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: names.Shard("customer-a", "eu", "west")}, sh))
	assert.Equal(t, []string{"https://etcd-customer-a.pm:2379"}, sh.Spec.Etcd.Endpoints)
	assert.Equal(t, "/customer-a/eu/west", sh.Spec.Etcd.Prefix)
	require.NotNil(t, sh.Spec.RootShard.Reference)
	assert.Equal(t, names.RootShard("customer-a", "root", "east"), sh.Spec.RootShard.Reference.Name)
	assert.Equal(t, "https://shards-eu.west.sslip.io:31443", sh.Spec.ShardBaseURL)
	require.NotNil(t, sh.Spec.Cache)
	require.NotNil(t, sh.Spec.Cache.Reference)
	assert.Equal(t, names.CacheServer("customer-a", "cache", "cache1"), sh.Spec.Cache.Reference.Name)
	assert.Equal(t, components.Shard("eu"), sh.Labels[components.LabelComponent])
	assert.Equal(t, "west", sh.Labels[components.LabelCluster])
}

func TestReconcileFrontProxy(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	pm.Spec.Topology.FrontProxy = pmdeployv1alpha1.FrontProxy{
		Name: "fp",
		Exposure: &pmdeployv1alpha1.Exposure{
			HostnameTemplate: `"api." + platformMesh + ".example.com"`,
			Port:             443,
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), shardTemplate()).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--west")

	r := newReconciler(t, cl, reg, pm)
	_, err := r.reconcileTopology(t.Context())
	require.NoError(t, err)

	fp := &operatorv1alpha1.FrontProxy{}
	require.NoError(t, cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: names.FrontProxy("customer-a", "fp", "west")}, fp))
	require.NotNil(t, fp.Spec.RootShard.Reference)
	assert.Equal(t, names.RootShard("customer-a", "root", "east"), fp.Spec.RootShard.Reference.Name)
	assert.Equal(t, "api.customer-a.example.com", fp.Spec.External.Hostname)
	assert.Equal(t, uint32(443), fp.Spec.External.Port)
	assert.Equal(t, components.FrontProxy, fp.Labels[components.LabelComponent])
	assert.Equal(t, "west", fp.Labels[components.LabelCluster])
}

func TestReconcileCacheServer(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	pm.Spec.Topology.CacheServer = &pmdeployv1alpha1.CacheServer{
		Name:        "global",
		TemplateRef: &pmdeployv1alpha1.TemplateReference{Name: "global"},
	}
	cacheTemplate := &pmdeployv1alpha1.CacheServerTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "global", Namespace: "pm"},
		Spec: operatorv1alpha1.CacheServerSpec{
			Etcd: &operatorv1alpha1.EtcdConfig{
				Endpoints: []string{`"https://cache-etcd-" + platformMesh + ".pm:2379"`},
				Prefix:    `"/" + platformMesh + "/cache"`,
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), shardTemplate(), cacheTemplate).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")
	engage(t, reg, "cacheserver#customer-a--west")

	r := newReconciler(t, cl, reg, pm)
	_, err := r.reconcileTopology(t.Context())
	require.NoError(t, err)

	cs := &operatorv1alpha1.CacheServer{}
	require.NoError(t, cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: names.CacheServer("customer-a", "global", "west")}, cs))
	require.NotNil(t, cs.Spec.Etcd)
	assert.Equal(t, []string{"https://cache-etcd-customer-a.pm:2379"}, cs.Spec.Etcd.Endpoints)
	assert.Equal(t, "/customer-a/cache", cs.Spec.Etcd.Prefix)
	assert.Equal(t, components.CacheServer, cs.Labels[components.LabelComponent])
	assert.Equal(t, "west", cs.Labels[components.LabelCluster])
}

func TestReconcileVirtualWorkspace(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	pm.Spec.Topology.RootShard.VirtualWorkspaces = pmdeployv1alpha1.VirtualWorkspaceSpec{
		Mode: pmdeployv1alpha1.VirtualWorkspaceModeStandalone,
		Exposure: &pmdeployv1alpha1.Exposure{
			HostnameTemplate: `"vw." + platformMesh + ".example.com"`,
			Port:             443,
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), shardTemplate()).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")

	r := newReconciler(t, cl, reg, pm)
	_, err := r.reconcileTopology(t.Context())
	require.NoError(t, err)

	vw := &operatorv1alpha1.VirtualWorkspace{}
	require.NoError(t, cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: names.VirtualWorkspace("customer-a", "root", "east")}, vw))
	require.NotNil(t, vw.Spec.Target.RootShardRef)
	assert.Equal(t, names.RootShard("customer-a", "root", "east"), vw.Spec.Target.RootShardRef.Name)
	assert.Equal(t, "vw.customer-a.example.com", vw.Spec.External.Hostname)
	assert.Equal(t, uint32(443), vw.Spec.External.Port)
	assert.Equal(t, components.VirtualWorkspace, vw.Labels[components.LabelComponent])
	assert.Equal(t, "east", vw.Labels[components.LabelCluster])
}

func TestReconcileVirtualWorkspaceEmbeddedSkipped(t *testing.T) {
	t.Parallel()
	pm := platformMesh() // root shard VirtualWorkspaces defaults to embedded (mode unset)
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), shardTemplate()).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")

	r := newReconciler(t, cl, reg, pm)
	_, err := r.reconcileTopology(t.Context())
	require.NoError(t, err)

	list := &operatorv1alpha1.VirtualWorkspaceList{}
	require.NoError(t, cl.List(t.Context(), list))
	assert.Empty(t, list.Items)
}

func TestReconcileNamesAreUniquePerPlatformMesh(t *testing.T) {
	t.Parallel()
	a := platformMesh()
	b := platformMesh()
	b.Name = "customer-b"

	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a, b, rootShardTemplate(), shardTemplate()).Build()
	reg := clusters.NewRegistry()
	for _, pm := range []string{"customer-a", "customer-b"} {
		engage(t, reg, multiclusterName("rootshard", pm, "east"))
		engage(t, reg, multiclusterName("frontproxy", pm, "east"))
	}

	for _, pm := range []*pmdeployv1alpha1.PlatformMesh{a, b} {
		_, err := newReconciler(t, cl, reg, pm).reconcileTopology(t.Context())
		require.NoError(t, err)
	}

	list := &operatorv1alpha1.RootShardList{}
	require.NoError(t, cl.List(t.Context(), list, ctrlruntimeclient.InNamespace("pm")))
	require.Len(t, list.Items, 2, "both installations must keep their own root shard")

	// Each root shard belongs to exactly one PlatformMesh and points at its own front proxy.
	byName := map[string]string{}
	for _, rs := range list.Items {
		byName[rs.Name] = rs.Labels[components.LabelPlatformMesh]
	}
	assert.Equal(t, map[string]string{
		names.RootShard("customer-a", "root", "east"): "customer-a",
		names.RootShard("customer-b", "root", "east"): "customer-b",
	}, byName)

	fps := &operatorv1alpha1.FrontProxyList{}
	require.NoError(t, cl.List(t.Context(), fps, ctrlruntimeclient.InNamespace("pm")))
	require.Len(t, fps.Items, 2)
	for _, fp := range fps.Items {
		pm := fp.Labels[components.LabelPlatformMesh]
		assert.Equal(t, names.RootShard(pm, "root", "east"), fp.Spec.RootShard.Reference.Name)
	}
}

func multiclusterName(component, platformMesh, clusterID string) string {
	return component + "#" + platformMesh + "--" + clusterID
}

func TestReconcileCacheServerRef(t *testing.T) {
	t.Parallel()
	shardGroup := func(ref string) []pmdeployv1alpha1.ShardGroup {
		return []pmdeployv1alpha1.ShardGroup{{
			Name:           "eu",
			CacheServerRef: ref,
			Exposure: &pmdeployv1alpha1.Exposure{
				HostnameTemplate: `component + "." + cluster + ".sslip.io"`,
				Port:             31443,
			},
		}}
	}

	for name, tc := range map[string]struct {
		cacheServer *pmdeployv1alpha1.CacheServer
		engaged     bool
		ref         string
		wantErr     string
	}{
		"undefined cache server": {
			ref:     "cache",
			wantErr: `cacheServerRef "cache" set but no cache server defined`,
		},
		"ref does not match": {
			cacheServer: &pmdeployv1alpha1.CacheServer{Name: "global"},
			engaged:     true,
			ref:         "cache",
			wantErr:     `cacheServerRef "cache" does not match cache server "global"`,
		},
		"not engaged yet": {
			cacheServer: &pmdeployv1alpha1.CacheServer{Name: "cache"},
			ref:         "cache",
			wantErr:     `cache server "cache" not ready`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			pm := platformMesh()
			pm.Spec.Topology.ShardGroups = shardGroup(tc.ref)
			pm.Spec.Topology.CacheServer = tc.cacheServer

			cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), shardTemplate()).Build()
			reg := clusters.NewRegistry()
			engage(t, reg, "rootshard#customer-a--east")
			engage(t, reg, "frontproxy#customer-a--fp")
			engage(t, reg, "shards-eu#customer-a--west")
			if tc.engaged {
				engage(t, reg, "cacheserver#customer-a--cache1")
			}

			_, err := newReconciler(t, cl, reg, pm).reconcileTopology(t.Context())
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestReconcileTemplateRef(t *testing.T) {
	t.Parallel()
	t.Run("nil ref renders a zero spec", func(t *testing.T) {
		pm := platformMesh()
		pm.Spec.Topology.RootShard.TemplateRef = nil

		cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm).Build()
		reg := clusters.NewRegistry()
		engage(t, reg, "rootshard#customer-a--east")
		engage(t, reg, "frontproxy#customer-a--fp")

		_, err := newReconciler(t, cl, reg, pm).reconcileTopology(t.Context())
		require.NoError(t, err)

		rs := &operatorv1alpha1.RootShard{}
		require.NoError(t, cl.Get(t.Context(),
			ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: names.RootShard("customer-a", "root", "east")}, rs))
		assert.Nil(t, rs.Spec.Etcd.Endpoints)
	})

	t.Run("dangling ref errors", func(t *testing.T) {
		pm := platformMesh()
		pm.Spec.Topology.RootShard.TemplateRef = &pmdeployv1alpha1.TemplateReference{Name: "gone"}

		cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm).Build()
		reg := clusters.NewRegistry()
		engage(t, reg, "rootshard#customer-a--east")
		engage(t, reg, "frontproxy#customer-a--fp")

		_, err := newReconciler(t, cl, reg, pm).reconcileTopology(t.Context())
		require.ErrorContains(t, err, "template pm/gone")
	})

	t.Run("shared across namespaces", func(t *testing.T) {
		shared := rootShardTemplate()
		shared.Namespace = "shared"

		a := platformMesh()
		a.Spec.Topology.RootShard.TemplateRef = &pmdeployv1alpha1.TemplateReference{Name: "root", Namespace: "shared"}
		b := platformMesh()
		b.Name, b.Namespace = "customer-b", "pm-b"
		b.Spec.Topology.RootShard.TemplateRef = &pmdeployv1alpha1.TemplateReference{Name: "root", Namespace: "shared"}

		cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(a, b, shared).Build()
		reg := clusters.NewRegistry()
		for _, pm := range []string{"customer-a", "customer-b"} {
			engage(t, reg, multiclusterName("rootshard", pm, "east"))
			engage(t, reg, multiclusterName("frontproxy", pm, "east"))
		}

		for _, pm := range []*pmdeployv1alpha1.PlatformMesh{a, b} {
			_, err := newReconciler(t, cl, reg, pm).reconcileTopology(t.Context())
			require.NoError(t, err)
		}

		// Both installations picked up the shared template, each with its own
		// CEL context.
		for _, tc := range []struct{ pm, namespace, prefix string }{
			{"customer-a", "pm", "/customer-a/east"},
			{"customer-b", "pm-b", "/customer-b/east"},
		} {
			rs := &operatorv1alpha1.RootShard{}
			require.NoError(t, cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{
				Namespace: tc.namespace, Name: names.RootShard(tc.pm, "root", "east"),
			}, rs))
			assert.Equal(t, tc.prefix, rs.Spec.Etcd.Prefix)
		}
	})
}

// A topology with no exposures describes a kcp nothing outside the cluster
// reaches, so every component advertises the Service kcp-operator gives it.
func TestUnexposedTopologyAddressesInCluster(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	pm.Spec.Topology.RootShard.Exposure = nil
	pm.Spec.Topology.FrontProxy.Exposure = nil
	pm.Spec.Topology.ShardGroups = []pmdeployv1alpha1.ShardGroup{{
		Name:        "eu",
		TemplateRef: &pmdeployv1alpha1.TemplateReference{Name: "eu"},
	}}

	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), shardTemplate()).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--east")
	engage(t, reg, "shards-eu#customer-a--east")
	r := newReconciler(t, cl, reg, pm)

	rootName := names.RootShard("customer-a", "root", "east")
	fpName := names.FrontProxy("customer-a", "fp", "east")
	shardName := names.Shard("customer-a", "eu", "east")

	rs, err := r.buildRootShardSpec(t.Context(), pm, pm.Spec.Topology.RootShard, "east")
	require.NoError(t, err)
	// The root shard reaches the other shards through the front proxy.
	assert.Equal(t, fpName+"-front-proxy.pm.svc", rs.External.Hostname)
	assert.Equal(t, uint32(6443), rs.External.Port)
	assert.Equal(t, "https://"+rootName+"-kcp.pm.svc:6443", rs.ShardBaseURL)

	fp, err := r.buildFrontProxySpec(t.Context(), pm, pm.Spec.Topology.FrontProxy, "east", rootName)
	require.NoError(t, err)
	assert.Equal(t, fpName+"-front-proxy.pm.svc", fp.External.Hostname)

	sh, err := r.buildShardSpec(t.Context(), pm, pm.Spec.Topology.ShardGroups[0], "east", rootName)
	require.NoError(t, err)
	assert.Equal(t, "https://"+shardName+"-shard-kcp.pm.svc:6443", sh.ShardBaseURL)
}

func TestBuildFrontProxySpecRejectsDropGroups(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	tpl := &pmdeployv1alpha1.FrontProxyTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "fp", Namespace: "pm"},
		Spec: operatorv1alpha1.FrontProxySpec{
			Auth: &operatorv1alpha1.AuthSpec{DropGroups: []string{"system:kcp:admin"}},
		},
	}
	pm.Spec.Topology.FrontProxy.TemplateRef = &pmdeployv1alpha1.TemplateReference{Name: "fp"}

	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), tpl).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "rootshard#customer-a--east")
	engage(t, reg, "frontproxy#customer-a--fp")
	r := newReconciler(t, cl, reg, pm)

	_, err := r.buildFrontProxySpec(t.Context(), pm, pm.Spec.Topology.FrontProxy, "fp", "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.dropGroups")
}

// A standalone virtual workspace server is only used if the shard is pointed at
// it; otherwise kcp-operator keeps serving them in-process and the deployment
// it renders takes no traffic.
func TestStandaloneVirtualWorkspaceIsWiredToItsShard(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		mode pmdeployv1alpha1.VirtualWorkspaceMode
		want bool
	}{
		{name: "standalone is referenced", mode: pmdeployv1alpha1.VirtualWorkspaceModeStandalone, want: true},
		{name: "embedded is not", mode: pmdeployv1alpha1.VirtualWorkspaceModeEmbedded, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pm := platformMesh()
			pm.Spec.Topology.RootShard.VirtualWorkspaces = pmdeployv1alpha1.VirtualWorkspaceSpec{
				Mode:     tc.mode,
				Exposure: &pmdeployv1alpha1.Exposure{HostnameTemplate: `"vw.example.com"`, Port: 6443},
			}
			pm.Spec.Topology.ShardGroups = []pmdeployv1alpha1.ShardGroup{{
				Name:        "eu",
				TemplateRef: &pmdeployv1alpha1.TemplateReference{Name: "eu"},
				VirtualWorkspaces: pmdeployv1alpha1.VirtualWorkspaceSpec{
					Mode:     tc.mode,
					Exposure: &pmdeployv1alpha1.Exposure{HostnameTemplate: `"vw.example.com"`, Port: 6443},
				},
			}}

			cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, rootShardTemplate(), shardTemplate()).Build()
			reg := clusters.NewRegistry()
			engage(t, reg, "rootshard#customer-a--east")
			engage(t, reg, "frontproxy#customer-a--east")
			engage(t, reg, "shards-eu#customer-a--east")
			r := newReconciler(t, cl, reg, pm)

			rs, err := r.buildRootShardSpec(t.Context(), pm, pm.Spec.Topology.RootShard, "east")
			require.NoError(t, err)
			sh, err := r.buildShardSpec(t.Context(), pm, pm.Spec.Topology.ShardGroups[0], "east",
				names.RootShard("customer-a", "root", "east"))
			require.NoError(t, err)

			if !tc.want {
				assert.Nil(t, rs.KCPVirtualWorkspace)
				assert.Nil(t, sh.KCPVirtualWorkspace)
				return
			}
			require.NotNil(t, rs.KCPVirtualWorkspace)
			require.NotNil(t, sh.KCPVirtualWorkspace)
			assert.Equal(t, names.VirtualWorkspace("customer-a", "root", "east"), rs.KCPVirtualWorkspace.Name)
			assert.Equal(t, names.VirtualWorkspace("customer-a", "eu", "east"), sh.KCPVirtualWorkspace.Name)
		})
	}
}
