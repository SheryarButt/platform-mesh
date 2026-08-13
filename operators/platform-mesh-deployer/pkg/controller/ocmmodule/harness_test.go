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

package ocmmodule

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocm"
	pmocmmodule "go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	corev1 "k8s.io/api/core/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// configPlaneOptions implements the Options funcs against a fake config-plane
// client. The deploy steps are about what lands on the API server, so a fake
// client is the double rather than a stub per call.
func configPlaneOptions(cl ctrlruntimeclient.Client, reg *clusters.Registry, resolver ocm.Resolver) Options {
	return Options{
		GetModule: func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.OCMModule, error) {
			mod := &pmdeployv1alpha1.OCMModule{}
			return mod, cl.Get(ctx, key, mod)
		},
		ListModules: func(ctx context.Context, namespace string) ([]pmdeployv1alpha1.OCMModule, error) {
			list := &pmdeployv1alpha1.OCMModuleList{}
			if err := cl.List(ctx, list, ctrlruntimeclient.InNamespace(namespace)); err != nil {
				return nil, err
			}
			return list.Items, nil
		},
		GetPlatformMesh: func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			pm := &pmdeployv1alpha1.PlatformMesh{}
			return pm, cl.Get(ctx, key, pm)
		},
		GetSecret: func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*corev1.Secret, error) {
			secret := &corev1.Secret{}
			return secret, cl.Get(ctx, key, secret)
		},
		UpdateModule: func(ctx context.Context, mod *pmdeployv1alpha1.OCMModule) error {
			return cl.Update(ctx, mod)
		},
		PatchStatus: func(ctx context.Context, old, current *pmdeployv1alpha1.OCMModule) error {
			return cl.Status().Patch(ctx, current, ctrlruntimeclient.MergeFrom(old))
		},
		Apply:          ApplyWith(cl),
		ClustersFor:    reg.ClustersFor,
		AllClustersFor: reg.AllClustersFor,
		RegistryEvents: reg.Events,
		ResolveModule: func(ctx context.Context, mod *pmdeployv1alpha1.OCMModule, fallback *pmdeployv1alpha1.OCMRepository) (*pmocmmodule.Resolved, error) {
			return pmocmmodule.Resolve(ctx, resolver, mod, fallback)
		},
		FanOut: func(mod *pmdeployv1alpha1.OCMModule) ([]pmocmmodule.Instance, error) {
			return pmocmmodule.FanOut(reg, mod)
		},
		Requeue: defaultRequeue,
	}
}

// newReconciler builds a reconciler for one pass over mod, with the config
// plane backed by cl and the engaged clusters by reg.
func newReconciler(t *testing.T, cl ctrlruntimeclient.Client, reg *clusters.Registry, resolver ocm.Resolver, mod *pmdeployv1alpha1.OCMModule) *reconciler {
	t.Helper()
	opts := configPlaneOptions(cl, reg, resolver)
	require.NoError(t, opts.validate())
	return &reconciler{opts: opts, old: mod.DeepCopy(), mod: mod}
}
