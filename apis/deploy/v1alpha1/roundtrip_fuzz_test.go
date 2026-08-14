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

package v1alpha1

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/api/equality"
)

func fuzzRoundTrip[T any](t *testing.T, data []byte, obj *T, obj2 *T) {
	t.Helper()
	if err := json.Unmarshal(data, obj); err != nil {
		return
	}
	roundtripped, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	if err := json.Unmarshal(roundtripped, obj2); err != nil {
		t.Fatalf("failed to unmarshal roundtripped data: %v", err)
	}
	if !equality.Semantic.DeepEqual(obj, obj2) {
		t.Errorf("roundtrip mismatch for %T", obj)
	}
}

func FuzzPlatformMeshRoundTrip(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"deploy.platform-mesh.io/v1alpha1","kind":"PlatformMesh","metadata":{"name":"pm"},"spec":{"version":"1.0.0","ocm":{"url":"ghcr.io/platform-mesh","component":"github.com/platform-mesh/platform-mesh"},"topology":{"rootShard":{"name":"root","templateRef":{"name":"root"},"virtualWorkspaces":{"mode":"Embedded","templateRef":{"name":"vw","namespace":"shared"},"exposure":{"hostnameTemplate":"vw.{{ .Cluster }}.example.com","port":443}}},"frontProxy":{"name":"public","templateRef":{"name":"fp"},"exposure":{"hostnameTemplate":"kcp.example.com","port":443}},"cacheServer":{"name":"global","templateRef":{"name":"cache"}}},"ingress":[{"name":"default","type":"gatewayapi"}]}}`))
	f.Add([]byte(`{"spec":{"version":"1.0.0","ocm":{"url":"ghcr.io/platform-mesh"},"topology":{"rootShard":{"name":"root","virtualWorkspaces":{"exposure":{"hostnameTemplate":"vw.example.com","port":6443}}}}}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzRoundTrip(t, data, &PlatformMesh{}, &PlatformMesh{})
	})
}

func FuzzModuleRoundTrip(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"deploy.platform-mesh.io/v1alpha1","kind":"OCMModule","metadata":{"name":"acme"},"spec":{"platformMeshRef":{"name":"pm"},"stage":"post-topology","dependsOn":[{"name":"etcd-druid"}],"component":"github.com/platform-mesh/e2e-acme","version":"0.1.0","values":{"replicas":2},"workspaces":[{"name":"","content":[{"name":"apiexports"}]},{"name":"validation","content":[{"name":"validation-schemas"}]}],"kubeconfigs":[{"name":"kcp","target":"front-proxy"},{"name":"shardadmin","target":"shard","workspace":"validation"}],"components":[{"name":"controller","resource":"controller-manifests","placement":"root-shard","namespace":"acme-system","kubeconfigs":["kcp"]},{"name":"gateway","resource":"gateway-manifests","placement":"per-front-proxy","namespace":"acme-system","mapping":{"path":"/services/acme/","service":"acme-gateway","port":8443}}]},"status":{"resolvedDigest":"sha256:abc","workspaces":[{"name":"","path":"root:modules:acme","ready":true}],"components":[{"name":"controller","placement":"root-shard","instances":[{"cluster":"c1","namespace":"acme-system","configMap":"acme-controller","secrets":["acme-kcp"],"ready":true}]}]}}`))
	f.Add([]byte(`{"spec":{"platformMeshRef":{"name":"pm"},"stage":"pre-topology","component":"github.com/platform-mesh/etcd-druid","version":"0.1.0","ocm":{"url":"ghcr.io/other","secretRef":{"name":"creds"}},"components":[{"name":"druid","resource":"manifests","placement":"all-clusters","namespace":"etcd-system"}]}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzRoundTrip(t, data, &OCMModule{}, &OCMModule{})
	})
}

func FuzzModuleSetupRoundTrip(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"deploy.platform-mesh.io/v1alpha1","kind":"OCMModuleSetup","metadata":{"name":"account-operator-setup"},"spec":{"platformMeshRef":{"name":"pm"},"moduleRef":{"name":"account-operator"},"componentDigest":"sha256:abc","workspaces":[{"path":"root:modules:account-operator","content":[{"name":"kcp-manifests","version":"0.1.0"}]},{"path":"root:modules:account-operator:validation"}],"kubeconfigRefs":[{"name":"ws-kubeconfig"}]},"status":{"endpoints":{"api":"https://kcp.example.com"}}}`))
	f.Add([]byte(`{"spec":{"platformMeshRef":{"name":"pm"},"moduleRef":{"name":"account-operator"}}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzRoundTrip(t, data, &OCMModuleSetup{}, &OCMModuleSetup{})
	})
}

func FuzzRootShardTemplateRoundTrip(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"deploy.platform-mesh.io/v1alpha1","kind":"RootShardTemplate","metadata":{"name":"root"},"spec":{"replicas":2,"etcd":{"endpoints":["https://etcd:2379"],"prefix":"/pm"}}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzRoundTrip(t, data, &RootShardTemplate{}, &RootShardTemplate{})
	})
}

func FuzzShardTemplateRoundTrip(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"deploy.platform-mesh.io/v1alpha1","kind":"ShardTemplate","metadata":{"name":"default"},"spec":{"replicas":3,"etcd":{"endpoints":["https://etcd:2379"]}}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzRoundTrip(t, data, &ShardTemplate{}, &ShardTemplate{})
	})
}

func FuzzFrontProxyTemplateRoundTrip(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"deploy.platform-mesh.io/v1alpha1","kind":"FrontProxyTemplate","metadata":{"name":"fp"},"spec":{"replicas":3}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzRoundTrip(t, data, &FrontProxyTemplate{}, &FrontProxyTemplate{})
	})
}

func FuzzCacheServerTemplateRoundTrip(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"deploy.platform-mesh.io/v1alpha1","kind":"CacheServerTemplate","metadata":{"name":"global"},"spec":{"replicas":1,"etcd":{"endpoints":["https://etcd:2379"]}}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzRoundTrip(t, data, &CacheServerTemplate{}, &CacheServerTemplate{})
	})
}

func FuzzVirtualWorkspaceTemplateRoundTrip(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"deploy.platform-mesh.io/v1alpha1","kind":"VirtualWorkspaceTemplate","metadata":{"name":"vw"},"spec":{"replicas":1}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzRoundTrip(t, data, &VirtualWorkspaceTemplate{}, &VirtualWorkspaceTemplate{})
	})
}
