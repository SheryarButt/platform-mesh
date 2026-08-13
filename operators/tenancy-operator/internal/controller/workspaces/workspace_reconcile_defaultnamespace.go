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

package workspaces

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// DefaultNamespace is the namespace kcp would have created for us.
const DefaultNamespace = "default"

// defaultNamespace creates `default` in a tenant workspace.
//
// It exists because the tenant WorkspaceType does not `extend: root:universal`,
// and universal is what makes kcp create this namespace. Not extending it is
// deliberate — universal would pull tenancy.kcp.io and topology.kcp.io into the
// TENANT's own API surface, letting a tenant spawn child workspaces and inspect
// cluster topology. This step is the price of that, paid once per workspace.
type defaultNamespace struct{}

var _ reconciler = &defaultNamespace{}

func (r *defaultNamespace) Name() string { return "DefaultNamespace" }

func (r *defaultNamespace) Reconcile(ctx context.Context, child ctrlruntimeclient.Client, ws *kcptenancyv1alpha1.Workspace) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Create rather than CreateOrUpdate: this step owns the namespace's existence
	// and nothing about its content, so there is no desired state to reconcile
	// into an existing one. AlreadyExists IS the success case on every reconcile
	// after the first.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultNamespace}}
	if err := child.Create(ctx, ns); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("creating the %s namespace in workspace %s: %w", DefaultNamespace, ws.Spec.Cluster, err)
	}

	log.Info("created the default namespace", "workspace", ws.Name, "cluster", ws.Spec.Cluster)
	return ctrl.Result{}, nil
}
