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
	"fmt"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// platformMesh fetches the OCMModule's PlatformMesh.
func (r *reconciler) platformMesh(ctx context.Context, mod *pmdeployv1alpha1.OCMModule) (*pmdeployv1alpha1.PlatformMesh, error) {
	key := ctrlruntimeclient.ObjectKey{Namespace: mod.Namespace, Name: mod.Spec.PlatformMeshRef.Name}
	pm, err := r.opts.GetPlatformMesh(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("getting PlatformMesh %q: %w", key.Name, err)
	}
	return pm, nil
}

// topologyReady reports whether the PlatformMesh has finished bringing kcp up.
func topologyReady(pm *pmdeployv1alpha1.PlatformMesh) (bool, string) {
	cond := meta.FindStatusCondition(pm.Status.Conditions, "Ready")
	if cond == nil {
		return false, fmt.Sprintf("PlatformMesh %q has no Ready condition yet", pm.Name)
	}
	if cond.Status != metav1.ConditionTrue {
		return false, fmt.Sprintf("PlatformMesh %q is not ready: %s", pm.Name, cond.Message)
	}
	return true, ""
}

// dependenciesReady reports whether every OCMModule in spec.dependsOn is ready.
func (r *reconciler) dependenciesReady(ctx context.Context, mod *pmdeployv1alpha1.OCMModule) (bool, string) {
	for _, ref := range mod.Spec.DependsOn {
		if ref.Name == mod.Name {
			return false, fmt.Sprintf("module %q depends on itself", mod.Name)
		}
		key := ctrlruntimeclient.ObjectKey{Namespace: mod.Namespace, Name: ref.Name}
		dep, err := r.opts.GetModule(ctx, key)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, fmt.Sprintf("dependency %q does not exist", ref.Name)
			}
			return false, fmt.Sprintf("getting dependency %q: %v", ref.Name, err)
		}
		if dep.Spec.PlatformMeshRef.Name != mod.Spec.PlatformMeshRef.Name {
			return false, fmt.Sprintf("dependency %q belongs to another PlatformMesh", ref.Name)
		}
		if mod.Spec.Stage == pmdeployv1alpha1.StagePreTopology && dep.Spec.Stage == pmdeployv1alpha1.StagePostTopology {
			return false, fmt.Sprintf("pre-topology module %q cannot depend on post-topology module %q", mod.Name, ref.Name)
		}
		if !meta.IsStatusConditionTrue(dep.Status.Conditions, "Ready") {
			return false, fmt.Sprintf("dependency %q is not ready", ref.Name)
		}
	}
	return true, ""
}
