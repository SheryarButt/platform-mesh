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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func moduleAt(name string, stage pmdeployv1alpha1.Stage, ready bool) *pmdeployv1alpha1.OCMModule {
	mod := &pmdeployv1alpha1.OCMModule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "pm"},
		Spec: pmdeployv1alpha1.OCMModuleSpec{
			PlatformMeshRef: corev1.LocalObjectReference{Name: "customer-a"},
			Stage:           stage,
			Component:       "github.com/platform-mesh/" + name,
			Version:         "0.1.0",
		},
	}
	if ready {
		meta.SetStatusCondition(&mod.Status.Conditions, metav1.Condition{
			Type: "Ready", Status: metav1.ConditionTrue, Reason: "R", Message: "ready",
		})
	}
	return mod
}

func TestPreTopologyGate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		modules  []ctrlruntimeclient.Object
		wantPass bool
	}{
		{
			name:     "no modules at all",
			wantPass: true,
		},
		{
			name:     "pre-topology module ready",
			modules:  []ctrlruntimeclient.Object{moduleAt("etcd", pmdeployv1alpha1.StagePreTopology, true)},
			wantPass: true,
		},
		{
			name:     "pre-topology module not ready",
			modules:  []ctrlruntimeclient.Object{moduleAt("etcd", pmdeployv1alpha1.StagePreTopology, false)},
			wantPass: false,
		},
		{
			name: "one of several pre-topology modules not ready",
			modules: []ctrlruntimeclient.Object{
				moduleAt("etcd", pmdeployv1alpha1.StagePreTopology, true),
				moduleAt("gateway", pmdeployv1alpha1.StagePreTopology, false),
			},
			wantPass: false,
		},
		{
			name:     "post-topology module is not a gate",
			modules:  []ctrlruntimeclient.Object{moduleAt("acme", pmdeployv1alpha1.StagePostTopology, false)},
			wantPass: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := platformMesh()
			objs := append([]ctrlruntimeclient.Object{pm}, tt.modules...)
			cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build()

			cont, err := newReconciler(t, cl, nil, pm).awaitPreTopology(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tt.wantPass, cont)

			cond := meta.FindStatusCondition(pm.Status.Conditions, ConditionPreTopologyModulesReady)
			require.NotNil(t, cond)
			if tt.wantPass {
				assert.Equal(t, metav1.ConditionTrue, cond.Status)
			} else {
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Contains(t, cond.Message, "waiting for pre-topology modules")
			}
		})
	}
}

// A module of another PlatformMesh must not hold this one's topology back.
func TestPreTopologyIgnoresOtherPlatformMeshes(t *testing.T) {
	t.Parallel()
	pm := platformMesh()
	other := moduleAt("etcd", pmdeployv1alpha1.StagePreTopology, false)
	other.Spec.PlatformMeshRef.Name = "customer-b"

	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(pm, other).Build()

	cont, err := newReconciler(t, cl, nil, pm).awaitPreTopology(t.Context())
	require.NoError(t, err)
	assert.True(t, cont)
}
