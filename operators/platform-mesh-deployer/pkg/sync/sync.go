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

// Package sync applies objects to another cluster and prunes what is no
// longer wanted. It is the deployer's single cross-cluster transport, shared
// by the compiled-CR copier and by module deploys.
package sync

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// FieldOwner identifies the deployer in server-side apply.
const FieldOwner = "platform-mesh-deployer"

// Strip removes the metadata that is bound to the source cluster and must not travel with an object.
func Strip(obj *unstructured.Unstructured) {
	obj.SetResourceVersion("")
	obj.SetUID("")
	obj.SetGeneration(0)
	obj.SetCreationTimestamp(metav1.Time{})
	obj.SetOwnerReferences(nil)
	obj.SetManagedFields(nil)
	obj.SetFinalizers(nil)
	unstructured.RemoveNestedField(obj.Object, "metadata", "selfLink")
	unstructured.RemoveNestedField(obj.Object, "metadata", "deletionTimestamp")
	unstructured.RemoveNestedField(obj.Object, "status")
}

// Apply server-side applies obj, taking ownership of conflicting fields.
func Apply(ctx context.Context, cl ctrlruntimeclient.Client, obj *unstructured.Unstructured) error {
	err := cl.Apply(ctx, ctrlruntimeclient.ApplyConfigurationFromUnstructured(obj),
		ctrlruntimeclient.FieldOwner(FieldOwner), ctrlruntimeclient.ForceOwnership)
	if err != nil {
		return fmt.Errorf("applying %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
	}
	return nil
}

// EnsureNamespace creates the namespace if it is missing, so applied objects have somewhere to land.
func EnsureNamespace(ctx context.Context, cl ctrlruntimeclient.Client, name string) error {
	if name == "" {
		return nil
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := cl.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %q: %w", name, err)
	}
	return nil
}

// CopySpec copies the spec of src to the object with gvk and key in the dst client.
func CopySpec(ctx context.Context, dst ctrlruntimeclient.Client, gvk schema.GroupVersionKind, key ctrlruntimeclient.ObjectKey, src *unstructured.Unstructured) error {
	spec, _, err := unstructured.NestedFieldCopy(src.Object, "spec")
	if err != nil {
		return fmt.Errorf("reading spec of %s %s/%s: %w", gvk.Kind, key.Namespace, key.Name, err)
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetNamespace(key.Namespace)
	obj.SetName(key.Name)
	_, err = controllerutil.CreateOrUpdate(ctx, dst, obj, func() error {
		obj.SetLabels(src.GetLabels())
		return unstructured.SetNestedField(obj.Object, spec, "spec")
	})
	if err != nil {
		return fmt.Errorf("copying %s %s/%s: %w", gvk.Kind, key.Namespace, key.Name, err)
	}
	return nil
}

// ReflectStatus copies the status of the object with gvk and key from the from client to the same object on the to client.
func ReflectStatus(ctx context.Context, from, to ctrlruntimeclient.Client, gvk schema.GroupVersionKind, key ctrlruntimeclient.ObjectKey) error {
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(gvk)
	if err := from.Get(ctx, key, current); err != nil {
		return ctrlruntimeclient.IgnoreNotFound(err)
	}
	status, ok, err := unstructured.NestedFieldCopy(current.Object, "status")
	if err != nil || !ok {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &unstructured.Unstructured{}
		latest.SetGroupVersionKind(gvk)
		if err := to.Get(ctx, key, latest); err != nil {
			return ctrlruntimeclient.IgnoreNotFound(err)
		}
		if err := unstructured.SetNestedField(latest.Object, status, "status"); err != nil {
			return err
		}
		return to.Status().Update(ctx, latest)
	})
}

// ObjectKey identifies an applied object across kinds and namespaces.
type ObjectKey struct {
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
}

// KeyOf returns the ObjectKey of an object.
func KeyOf(obj *unstructured.Unstructured) ObjectKey {
	return ObjectKey{GVK: obj.GroupVersionKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}
}

// Prune deletes objects of the given kinds that carry the selector labels but are not in keep.
func Prune(ctx context.Context, cl ctrlruntimeclient.Client, gvks []schema.GroupVersionKind, selector map[string]string, keep map[ObjectKey]struct{}) error {
	for _, gvk := range gvks {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := cl.List(ctx, list, ctrlruntimeclient.MatchingLabels(selector)); err != nil {
			if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
				continue
			}
			return fmt.Errorf("listing %s: %w", gvk.Kind, err)
		}
		for i := range list.Items {
			item := &list.Items[i]
			if _, ok := keep[KeyOf(item)]; ok {
				continue
			}
			if err := cl.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting %s %s/%s: %w", gvk.Kind, item.GetNamespace(), item.GetName(), err)
			}
		}
	}
	return nil
}
