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
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"ocm.software/open-component-model/bindings/go/blob"
	descriptorruntime "ocm.software/open-component-model/bindings/go/descriptor/runtime"
	ocmruntime "ocm.software/open-component-model/bindings/go/runtime"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocm"
	pmocmmodule "go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

const agentManifest = `
apiVersion: v1
kind: Service
metadata:
  name: ${module}-agent
spec:
  ports:
    - port: 8443
`

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, pmdeployv1alpha1.AddToScheme(s))
	return s
}

// fakeCluster is a cluster.Cluster backed by a fake client.
type fakeCluster struct {
	cluster.Cluster
	client ctrlruntimeclient.Client
}

func (f *fakeCluster) GetClient() ctrlruntimeclient.Client { return f.client }

func engage(t *testing.T, reg *clusters.Registry, name string, cl ctrlruntimeclient.Client) {
	t.Helper()
	require.NoError(t, reg.Engage(context.Background(), multicluster.ClusterName(name), &fakeCluster{client: cl}))
}

type fakeCV struct{ contents map[string]string }

func (f *fakeCV) Descriptor() *descriptorruntime.Descriptor {
	desc := &descriptorruntime.Descriptor{}
	for name := range f.contents {
		desc.Component.Resources = append(desc.Component.Resources, descriptorruntime.Resource{
			ElementMeta: descriptorruntime.ElementMeta{
				ObjectMeta: descriptorruntime.ObjectMeta{Name: name, Version: "0.1.0"},
			},
		})
	}
	return desc
}

func (f *fakeCV) Resource(id ocmruntime.Identity) (*descriptorruntime.Resource, error) {
	for _, res := range f.Descriptor().Component.Resources {
		if res.ToIdentity().Equal(id) {
			return &res, nil
		}
	}
	return nil, ocm.ErrNotFound
}

func (f *fakeCV) ResourcesByType(string) []*descriptorruntime.Resource { return nil }

func (f *fakeCV) Download(_ context.Context, res *descriptorruntime.Resource) (blob.ReadOnlyBlob, error) {
	content, ok := f.contents[res.Name]
	if !ok {
		return nil, ocm.ErrNotFound
	}
	return stringBlob(content), nil
}

type stringBlob string

func (s stringBlob) ReadCloser() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(s))), nil
}

type fakeResolver struct{ cv ocm.ComponentVersion }

func (f *fakeResolver) Resolve(context.Context, ocm.OCMRepositorySpec, string, string) (ocm.ComponentVersion, error) {
	return f.cv, nil
}

func platformMesh(ready bool) *pmdeployv1alpha1.PlatformMesh {
	pm := &pmdeployv1alpha1.PlatformMesh{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm"},
		Spec: pmdeployv1alpha1.PlatformMeshSpec{
			OCM: pmdeployv1alpha1.OCMRepository{URL: "http://registry:5000"},
		},
	}
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&pm.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: status, Reason: "T", Message: "topology",
	})
	return pm
}

func testModule() *pmdeployv1alpha1.OCMModule {
	return &pmdeployv1alpha1.OCMModule{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", Namespace: "pm"},
		Spec: pmdeployv1alpha1.OCMModuleSpec{
			PlatformMeshRef: corev1.LocalObjectReference{Name: "customer-a"},
			Stage:           pmdeployv1alpha1.StagePostTopology,
			Component:       "github.com/platform-mesh/e2e-acme",
			Version:         "0.1.0",
			Components: []pmdeployv1alpha1.OCMModuleComponent{{
				Name:      "agent",
				Resource:  "agent-manifests",
				Placement: pmdeployv1alpha1.PlacementPerShard,
				Namespace: "acme-system",
			}},
		},
	}
}

func testResolver() ocm.Resolver {
	return &fakeResolver{cv: &fakeCV{contents: map[string]string{"agent-manifests": agentManifest}}}
}

func newTestReconciler(t *testing.T, objs []ctrlruntimeclient.Object, reg *clusters.Registry, mod *pmdeployv1alpha1.OCMModule) *reconciler {
	t.Helper()
	local := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build()
	return newReconciler(t, local, reg, testResolver(), mod)
}

