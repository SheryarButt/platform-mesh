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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/names"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func exposeString(host string, port int32) *pmdeployv1alpha1.Exposure {
	return &pmdeployv1alpha1.Exposure{HostnameTemplate: host, Port: port}
}

func TestExposureCreatesRoutes(t *testing.T) {
	t.Parallel()
	s := scheme(t)
	pm := &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm"},
		Spec: pmdeployv1alpha1.PlatformMeshSpec{
			Topology: pmdeployv1alpha1.Topology{
				RootShard: pmdeployv1alpha1.RootShard{
					Name:     "root",
					Exposure: exposeString(`"root." + cluster + ".sslip.io"`, 31443),
				},
				FrontProxy: pmdeployv1alpha1.FrontProxy{
					Name:     "fp",
					Exposure: exposeString(`"fp." + cluster + ".sslip.io"`, 31443),
				},
				ShardGroups: []pmdeployv1alpha1.ShardGroup{{
					Name:     "eu",
					Exposure: exposeString(`component + "." + cluster + ".sslip.io"`, 31443),
				}},
			},
			Ingress: []pmdeployv1alpha1.IngressStack{{
				Name: "gw",
				Type: pmdeployv1alpha1.IngressTypeGatewayAPI,
				GatewayAPI: &pmdeployv1alpha1.GatewayAPIValues{
					GatewayName:      "eg",
					GatewayNamespace: "envoy-gateway-system",
					SectionName:      "passthrough",
				},
			}},
		},
	}

	reg := clusters.NewRegistry()
	rootCl := fake.NewClientBuilder().WithScheme(s).Build()
	shardCl := fake.NewClientBuilder().WithScheme(s).Build()
	fpCl := fake.NewClientBuilder().WithScheme(s).Build()
	engageWithClient(t, reg, "rootshard#customer-a--east", rootCl)
	engageWithClient(t, reg, "shards-eu#customer-a--west", shardCl)
	engageWithClient(t, reg, "frontproxy#customer-a--fpc", fpCl)

	r := newReconciler(t, newClient(t), reg, pm)
	_, err := r.reconcileExposure(context.Background())
	require.NoError(t, err)

	cond := meta.FindStatusCondition(pm.Status.Conditions, ConditionExposureReady)
	require.NotNil(t, cond, "exposing the topology has to say so on the status")
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "Exposed", cond.Reason)
	assert.Equal(t, pm.Generation, cond.ObservedGeneration)

	root := getRoute(t, rootCl, "pm", names.RootShard("customer-a", "root", "east")+"-gw")
	assert.Equal(t, []gwapiv1alpha2.Hostname{"root.east.sslip.io"}, root.Spec.Hostnames)
	assert.Equal(t, names.RootShard("customer-a", "root", "east")+"-kcp", string(root.Spec.Rules[0].BackendRefs[0].Name))
	assert.Equal(t, gwapiv1alpha2.PortNumber(6443), *root.Spec.Rules[0].BackendRefs[0].Port)

	shard := getRoute(t, shardCl, "pm", names.Shard("customer-a", "eu", "west")+"-gw")
	assert.Equal(t, []gwapiv1alpha2.Hostname{"shards-eu.west.sslip.io"}, shard.Spec.Hostnames)
	assert.Equal(t, names.Shard("customer-a", "eu", "west")+"-shard-kcp", string(shard.Spec.Rules[0].BackendRefs[0].Name))
	assert.Equal(t, gwapiv1alpha2.PortNumber(6443), *shard.Spec.Rules[0].BackendRefs[0].Port)

	fp := getRoute(t, fpCl, "pm", names.FrontProxy("customer-a", "fp", "fpc")+"-gw")
	assert.Equal(t, []gwapiv1alpha2.Hostname{"fp.fpc.sslip.io"}, fp.Spec.Hostnames)
	assert.Equal(t, names.FrontProxy("customer-a", "fp", "fpc")+"-front-proxy", string(fp.Spec.Rules[0].BackendRefs[0].Name))
	assert.Equal(t, gwapiv1alpha2.PortNumber(31443), *fp.Spec.Rules[0].BackendRefs[0].Port)
	require.Len(t, fp.Spec.ParentRefs, 1)
	assert.Equal(t, "eg", string(fp.Spec.ParentRefs[0].Name))
	assert.Equal(t, "envoy-gateway-system", string(*fp.Spec.ParentRefs[0].Namespace))
	assert.Equal(t, "passthrough", string(*fp.Spec.ParentRefs[0].SectionName))
	assert.Equal(t, components.FrontProxy, fp.Labels[components.LabelComponent])
}

