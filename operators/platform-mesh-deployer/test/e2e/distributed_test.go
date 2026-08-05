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

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.platform-mesh.io/platform-mesh-deployer/pkg/names"
	"go.platform-mesh.io/platform-mesh-deployer/test/e2e/suite"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	deployv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/deploy/v1alpha1"
)

// TestDistributedClusters deploys each component onto its own workload cluster.
func TestDistributedClusters(t *testing.T) {
	t.Parallel()
	env := suite.Start(t, 4)
	rs, sh1, sh2, fp := env.Workloads[0], env.Workloads[1], env.Workloads[2], env.Workloads[3]

	env.EngageWorkload(t, "customer-a", rs, "rootshard")
	env.EngageWorkload(t, "customer-a", sh1, "shards-default")
	env.EngageWorkload(t, "customer-a", sh2, "shards-default")
	env.EngageWorkload(t, "customer-a", fp, "frontproxy")
	env.CopyEtcdClientCert(t, rs)
	env.CopyEtcdClientCert(t, sh1)
	env.CopyEtcdClientCert(t, sh2)
	env.CopyEtcdClientCert(t, fp)

	createPlatformMesh(t, env.Config.Client, env.EtcdEndpoint())

	cases := []struct {
		kind    string
		name    string
		cluster *suite.Cluster
	}{
		{"CompiledRootShard", names.RootShard(suite.PlatformMeshName, "root", rs.NodeIP), rs},
		{"CompiledShard", names.Shard(suite.PlatformMeshName, "default", sh1.NodeIP), sh1},
		{"CompiledShard", names.Shard(suite.PlatformMeshName, "default", sh2.NodeIP), sh2},
		{"CompiledFrontProxy", names.FrontProxy(suite.PlatformMeshName, "fp", fp.NodeIP), fp},
	}
	for _, c := range cases {
		require.Eventuallyf(t, func() bool {
			return compiledExists(t, env.Config.Client, c.kind, c.name)
		}, 15*time.Minute, 5*time.Second, "config kcp-operator did not compile %s %q", c.kind, c.name)

		require.Eventuallyf(t, func() bool {
			return compiledExists(t, c.cluster.Client, c.kind, c.name)
		}, 5*time.Minute, 5*time.Second, "deployer did not copy %s %q to its workload cluster", c.kind, c.name)
	}

	env.VerifyKcp(t, rs, fp, 3)
}

func compiledExists(t *testing.T, cl ctrlruntimeclient.Client, kind, name string) bool {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(deployv1alpha1.SchemeGroupVersion.WithKind(kind))
	return cl.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: suite.ProviderNamespace, Name: name}, obj) == nil
}
