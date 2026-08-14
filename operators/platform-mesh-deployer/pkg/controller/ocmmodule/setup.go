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
	pmocmmodule "go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// errSetupPending signals that the kcp side of a module is not ready yet.
var errSetupPending = fmt.Errorf("module setup not ready yet")

// ensureSetup writes the OCMModuleSetup handshake object and reports whether the
// provisioner has finished the kcp side. The workload is only deployed after
// that, so it can rely on its workspaces and their content existing.
func (r *reconciler) ensureSetup(ctx context.Context) error {
	mod := r.mod
	if len(mod.Spec.Workspaces) == 0 {
		return nil
	}

	// Content stays bound to the workspace it was declared for; flattening
	// it would apply a child's manifests into the parent.
	workspaces := make([]pmdeployv1alpha1.OCMModuleSetupWorkspace, 0, len(mod.Spec.Workspaces))
	for _, ws := range mod.Spec.Workspaces {
		workspaces = append(workspaces, pmdeployv1alpha1.OCMModuleSetupWorkspace{
			Path:    pmocmmodule.WorkspacePath(mod.Name, ws.Name),
			Content: ws.Content,
		})
	}

	setup := &pmdeployv1alpha1.OCMModuleSetup{
		ObjectMeta: metav1.ObjectMeta{Name: mod.Name, Namespace: mod.Namespace},
	}
	if err := r.opts.Apply(ctx, mod, setup, func() error {
		setup.Labels = pmocmmodule.OCMModuleSelector(mod, "")
		setup.Spec = pmdeployv1alpha1.OCMModuleSetupSpec{
			PlatformMeshRef: mod.Spec.PlatformMeshRef,
			OCMModuleRef:    corev1.LocalObjectReference{Name: mod.Name},
			ComponentDigest: mod.Status.ResolvedDigest,
			Workspaces:      workspaces,
		}
		return nil
	}); err != nil {
		return fmt.Errorf("reconciling OCMModuleSetup %q: %w", mod.Name, err)
	}

	if !meta.IsStatusConditionTrue(setup.Status.Conditions, "Ready") {
		return fmt.Errorf("%w: %s", errSetupPending, mod.Name)
	}

	r.endpoints = setup.Status.Endpoints
	return nil
}
