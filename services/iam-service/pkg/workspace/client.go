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

package workspace

import (
	"context"
	"fmt"

	"go.platform-mesh.io/golang-commons/logger"

	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/kcp-dev/logicalcluster/v3"
	mcclient "github.com/kcp-dev/multicluster-provider/client"
)

// ClientFactory creates a client for a specific kcp workspace
type ClientFactory interface {
	New(ctx context.Context, accountPath multicluster.ClusterName) (ctrlruntimeclient.Client, error)
}

// KCPClient implements ClientFactory for kcp workspaces using the multicluster manager
type KCPClient struct {
	mgr mcmanager.Manager
}

// NewClientFactory creates a new workspace client factory
func NewClientFactory(mgr mcmanager.Manager) *KCPClient {
	return &KCPClient{
		mgr: mgr,
	}
}

// New creates a new client for the specified workspace path
func (f *KCPClient) New(ctx context.Context, accountPath multicluster.ClusterName) (ctrlruntimeclient.Client, error) {
	log := logger.LoadLoggerFromContext(ctx)
	cluster, err := f.mgr.GetCluster(ctx, accountPath)
	if err != nil {
		log.Err(err).Msg(fmt.Sprintf("failed to get cluster: %s", accountPath))
		return nil, err
	}

	return cluster.GetClient(), nil
}

// ClusterClientFactory wraps the multicluster-provider ClusterClient to implement ClientFactory.
// Unlike KCPClient which relies on the multicluster manager's REST mapper,
// this client creates new clients with dynamic REST mappers for each workspace path,
// allowing it to work with any API group available in that workspace.
type ClusterClientFactory struct {
	client mcclient.ClusterClient
}

// NewClusterClientFactory creates a new ClientFactory using the multicluster-provider ClusterClient.
func NewClusterClientFactory(client mcclient.ClusterClient) *ClusterClientFactory {
	return &ClusterClientFactory{
		client: client,
	}
}

// New implements ClientFactory interface
func (c *ClusterClientFactory) New(_ context.Context, accountPath multicluster.ClusterName) (ctrlruntimeclient.Client, error) {
	path := logicalcluster.NewPath(string(accountPath))
	return c.client.Cluster(path), nil
}
