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

// Package virtualworkspace serves the tenancy APIs.
//
// One global singleton, not one instance per workspace or per APIExport: "which
// tenants am I in" is a cross-cutting projection that no single workspace
// can answer, so the per-workspace VW patterns do not fit.
//
// It lives in this operator rather than in a service of its own because it shares
// the things that must not drift: pkg/paths (the workspace layout), pkg/identity
// (the rbacIdentity mirror and the User name), and the API types. It runs as its
// own command and its own Deployment, though — it is stateless and scales
// horizontally with a watch per caller, while the controller is a leader-elected
// singleton.
package virtualworkspace

import (
	"context"
	"strings"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
)

// WildcardCluster is the cluster segment meaning "across the tenant fleet,
// filtered to the caller's memberships". It covers the one case a single-cluster
// scope cannot express: listing the tenants you belong to before selecting one.
const WildcardCluster = "*"

// clusterKey carries the resolved cluster scope to the storage layer.
type clusterKey struct{}

// ClusterFrom returns the cluster segment this request was scoped to, and whether
// the request came through this virtual workspace at all.
func ClusterFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(clusterKey{}).(string)
	return v, ok
}

// NewRootPathResolver accepts the URL shape:
//
//	<prefix>/clusters/{cluster}/apis/tenancy.platform-mesh.io/v1alpha1/...
//
// Deliberately the standard framework layout — a fixed root prefix, then the
// ordinary /clusters/{x} segment and kube API surface — so `prefixToStrip` is
// trivial and every kube client works unmodified. A bespoke URL shape here would
// mean a bespoke client everywhere.
//
// The prefix is configuration: two installs on one kcp must not both answer at
// /services/tenancy, or each one's singleton serves the other's callers.
func NewRootPathResolver(prefix string) framework.RootPathResolverFunc {
	prefix = "/" + strings.Trim(prefix, "/")

	return func(urlPath string, requestContext context.Context) (bool, string, context.Context) {
		if !strings.HasPrefix(urlPath, prefix) {
			return false, "", requestContext
		}

		rest := strings.TrimPrefix(urlPath, prefix)

		const clustersPrefix = "/clusters/"
		if !strings.HasPrefix(rest, clustersPrefix) {
			return false, "", requestContext
		}
		rest = strings.TrimPrefix(rest, clustersPrefix)

		// <cluster>[/api...]
		parts := strings.SplitN(rest, "/", 2)
		cluster := parts[0]
		if cluster == "" {
			return false, "", requestContext
		}

		ctx := context.WithValue(requestContext, clusterKey{}, cluster)

		// The wildcard is ours to interpret: the result set is a function of the
		// caller's memberships, not of a real logical cluster. Anything else names
		// one Tenant's tier and is passed through as a cluster.
		if cluster == WildcardCluster {
			ctx = genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Wildcard: true})
		} else {
			ctx = genericapirequest.WithCluster(ctx, genericapirequest.Cluster{
				Name: logicalcluster.Name(cluster),
			})
		}

		prefixToStrip := prefix + clustersPrefix + cluster
		return true, prefixToStrip, ctx
	}
}
