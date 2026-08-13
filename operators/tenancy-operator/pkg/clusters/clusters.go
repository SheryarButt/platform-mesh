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

// Package clusters resolves the logical cluster a reconcile is about, and the
// clients this operator uses to reach across the fleet.
//
// Every client here comes from an APIExport virtual workspace, and that is the
// point: no long-running part of this operator holds cluster-admin on kcp. A bug
// in a subroutine is bounded by the claim list on the export it went through, and
// an operator can read that list per workspace with `kubectl get apibindings`.
package clusters

import (
	"context"
	"fmt"

	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/kcp-dev/logicalcluster/v3"
)

// NameFrom returns the logical cluster the current reconcile is scoped to.
func NameFrom(ctx context.Context) (logicalcluster.Name, error) {
	name, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		return "", fmt.Errorf("no cluster in context: this reconcile is not cluster-scoped")
	}
	return logicalcluster.Name(name), nil
}

// ClientFor returns a client for the cluster the reconciled object lives in.
func ClientFor(ctx context.Context, mgr mcmanager.Manager, _ ctrlruntimeclient.Object) (ctrlruntimeclient.Client, error) {
	name, err := NameFrom(ctx)
	if err != nil {
		return nil, err
	}
	return ClientForCluster(ctx, mgr, name.String())
}

// ClientForCluster returns a client for one cluster, addressed by logical cluster
// name or — with a path-aware provider — by workspace path.
//
// A path only resolves when that workspace binds the export the manager watches.
// That is a feature: it is what makes "this operator can reach exactly these
// workspaces" a property of the bindings rather than of the credential.
func ClientForCluster(ctx context.Context, mgr mcmanager.Manager, cluster string) (ctrlruntimeclient.Client, error) {
	if mgr == nil {
		return nil, fmt.Errorf("no manager for cluster %q: the export it reads through is not wired", cluster)
	}
	c, err := mgr.GetCluster(ctx, multicluster.ClusterName(cluster))
	if err != nil {
		return nil, fmt.Errorf("getting cluster %q: %w", cluster, err)
	}
	return c.GetClient(), nil
}
