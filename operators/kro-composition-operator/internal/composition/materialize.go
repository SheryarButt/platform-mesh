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

package composition

import (
	"context"
	"errors"
	"fmt"

	"github.com/kubernetes-sigs/kro/pkg/graph"
	kroruntime "github.com/kubernetes-sigs/kro/pkg/runtime"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
)

const fieldManager = "kro-composition-operator"

// Materializer resolves an instance of a composite type against its RGD graph and
// applies the resulting child resources into the target (workspace) cluster.
//
// Single-target: all children are applied into the same cluster the instance lives
// in (the consumer workspace), via a dynamic client scoped to it. This matches the
// compose-only model — children are instances of provider APIs already available in
// the workspace; the operator never reaches out to a separate compute cluster.
type Materializer struct {
	Dyn    dynamic.Interface
	Mapper meta.RESTMapper
}

// Materialize builds the kro runtime for (graph, instance), walks the nodes in
// topological order, and applies each resource node's desired objects — feeding
// applied objects back as observed state so downstream CEL expressions resolve.
//
// Returns ready=false (nil error) when a dependency is not yet satisfied
// (kro ErrDataPending, or an external reference not found): the caller should requeue.
func (m *Materializer) Materialize(ctx context.Context, g *graph.Graph, gvr schema.GroupVersionResource, instance *unstructured.Unstructured) (bool, error) {
	rt, err := kroruntime.FromGraph(g, instance, graph.RGDConfig{
		MaxCollectionSize:          maxCollectionSize,
		MaxCollectionDimensionSize: maxCollectionDimensionSize,
	})
	if err != nil {
		return false, fmt.Errorf("build runtime: %w", err)
	}

	for _, node := range rt.Nodes() {
		ignored, err := node.IsIgnored()
		if err != nil {
			return false, fmt.Errorf("node %s includeWhen: %w", node.Spec.Meta.ID, err)
		}
		if ignored {
			continue
		}

		desired, err := node.GetDesired()
		if err != nil {
			if errors.Is(err, kroruntime.ErrDataPending) {
				return false, nil // a CEL dependency is not ready yet; requeue
			}
			return false, fmt.Errorf("node %s resolve: %w", node.Spec.Meta.ID, err)
		}

		switch node.Spec.Meta.Type {
		case graph.NodeTypeExternal:
			// Read-only reference: fetch and feed observed state for downstream CEL.
			if len(desired) == 0 {
				continue
			}
			obj, err := m.get(ctx, desired[0])
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil {
				return false, fmt.Errorf("node %s external get: %w", node.Spec.Meta.ID, err)
			}
			node.SetObserved([]*unstructured.Unstructured{obj})

		case graph.NodeTypeExternalCollection:
			// externalRef + forEach: read-only. List by label selector and feed the
			// observed items to downstream CEL (never applied).
			if len(desired) == 0 {
				continue
			}
			items, err := m.list(ctx, desired[0])
			if err != nil {
				return false, fmt.Errorf("node %s external list: %w", node.Spec.Meta.ID, err)
			}
			node.SetObserved(items)

		default: // NodeTypeResource, and NodeTypeCollection (forEach): apply every
			// desired object. For a forEach collection GetDesired returns the full
			// expanded set, so the same apply loop materializes all of them.
			observed := make([]*unstructured.Unstructured, 0, len(desired))
			for _, d := range desired {
				setOwnerRef(d, instance) // so deleting the instance GCs its children
				applied, err := m.apply(ctx, d)
				if err != nil {
					return false, fmt.Errorf("node %s apply: %w", node.Spec.Meta.ID, err)
				}
				observed = append(observed, applied)
			}
			node.SetObserved(observed)
		}
	}

	// All children applied and observed → resolve the instance's status (its CEL
	// expressions reference the now-observed children) and write it back.
	if err := m.writeStatus(ctx, rt, gvr, instance); err != nil {
		return true, fmt.Errorf("write instance status: %w", err)
	}
	return true, nil
}

