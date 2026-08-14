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

package ocmmodulesetup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func watchScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, pmdeployv1alpha1.AddToScheme(s))
	return s
}

func setupFor(name, platformMesh string) *pmdeployv1alpha1.OCMModuleSetup {
	return &pmdeployv1alpha1.OCMModuleSetup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "pm"},
		Spec: pmdeployv1alpha1.OCMModuleSetupSpec{
			PlatformMeshRef: corev1.LocalObjectReference{Name: platformMesh},
		},
	}
}

func requestNames(reqs []reconcile.Request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Name)
	}
	return out
}

// This watch is the only wake-up for the root-structure wait, which no longer
// polls, so a setup that is not enqueued here never provisions.
func TestEnqueueSetupsOfPlatformMesh_enqueuesOnlyItsOwnSetups(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(watchScheme(t)).WithObjects(
		setupFor("acme", "customer-a"),
		setupFor("other", "customer-a"),
		setupFor("elsewhere", "customer-b"),
	).Build()

	got := enqueueSetupsOfPlatformMesh(cl)(t.Context(),
		&pmdeployv1alpha1.PlatformMesh{ObjectMeta: metav1.ObjectMeta{Name: "customer-a", Namespace: "pm"}})

	assert.ElementsMatch(t, []string{"acme", "other"}, requestNames(got))
}

// A failing client must not enqueue anything rather than panic.
func TestEnqueueSetupsOfPlatformMesh_isEmptyWhenTheListFails(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()

	assert.Empty(t, enqueueSetupsOfPlatformMesh(cl)(t.Context(),
		&pmdeployv1alpha1.PlatformMesh{ObjectMeta: metav1.ObjectMeta{Name: "customer-a"}}))
}
