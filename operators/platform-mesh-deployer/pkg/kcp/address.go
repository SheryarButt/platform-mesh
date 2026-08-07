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
	"net"
	"strconv"

	operatorclient "github.com/kcp-dev/kcp-operator/pkg/client"
	operatorv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/operator/v1alpha1"
)

// Addresser resolves the kcp components kcp-operator's controllers talk to.
//
// Those controllers run in this process, on the cluster the deployer runs on,
// while the components they address run on the clusters it engaged. Service DNS
// does not cross that boundary: engaging a cluster yields a client for its API
// server, not a route onto its pod network. So each component is addressed by
// the external endpoint the deployer itself published for it, which is a
// subject alternative name of its serving certificate and therefore verifies.
type Addresser struct {
	// Dial reaches the published hosts. Nil dials them directly; the e2e runs
	// the deployer where they do not route and supplies one.
	Dial DialFunc
}

var _ operatorclient.Addresser = Addresser{}

// RootShard addresses a root shard by the URL it advertises to the other shards.
func (a Addresser) RootShard(rootShard *operatorv1alpha1.RootShard) operatorclient.Endpoint {
	return a.endpoint(rootShard.Spec.ShardBaseURL)
}

// RootShardProxy addresses the front proxy rather than the root shard's internal
// one, which has no external address. spec.external is the front proxy: the
// deployer points every root shard it renders at the one it renders alongside.
func (a Addresser) RootShardProxy(rootShard *operatorv1alpha1.RootShard) operatorclient.Endpoint {
	ext := rootShard.Spec.External
	if ext.Hostname == "" {
		return operatorclient.Endpoint{}
	}
	return a.endpoint("https://" + net.JoinHostPort(ext.Hostname, strconv.FormatUint(uint64(ext.Port), 10)))
}

// Shard addresses a shard by the URL it advertises, rendered from its group's
// exposure. A group without one is rejected when the PlatformMesh is validated.
func (a Addresser) Shard(shard *operatorv1alpha1.Shard) operatorclient.Endpoint {
	return a.endpoint(shard.Spec.ShardBaseURL)
}

func (a Addresser) endpoint(url string) operatorclient.Endpoint {
	return operatorclient.Endpoint{URL: url, Dial: a.Dial}
}
