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

package transfer

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	metadataKey = "metadata"
	statusKey   = "status"
)

// Ref locates an object on a cluster.
type Ref struct {
	Client ctrlruntimeclient.Client
	Name   types.NamespacedName
}

// StripClusterMetadata returns a deep copy of the provided Unstructured with cluster-specific fields removed so the object is safe to write in another cluster.
func StripClusterMetadata(obj *unstructured.Unstructured) *unstructured.Unstructured {
	c := obj.DeepCopy()
	delete(c.Object, statusKey)
	if m, ok := c.Object[metadataKey].(map[string]any); ok {
		delete(m, "resourceVersion")
		delete(m, "uid")
		delete(m, "creationTimestamp")
		delete(m, "managedFields")
		delete(m, "generation")
		delete(m, "ownerReferences")
		delete(m, "finalizers")
		delete(m, "annotations")
		delete(m, "labels")
	}
	return c
}

// EqualObjects returns true if the two unstructured objects are equal after removing cluster-specific metadata and status.
func EqualObjects(a, b *unstructured.Unstructured) bool {
	return cmp.Equal(
		StripClusterMetadata(a).Object,
		StripClusterMetadata(b).Object,
		cmpopts.EquateEmpty(),
	)
}

// Spec copies an object from one cluster to another without touching the status, creating it if it is not there yet.
func Spec(ctx context.Context, gvk schema.GroupVersionKind, from, to Ref) error {
	source := &unstructured.Unstructured{}
	source.SetGroupVersionKind(gvk)
	if err := from.Client.Get(ctx, from.Name, source); err != nil {
		return fmt.Errorf("get %s: %w", from.Name, err)
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(gvk)
	if err := to.Client.Get(ctx, to.Name, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get %s: %w", to.Name, err)
		}

		target := StripClusterMetadata(source)
		target.SetName(to.Name.Name)
		target.SetNamespace(to.Name.Namespace)
		if err := to.Client.Create(ctx, target); err != nil {
			return fmt.Errorf("create %s: %w", to.Name, err)
		}
		return nil
	}

	if EqualObjects(source, existing) {
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Re-fetch the target object to get the latest resourceVersion
		if err := to.Client.Get(ctx, to.Name, existing); err != nil {
			return err
		}
		update := existing.DeepCopy()
		for k := range update.Object {
			if k == metadataKey || k == statusKey {
				continue
			}
			if _, ok := source.Object[k]; !ok {
				delete(update.Object, k)
			}
		}
		for k, v := range source.Object {
			if k == metadataKey || k == statusKey {
				continue
			}
			update.Object[k] = v
		}
		return to.Client.Update(ctx, update)
	})
	if err != nil {
		return fmt.Errorf("update %s: %w", to.Name, err)
	}

	return nil
}

// Status copies the status of an object from one cluster to another.
func Status(ctx context.Context, gvk schema.GroupVersionKind, from, to Ref) error {
	source := &unstructured.Unstructured{}
	source.SetGroupVersionKind(gvk)
	if err := from.Client.Get(ctx, from.Name, source); err != nil {
		return fmt.Errorf("get %s: %w", from.Name, err)
	}

	status, ok := source.Object[statusKey]
	if !ok {
		return nil
	}

	target := &unstructured.Unstructured{}
	target.SetGroupVersionKind(gvk)
	if err := to.Client.Get(ctx, to.Name, target); err != nil {
		return fmt.Errorf("get %s: %w", to.Name, err)
	}

	if cmp.Equal(target.Object[statusKey], status, cmpopts.EquateEmpty()) {
		return nil
	}

	target.Object[statusKey] = status
	if err := to.Client.Status().Update(ctx, target); err != nil {
		return fmt.Errorf("update status of %s: %w", to.Name, err)
	}

	return nil
}

// Resource copies an object with [Spec] and then reflects the status the other way with [Status].
func Resource(ctx context.Context, gvk schema.GroupVersionKind, from, to Ref) error {
	if err := Spec(ctx, gvk, from, to); err != nil {
		return err
	}

	return Status(ctx, gvk, to, from)
}
