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

	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	operatorv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/operator/v1alpha1"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, pmdeployv1alpha1.AddToScheme(s))
	require.NoError(t, operatorv1alpha1.AddToScheme(s))
	require.NoError(t, gwapiv1alpha2.Install(s))
	return s
}

// fakeCluster satisfies cluster.Cluster by only implementing GetClient, which
// is all the exposure step calls.
type fakeCluster struct {
	cluster.Cluster
	client ctrlruntimeclient.Client
}

func (f *fakeCluster) GetClient() ctrlruntimeclient.Client { return f.client }

func engage(t *testing.T, r *clusters.Registry, name string) {
	t.Helper()
	require.NoError(t, r.Engage(context.Background(), multicluster.ClusterName(name), nil))
}

func engageWithClient(t *testing.T, r *clusters.Registry, name string, cl ctrlruntimeclient.Client) {
	t.Helper()
	require.NoError(t, r.Engage(context.Background(), multicluster.ClusterName(name), &fakeCluster{client: cl}))
}

// configPlaneOptions implements the Options funcs against a fake config-plane
// client. The rendering steps are about what lands on the API server, so a
// fake client is the double rather than a stub per call.
func configPlaneOptions(t *testing.T, cl ctrlruntimeclient.Client, reg *clusters.Registry) Options {
	t.Helper()
	return Options{
		GetPlatformMesh: func(ctx context.Context, key ctrlruntimeclient.ObjectKey) (*pmdeployv1alpha1.PlatformMesh, error) {
			pm := &pmdeployv1alpha1.PlatformMesh{}
			return pm, cl.Get(ctx, key, pm)
		},
		PatchStatus: func(ctx context.Context, old, current *pmdeployv1alpha1.PlatformMesh) error {
			return cl.Status().Patch(ctx, current, ctrlruntimeclient.MergeFrom(old))
		},
		ListModules: func(ctx context.Context, namespace string) ([]pmdeployv1alpha1.OCMModule, error) {
			list := &pmdeployv1alpha1.OCMModuleList{}
			if err := cl.List(ctx, list, ctrlruntimeclient.InNamespace(namespace)); err != nil {
				return nil, err
			}
			return list.Items, nil
		},
		GetTemplate: func(ctx context.Context, key ctrlruntimeclient.ObjectKey, into ctrlruntimeclient.Object) error {
			return cl.Get(ctx, key, into)
		},
		Apply:          ApplyWith(cl),
		Teardown:       TeardownWith(cl),
		ClustersFor:    reg.ClustersFor,
		RegistryEvents: reg.Events,
		Requeue:        defaultRequeue,
	}
}

// newReconciler builds a reconciler for one pass over pm, with the config
// plane backed by cl and the engaged clusters by reg.
func newReconciler(t *testing.T, cl ctrlruntimeclient.Client, reg *clusters.Registry, pm *pmdeployv1alpha1.PlatformMesh) *reconciler {
	t.Helper()
	return &reconciler{
		opts: configPlaneOptions(t, cl, reg),
		old:  pm.DeepCopy(),
		pm:   pm,
	}
}

func newClient(t *testing.T, objs ...ctrlruntimeclient.Object) ctrlruntimeclient.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build()
}

func notFoundPlatformMesh() error {
	return apierrors.NewNotFound(schema.GroupResource{Resource: "platformmeshes"}, "customer-a")
}

func reconcileRequest() reconcile.Request {
	return reconcile.Request{NamespacedName: ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: "customer-a"}}
}
