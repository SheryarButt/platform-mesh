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
	"encoding/json"
	"fmt"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/celtemplate"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"

	"k8s.io/apimachinery/pkg/api/meta"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/operator/v1alpha1"
)

// reconcileTopology renders the kcp-operator admin CRs. Every unmet
// precondition here is an error, not a wait: a missing engaged cluster means
// the installation is misconfigured, not that it is still starting.
func (r *reconciler) reconcileTopology(ctx context.Context) (bool, error) {
	pm := r.pm
	if err := r.reconcileRootShard(ctx, pm); err != nil {
		return r.topologyFailed(err)
	}
	if err := r.reconcileShards(ctx, pm); err != nil {
		return r.topologyFailed(err)
	}
	if err := r.reconcileFrontProxy(ctx, pm); err != nil {
		return r.topologyFailed(err)
	}
	if err := r.reconcileCacheServer(ctx, pm); err != nil {
		return r.topologyFailed(err)
	}
	if err := r.reconcileVirtualWorkspaces(ctx, pm); err != nil {
		return r.topologyFailed(err)
	}
	meta.SetStatusCondition(&pm.Status.Conditions, topologyReady(pm.Generation))
	return true, nil
}

func (r *reconciler) topologyFailed(err error) (bool, error) {
	meta.SetStatusCondition(&r.pm.Status.Conditions, topologyFailed(r.pm.Generation, err))
	return false, err
}

// resolveTemplate converts the referenced template CR's spec into out.
// A nil ref leaves out at its zero value.
func (r *reconciler) resolveTemplate(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh, ref *pmdeployv1alpha1.TemplateReference, tpl ctrlruntimeclient.Object, spec func() any, out any) error {
	if ref == nil {
		return nil
	}
	namespace := ref.Namespace
	if namespace == "" {
		namespace = pm.Namespace
	}
	key := ctrlruntimeclient.ObjectKey{Namespace: namespace, Name: ref.Name}
	if err := r.opts.GetTemplate(ctx, key, tpl); err != nil {
		return fmt.Errorf("template %s: %w", key, err)
	}
	// Templates carry the same spec type as their target, so the deployer-owned
	// fields it is about to overwrite are whatever the author put there.
	data, err := json.Marshal(spec())
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// resolveEtcd expands the CEL expressions in the owned etcd endpoints and prefix.
func resolveEtcd(etcd *operatorv1alpha1.EtcdConfig, celCtx celtemplate.Context, what string) error {
	for i, endpoint := range etcd.Endpoints {
		resolved, err := celtemplate.Eval(endpoint, celCtx)
		if err != nil {
			return fmt.Errorf("%s etcd endpoint: %w", what, err)
		}
		etcd.Endpoints[i] = resolved
	}
	if etcd.Prefix != "" {
		resolved, err := celtemplate.Eval(etcd.Prefix, celCtx)
		if err != nil {
			return fmt.Errorf("%s etcd prefix: %w", what, err)
		}
		etcd.Prefix = resolved
	}
	return nil
}

func labels(platformMesh, component, clusterID string) map[string]string {
	return map[string]string{
		components.LabelPlatformMesh: platformMesh,
		components.LabelComponent:    component,
		components.LabelCluster:      clusterID,
	}
}