func TestProcessDeploys(t *testing.T) {
	mod := testModule()
	pm := platformMesh(true)

	workload := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "shards-default#customer-a--s1", workload)

	r := newTestReconciler(t, []ctrlruntimeclient.Object{pm, mod}, reg, mod)
	require.NoError(t, r.run(t.Context()))

	// The rendered Service and the generated ConfigMap landed on the shard.
	svc := &corev1.Service{}
	require.NoError(t, workload.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "acme-system", Name: "acme-agent"}, svc))
	assert.Equal(t, "acme", svc.Labels[pmocmmodule.LabelModule])

	cm := &corev1.ConfigMap{}
	require.NoError(t, workload.Get(t.Context(), ctrlruntimeclient.ObjectKey{Namespace: "acme-system", Name: "acme-agent"}, cm))
	assert.Equal(t, "s1", cm.Data["CLUSTER"])

	require.Len(t, mod.Status.Components, 1)
	assert.Equal(t, "agent", mod.Status.Components[0].Name)
	require.Len(t, mod.Status.Components[0].Instances, 1)
	assert.Equal(t, "s1", mod.Status.Components[0].Instances[0].Cluster)
	assert.True(t, meta.IsStatusConditionTrue(mod.Status.Conditions, ConditionDeployed))
}

func TestProcessWaitsForTopology(t *testing.T) {
	mod := testModule()
	pm := platformMesh(false)

	reg := clusters.NewRegistry()
	r := newTestReconciler(t, []ctrlruntimeclient.Object{pm, mod}, reg, mod)

	err := r.run(t.Context())
	require.NoError(t, err)
	cond := meta.FindStatusCondition(mod.Status.Conditions, ConditionGated)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status, "a post-topology module waits for the topology")
	assert.Equal(t, "WaitingForTopology", cond.Reason)
	assert.Nil(t, meta.FindStatusCondition(mod.Status.Conditions, ConditionDeployed),
		"nothing may be deployed before the topology is up")
}

func TestProcessDependencies(t *testing.T) {
	ready := func(m *pmdeployv1alpha1.OCMModule) *pmdeployv1alpha1.OCMModule {
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
			Type: "Ready", Status: metav1.ConditionTrue, Reason: "R", Message: "ready",
		})
		return m
	}
	dep := func(name string, mutate func(*pmdeployv1alpha1.OCMModule)) *pmdeployv1alpha1.OCMModule {
		m := testModule()
		m.Name = name
		if mutate != nil {
			mutate(m)
		}
		return m
	}

	tests := []struct {
		name       string
		dependency *pmdeployv1alpha1.OCMModule
		stage      pmdeployv1alpha1.Stage
		wantReason string
	}{
		{
			name:       "missing dependency",
			dependency: nil,
			wantReason: "WaitingForDependency",
		},
		{
			name:       "dependency not ready",
			dependency: dep("base", nil),
			wantReason: "WaitingForDependency",
		},
		{
			name:       "dependency of another PlatformMesh",
			dependency: ready(dep("base", func(m *pmdeployv1alpha1.OCMModule) { m.Spec.PlatformMeshRef.Name = "other" })),
			wantReason: "WaitingForDependency",
		},
		{
			name:       "pre-topology depending on post-topology",
			dependency: ready(dep("base", nil)),
			stage:      pmdeployv1alpha1.StagePreTopology,
			wantReason: "WaitingForDependency",
		},
		{
			name:       "ready dependency",
			dependency: ready(dep("base", nil)),
			wantReason: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod := testModule()
			mod.Spec.DependsOn = []corev1.LocalObjectReference{{Name: "base"}}
			if tt.stage != "" {
				mod.Spec.Stage = tt.stage
			}

			objs := []ctrlruntimeclient.Object{platformMesh(true), mod}
			if tt.dependency != nil {
				objs = append(objs, tt.dependency)
			}
			workload := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
			reg := clusters.NewRegistry()
			engage(t, reg, "shards-default#customer-a--s1", workload)

			r := newTestReconciler(t, objs, reg, mod)
			err := r.run(t.Context())
			require.NoError(t, err)

			cond := meta.FindStatusCondition(mod.Status.Conditions, ConditionGated)
			require.NotNil(t, cond)
			if tt.wantReason == "" {
				assert.Equal(t, metav1.ConditionTrue, cond.Status)
				return
			}
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
		})
	}
}