func TestExposureNoStacksNoRoutes(t *testing.T) {
	t.Parallel()
	s := scheme(t)
	pm := &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm"},
		Spec: pmdeployv1alpha1.PlatformMeshSpec{
			Topology: pmdeployv1alpha1.Topology{
				RootShard:  pmdeployv1alpha1.RootShard{Name: "root", Exposure: exposeString(`"x"`, 31443)},
				FrontProxy: pmdeployv1alpha1.FrontProxy{Name: "fp", Exposure: exposeString(`"x"`, 31443)},
			},
		},
	}
	reg := clusters.NewRegistry()
	rootCl := fake.NewClientBuilder().WithScheme(s).Build()
	engageWithClient(t, reg, "rootshard#customer-a--east", rootCl)

	_, err := newReconciler(t, newClient(t), reg, pm).reconcileExposure(context.Background())
	require.NoError(t, err)

	list := &gwapiv1alpha2.TLSRouteList{}
	require.NoError(t, rootCl.List(context.Background(), list))
	assert.Empty(t, list.Items)
}

func TestExposureTeardownStale(t *testing.T) {
	t.Parallel()
	s := scheme(t)
	stale := &gwapiv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fp-gone-gw",
			Namespace: "pm",
			Labels: map[string]string{
				components.LabelPlatformMesh: "customer-a",
				components.LabelComponent:    components.FrontProxy,
				components.LabelCluster:      "gone",
			},
		},
	}
	fpCl := fake.NewClientBuilder().WithScheme(s).WithObjects(stale).Build()

	pm := &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm"},
		Spec: pmdeployv1alpha1.PlatformMeshSpec{
			Topology: pmdeployv1alpha1.Topology{
				RootShard:  pmdeployv1alpha1.RootShard{Name: "root", Exposure: exposeString(`"fp." + cluster + ".sslip.io"`, 31443)},
				FrontProxy: pmdeployv1alpha1.FrontProxy{Name: "fp", Exposure: exposeString(`"fp." + cluster + ".sslip.io"`, 31443)},
			},
			Ingress: []pmdeployv1alpha1.IngressStack{{
				Name:       "gw",
				Type:       pmdeployv1alpha1.IngressTypeGatewayAPI,
				GatewayAPI: &pmdeployv1alpha1.GatewayAPIValues{GatewayName: "eg", GatewayNamespace: "envoy-gateway-system"},
			}},
		},
	}
	reg := clusters.NewRegistry()
	engageWithClient(t, reg, "frontproxy#customer-a--east", fpCl)

	_, err := newReconciler(t, newClient(t), reg, pm).reconcileExposure(context.Background())
	require.NoError(t, err)

	// Stale route for the disengaged "gone" cluster removed, current one present.
	list := &gwapiv1alpha2.TLSRouteList{}
	require.NoError(t, fpCl.List(context.Background(), list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, names.FrontProxy("customer-a", "fp", "east")+"-gw", list.Items[0].Name)
	assert.Equal(t, "east", list.Items[0].Labels[components.LabelCluster])
}

func TestExposureTeardownKeepsModuleRoutes(t *testing.T) {
	t.Parallel()
	s := scheme(t)
	owned := &gwapiv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "acme-ui",
			Namespace: "pm",
			Labels: map[string]string{
				ocmmodule.LabelPlatformMesh: "customer-a",
				ocmmodule.LabelModule:       "acme",
				ocmmodule.LabelComponent:    "ui",
				ocmmodule.LabelCluster:      "east",
			},
		},
	}
	fpCl := fake.NewClientBuilder().WithScheme(s).WithObjects(owned).Build()

	pm := &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm"},
		Spec: pmdeployv1alpha1.PlatformMeshSpec{
			Topology: pmdeployv1alpha1.Topology{
				RootShard:  pmdeployv1alpha1.RootShard{Name: "root", Exposure: exposeString(`"fp." + cluster + ".sslip.io"`, 31443)},
				FrontProxy: pmdeployv1alpha1.FrontProxy{Name: "fp", Exposure: exposeString(`"fp." + cluster + ".sslip.io"`, 31443)},
			},
			Ingress: []pmdeployv1alpha1.IngressStack{{
				Name:       "gw",
				Type:       pmdeployv1alpha1.IngressTypeGatewayAPI,
				GatewayAPI: &pmdeployv1alpha1.GatewayAPIValues{GatewayName: "eg", GatewayNamespace: "envoy-gateway-system"},
			}},
		},
	}
	reg := clusters.NewRegistry()
	engageWithClient(t, reg, "frontproxy#customer-a--east", fpCl)

	_, err := newReconciler(t, newClient(t), reg, pm).reconcileExposure(context.Background())
	require.NoError(t, err)

	route := &gwapiv1alpha2.TLSRoute{}
	assert.NoError(t, fpCl.Get(context.Background(), ctrlruntimeclient.ObjectKeyFromObject(owned), route))
}

func getRoute(t *testing.T, cl ctrlruntimeclient.Client, ns, name string) *gwapiv1alpha2.TLSRoute {
	t.Helper()
	route := &gwapiv1alpha2.TLSRoute{}
	require.NoError(t, cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Namespace: ns, Name: name}, route))
	return route
}