// writeStatus resolves the instance node (status CEL now evaluable against observed
// children) and patches the resolved status + state onto the live instance.
func (m *Materializer) writeStatus(ctx context.Context, rt *kroruntime.Runtime, gvr schema.GroupVersionResource, instance *unstructured.Unstructured) error {
	desired, err := rt.Instance().GetDesired()
	if err != nil || len(desired) == 0 {
		//nolint:nilerr // status is best-effort: an unresolved instance node is non-fatal and retried next reconcile
		return nil
	}
	status := map[string]any{"state": "Active"}
	if resolved, found, _ := unstructured.NestedMap(desired[0].Object, "status"); found {
		for k, v := range resolved {
			status[k] = v
		}
	}

	ri := m.Dyn.Resource(gvr).Namespace(instance.GetNamespace())
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur, err := ri.Get(ctx, instance.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := unstructured.SetNestedMap(cur.Object, status, "status"); err != nil {
			return err
		}
		if _, err := ri.UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
			// Fall back to a plain update if the CRD has no /status subresource.
			if apierrors.IsNotFound(err) {
				_, uerr := ri.Update(ctx, cur, metav1.UpdateOptions{})
				return uerr
			}
			return err
		}
		return nil
	})
}

func (m *Materializer) resourceInterface(obj *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := obj.GroupVersionKind()
	mapping, err := m.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("rest mapping for %s: %w", gvk, err)
	}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return m.Dyn.Resource(mapping.Resource).Namespace(obj.GetNamespace()), nil
	}
	return m.Dyn.Resource(mapping.Resource), nil
}

func (m *Materializer) apply(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	ri, err := m.resourceInterface(obj)
	if err != nil {
		return nil, err
	}
	return ri.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{FieldManager: fieldManager, Force: true})
}

func (m *Materializer) get(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	ri, err := m.resourceInterface(obj)
	if err != nil {
		return nil, err
	}
	return ri.Get(ctx, obj.GetName(), metav1.GetOptions{})
}

// list reads all objects of the (external-collection) resource matching the label
// selector carried on the resolved object's metadata.selector.
func (m *Materializer) list(ctx context.Context, obj *unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	selector, err := externalCollectionSelector(obj)
	if err != nil {
		return nil, err
	}
	ri, err := m.resourceInterface(obj)
	if err != nil {
		return nil, err
	}
	ul, err := ri.List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, err
	}
	items := make([]*unstructured.Unstructured, len(ul.Items))
	for i := range ul.Items {
		items[i] = &ul.Items[i]
	}
	return items, nil
}

// externalCollectionSelector extracts the label selector from a resolved external
// collection node's metadata.selector (empty selector = match everything).
func externalCollectionSelector(obj *unstructured.Unstructured) (labels.Selector, error) {
	raw, found, err := unstructured.NestedMap(obj.Object, "metadata", "selector")
	if err != nil || !found {
		//nolint:nilerr // absent or unparseable selector defaults to match-everything (documented behavior)
		return labels.Everything(), nil
	}
	ls := &metav1.LabelSelector{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, ls); err != nil {
		return nil, fmt.Errorf("convert selector: %w", err)
	}
	return metav1.LabelSelectorAsSelector(ls)
}

// setOwnerRef makes child owned by instance so instance deletion garbage-collects
// it. Only applied when both are namespaced and co-located (a namespaced owner
// cannot own a cluster-scoped or cross-namespace object); otherwise skipped.
func setOwnerRef(child, instance *unstructured.Unstructured) {
	if instance.GetNamespace() == "" || child.GetNamespace() == "" ||
		child.GetNamespace() != instance.GetNamespace() {
		return
	}
	yes := true
	child.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion:         instance.GetAPIVersion(),
		Kind:               instance.GetKind(),
		Name:               instance.GetName(),
		UID:                instance.GetUID(),
		Controller:         &yes,
		BlockOwnerDeletion: &yes,
	}})
}