// A cycle is a permanent misconfiguration, so it fails instead of requeueing
// forever with WaitingForDependency.
func TestProcessRejectsDependencyCycles(t *testing.T) {
	tests := []struct {
		name  string
		build func() []ctrlruntimeclient.Object
	}{
		{
			name: "self dependency",
			build: func() []ctrlruntimeclient.Object {
				mod := testModule()
				mod.Spec.DependsOn = []corev1.LocalObjectReference{{Name: mod.Name}}
				return []ctrlruntimeclient.Object{platformMesh(true), mod}
			},
		},
		{
			name: "two modules depending on each other",
			build: func() []ctrlruntimeclient.Object {
				a := testModule()
				a.Spec.DependsOn = []corev1.LocalObjectReference{{Name: "base"}}
				b := testModule()
				b.Name = "base"
				b.Spec.DependsOn = []corev1.LocalObjectReference{{Name: "acme"}}
				return []ctrlruntimeclient.Object{platformMesh(true), a, b}
			},
		},
		{
			name: "cycle through a third module",
			build: func() []ctrlruntimeclient.Object {
				a := testModule()
				a.Spec.DependsOn = []corev1.LocalObjectReference{{Name: "b"}}
				b := testModule()
				b.Name = "b"
				b.Spec.DependsOn = []corev1.LocalObjectReference{{Name: "c"}}
				c := testModule()
				c.Name = "c"
				c.Spec.DependsOn = []corev1.LocalObjectReference{{Name: "acme"}}
				return []ctrlruntimeclient.Object{platformMesh(true), a, b, c}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := tt.build()
			mod := objs[1].(*pmdeployv1alpha1.OCMModule)

			r := newTestReconciler(t, objs, clusters.NewRegistry(), mod)
			// A cycle is a permanent misconfiguration, so it is recorded
			// rather than retried forever.
			require.NoError(t, r.run(t.Context()))

			cond := meta.FindStatusCondition(mod.Status.Conditions, ConditionSpecValid)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, "Invalid", cond.Reason)
			assert.Contains(t, cond.Message, "dependency cycle")
		})
	}
}

// A dependency chain without a cycle must not be mistaken for one.
func TestProcessAcceptsDependencyChain(t *testing.T) {
	a := testModule()
	a.Spec.DependsOn = []corev1.LocalObjectReference{{Name: "b"}}
	b := testModule()
	b.Name = "b"
	b.Spec.DependsOn = []corev1.LocalObjectReference{{Name: "c"}}
	c := testModule()
	c.Name = "c"

	r := newTestReconciler(t, []ctrlruntimeclient.Object{platformMesh(true), a, b, c}, clusters.NewRegistry(), a)
	err := r.run(t.Context())
	require.NoError(t, err, "a chain is not a cycle")
}

func TestProcessPrunesStaleInstances(t *testing.T) {
	mod := testModule()
	pm := platformMesh(true)

	workload := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	reg := clusters.NewRegistry()
	engage(t, reg, "shards-default#customer-a--s1", workload)

	r := newTestReconciler(t, []ctrlruntimeclient.Object{pm, mod}, reg, mod)
	err := r.run(t.Context())
	require.NoError(t, err)

	svcKey := ctrlruntimeclient.ObjectKey{Namespace: "acme-system", Name: "acme-agent"}
	require.NoError(t, workload.Get(t.Context(), svcKey, &corev1.Service{}))

	// Dropping the component must remove what it left behind.
	mod.Spec.Components = nil
	require.NoError(t, r.run(t.Context()))

	cm := &unstructured.Unstructured{}
	cm.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMap"))
	err = workload.Get(t.Context(), svcKey, cm)
	assert.True(t, apierrors.IsNotFound(err), "the generated ConfigMap of a removed component is pruned")
}

func TestProcessMissingPlatformMesh(t *testing.T) {
	mod := testModule()
	reg := clusters.NewRegistry()
	r := newTestReconciler(t, []ctrlruntimeclient.Object{mod}, reg, mod)

	err := r.run(t.Context())
	require.Error(t, err)
}
