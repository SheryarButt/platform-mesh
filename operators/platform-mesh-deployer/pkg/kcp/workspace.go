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

package kcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// ErrWorkspacePending signals that a workspace exists but is not usable yet.
var ErrWorkspacePending = errors.New("workspace not ready yet")

// WorkspaceType is the type new workspaces are created with. "universal" has
// no initializers, so it schedules without any further setup.
const WorkspaceType = "universal"

// EnsureWorkspace creates a workspace below parent if it is missing and
// reports ErrWorkspacePending until it is ready.
func EnsureWorkspace(ctx context.Context, parent ctrlruntimeclient.Client, name string) error {
	ws := &kcptenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := parent.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name}, ws); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("reading workspace %q: %w", name, err)
		}
		ws = &kcptenancyv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: kcptenancyv1alpha1.WorkspaceSpec{
				Type: &kcptenancyv1alpha1.WorkspaceTypeReference{Name: kcptenancyv1alpha1.WorkspaceTypeName(WorkspaceType)},
			},
		}
		if err := parent.Create(ctx, ws); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating workspace %q: %w", name, err)
		}
		return fmt.Errorf("%w: %s", ErrWorkspacePending, name)
	}

	if ws.Status.Phase != kcpcorev1alpha1.LogicalClusterPhaseReady {
		return fmt.Errorf("%w: %s is %s", ErrWorkspacePending, name, ws.Status.Phase)
	}
	return nil
}

// EnsurePath creates every workspace along an absolute path, starting below
// the given root, and returns a client for the leaf.
func (a *Access) EnsurePath(ctx context.Context, base *rest.Config, path string) (ctrlruntimeclient.Client, error) {
	segments := strings.Split(path, ":")
	if len(segments) == 0 || segments[0] != "root" {
		return nil, fmt.Errorf("workspace path %q must start at root", path)
	}

	current := "root"
	client, err := a.ClientFor(base, current)
	if err != nil {
		return nil, err
	}
	for _, segment := range segments[1:] {
		if err := EnsureWorkspace(ctx, client, segment); err != nil {
			return nil, err
		}
		current += ":" + segment
		if client, err = a.ClientFor(base, current); err != nil {
			return nil, err
		}
	}
	return client, nil
}

// DeletePath removes the workspace at an absolute path, resolving its parent
// to delete it from.
func (a *Access) DeletePath(ctx context.Context, base *rest.Config, path string) error {
	parent, name, ok := splitPath(path)
	if !ok {
		return fmt.Errorf("workspace path %q has no parent", path)
	}
	client, err := a.ClientFor(base, parent)
	if err != nil {
		return err
	}
	return DeleteWorkspace(ctx, client, name)
}

// DeleteWorkspace removes a workspace below parent. It reports
// ErrWorkspacePending while the workspace is still terminating, and treats a
// missing workspace as already done.
func DeleteWorkspace(ctx context.Context, parent ctrlruntimeclient.Client, name string) error {
	ws := &kcptenancyv1alpha1.Workspace{}
	if err := parent.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name}, ws); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return nil
		}
		return fmt.Errorf("reading workspace %q: %w", name, err)
	}

	if ws.DeletionTimestamp == nil {
		if err := parent.Delete(ctx, ws); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting workspace %q: %w", name, err)
		}
	}
	return fmt.Errorf("%w: %s is terminating", ErrWorkspacePending, name)
}

// splitPath splits an absolute workspace path into its parent and leaf.
func splitPath(path string) (string, string, bool) {
	idx := strings.LastIndex(path, ":")
	if idx < 0 {
		return "", "", false
	}
	return path[:idx], path[idx+1:], true
}
