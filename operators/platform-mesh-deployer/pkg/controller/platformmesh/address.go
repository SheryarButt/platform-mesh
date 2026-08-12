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
	"fmt"
	"net"
	"strconv"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/celtemplate"
)

// endpointAddress is where one rendered component answers.
type endpointAddress struct {
	host string
	port uint32
}

// URL is the address as an https base URL.
func (a endpointAddress) URL() string {
	return "https://" + net.JoinHostPort(a.host, strconv.FormatUint(uint64(a.port), 10))
}

// resolveAddress returns the address a component advertises.
//
// An exposure names a host reachable from outside the cluster, so it wins. With
// none, the component is reachable only from inside the cluster it runs on, and
// the address is the Service kcp-operator gives it — which the deployer can name
// exactly, because it chose the object name that Service is derived from.
func resolveAddress(exposure *pmdeployv1alpha1.Exposure, celCtx celtemplate.Context, svc service, namespace, what string) (endpointAddress, error) {
	if exposure == nil {
		return endpointAddress{host: svc.dnsName(namespace), port: svc.port}, nil
	}
	host, err := celtemplate.Eval(exposure.HostnameTemplate, celCtx)
	if err != nil {
		return endpointAddress{}, fmt.Errorf("%s hostname: %w", what, err)
	}
	return endpointAddress{host: host, port: uint32(exposure.Port)}, nil
}

// service is the Service kcp-operator renders for a component, named after the
// admin CR the deployer created.
type service struct {
	adminName string
	suffix    string
	port      uint32
}

// dnsName is the in-cluster name of the Service.
func (s service) dnsName(namespace string) string {
	return s.adminName + s.suffix + "." + namespace + ".svc"
}

// The Service suffixes kcp-operator appends to a component's object name, and
// the ports those Services listen on. The front proxy's Service takes its port
// from spec.external.port, so an unexposed one is given the same port as the
// shards rather than a second number to remember.
const (
	rootShardServiceSuffix  = "-kcp"
	shardServiceSuffix      = "-shard-kcp"
	frontProxyServiceSuffix = "-front-proxy"

	// shardServicePort is the kcp-operator service port for root shards and shards.
	shardServicePort = 6443
)
