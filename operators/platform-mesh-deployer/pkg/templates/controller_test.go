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

package templates

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconcilerFinalizer(t *testing.T) {
	newReconciler := func(t *testing.T, objs ...ctrlruntimeclient.Object) *Reconciler {
		t.Helper()
		return &Reconciler{
			client: fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build(),
			kind:   "RootShardTemplate",
			object: func() ctrlruntimeclient.Object { return &pmdeployv1alpha1.RootShardTemplate{} },
		}
	}
	key := reconcile.Request{NamespacedName: ctrlruntimeclient.ObjectKey{Namespace: "pm", Name: "root"}}
	template := func() *pmdeployv1alpha1.RootShardTemplate {
		return &pmdeployv1alpha1.RootShardTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Namespace: "pm"},
		}
	}

	t.Run("holds a referenced template", func(t *testing.T) {
		r := newReconciler(t, template(), templatedPlatformMesh("customer-a", "pm"))
		_, err := r.Reconcile(t.Context(), key)
		require.NoError(t, err)

		got := &pmdeployv1alpha1.RootShardTemplate{}
		require.NoError(t, r.client.Get(t.Context(), key.NamespacedName, got))
		assert.True(t, controllerutil.ContainsFinalizer(got, pmdeployv1alpha1.TemplateFinalizer))
	})

	t.Run("releases an unreferenced template", func(t *testing.T) {
		held := template()
		controllerutil.AddFinalizer(held, pmdeployv1alpha1.TemplateFinalizer)
		r := newReconciler(t, held)

		_, err := r.Reconcile(t.Context(), key)
		require.NoError(t, err)

		got := &pmdeployv1alpha1.RootShardTemplate{}
		require.NoError(t, r.client.Get(t.Context(), key.NamespacedName, got))
		assert.False(t, controllerutil.ContainsFinalizer(got, pmdeployv1alpha1.TemplateFinalizer))
	})

	t.Run("keeps holding while any installation still refers", func(t *testing.T) {
		held := template()
		controllerutil.AddFinalizer(held, pmdeployv1alpha1.TemplateFinalizer)
		gone := templatedPlatformMesh("customer-a", "pm")
		gone.Spec.Topology.RootShard.TemplateRef = nil
		r := newReconciler(t, held, gone, templatedPlatformMesh("customer-b", "pm"))

		_, err := r.Reconcile(t.Context(), key)
		require.NoError(t, err)

		got := &pmdeployv1alpha1.RootShardTemplate{}
		require.NoError(t, r.client.Get(t.Context(), key.NamespacedName, got))
		assert.True(t, controllerutil.ContainsFinalizer(got, pmdeployv1alpha1.TemplateFinalizer))
	})

	t.Run("ignores a deleted template", func(t *testing.T) {
		r := newReconciler(t)
		_, err := r.Reconcile(t.Context(), key)
		require.NoError(t, err)
	})
}
