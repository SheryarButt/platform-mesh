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

package engine

import (
	"context"
	"sort"

	krov1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	rgdcontroller "github.com/kubernetes-sigs/kro/pkg/controller/resourcegraphdefinition"
	"github.com/kubernetes-sigs/kro/pkg/graph"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// storageVersion returns the CRD's storage version (falling back to the first).
func storageVersion(crd *apiextensionsv1.CustomResourceDefinition) string {
	if len(crd.Spec.Versions) == 0 {
		return ""
	}
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			return v.Name
		}
	}
	return crd.Spec.Versions[0].Name
}

// specFieldsFromCRD returns the sorted spec property names of the given version,
// used to populate the portal list/create forms.
func specFieldsFromCRD(crd *apiextensionsv1.CustomResourceDefinition, version string) []string {
	for _, v := range crd.Spec.Versions {
		if v.Name != version || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		spec, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			return nil
		}
		keys := make([]string, 0, len(spec.Properties))
		for k := range spec.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}
	return nil
}

func contentConfigName(rgdName string) string { return "kro-" + rgdName }

func graphKey(clusterName string, gvr schema.GroupVersionResource) string {
	return clusterName + "|" + gvr.String()
}

// instanceGVR derives the served GVR of the composite type from its generated CRD.
func instanceGVR(crd *apiextensionsv1.CustomResourceDefinition) schema.GroupVersionResource {
	version := crd.Spec.Versions[0].Name
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			version = v.Name
			break
		}
	}
	return schema.GroupVersionResource{
		Group:    crd.Spec.Group,
		Version:  version,
		Resource: crd.Spec.Names.Plural,
	}
}

// graphStatus is the part of an RGD's status derived from its compiled graph.
type graphStatus struct {
	kind             string
	topologicalOrder []string
	resources        []krov1alpha1.ResourceInformation
}

// statusFromGraph collects the status fields kro reports for a compiled graph.
func statusFromGraph(g *graph.Graph) graphStatus {
	return graphStatus{
		kind:             g.CRD.Spec.Names.Kind,
		topologicalOrder: g.TopologicalOrder,
		resources:        resourceDependencies(g),
	}
}

// resourceDependencies lists each node's dependencies in topological order. Nodes with none
// are left out, as in kro.
func resourceDependencies(g *graph.Graph) []krov1alpha1.ResourceInformation {
	info := make([]krov1alpha1.ResourceInformation, 0, len(g.Nodes))
	for _, id := range g.TopologicalOrder {
		node, ok := g.Nodes[id]
		if !ok || len(node.Meta.Dependencies) == 0 {
			continue
		}
		dependencies := make([]krov1alpha1.Dependency, 0, len(node.Meta.Dependencies))
		for _, dep := range node.Meta.Dependencies {
			dependencies = append(dependencies, krov1alpha1.Dependency{ID: dep})
		}
		info = append(info, krov1alpha1.ResourceInformation{ID: id, Dependencies: dependencies})
	}
	return info
}

// writeRGDStatus marks the RGD Active. Conditions come from kro's marker, so they carry kro's
// reasons, roll up into Ready, and prune the condition kro renamed in v0.9.
func writeRGDStatus(ctx context.Context, c ctrlruntimeclient.Client, name string, s graphStatus, portalErr error) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &krov1alpha1.ResourceGraphDefinition{}
		if err := c.Get(ctx, types.NamespacedName{Name: name}, cur); err != nil {
			return err
		}

		mark := rgdcontroller.NewConditionsMarkerFor(cur)
		mark.ResourceGraphValid()
		mark.KindReady(s.kind)
		mark.ControllerRunning()
		// One entry per RGD, so nothing is left to resolve once the controller is serving.
		mark.GraphRevisionsResolved(activeRevision)

		cur.Status.State = krov1alpha1.ResourceGraphDefinitionStateActive
		cur.Status.TopologicalOrder = s.topologicalOrder
		cur.Status.Resources = s.resources

		// Ours, not kro's, so set directly and kept out of the aggregate Ready.
		now := metav1.Now()
		queryable := krov1alpha1.Condition{
			Type:               conditionTypePortalQueryable,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: &now,
			ObservedGeneration: cur.Generation,
		}
		if portalErr != nil {
			reason := "InvalidAPIGroup"
			message := portalErr.Error()
			queryable.Status = metav1.ConditionFalse
			queryable.Reason = &reason
			queryable.Message = &message
		}
		cur.Status.Conditions = cur.Status.Conditions.Set(queryable)

		return c.Status().Update(ctx, cur)
	})
}

// writeRGDKindNotReady marks the RGD Inactive with KindReady=False, as kro does when it
// will not serve the type. What was already published keeps serving.
func writeRGDKindNotReady(ctx context.Context, c ctrlruntimeclient.Client, name, message string) error {
	return writeRGDNotReady(ctx, c, name, krov1alpha1.RGDConditionTypeKindReady, message,
		func(m *rgdcontroller.ConditionsMarker) { m.KindUnready(message) })
}

// writeRGDGraphInvalid marks the RGD Inactive with GraphAccepted=False, as kro does for a
// graph that will not build. Without it, a permanent failure looks like a slow API.
func writeRGDGraphInvalid(ctx context.Context, c ctrlruntimeclient.Client, name, message string) error {
	return writeRGDNotReady(ctx, c, name, krov1alpha1.RGDConditionTypeGraphAccepted, message,
		func(m *rgdcontroller.ConditionsMarker) { m.ResourceGraphInvalid(message) })
}

// writeRGDNotReady marks the RGD Inactive with one of kro's failure conditions, skipping the
// write when the status already says this. Callers retry on a timer.
func writeRGDNotReady(
	ctx context.Context,
	c ctrlruntimeclient.Client,
	name string,
	conditionType krov1alpha1.ConditionType,
	message string,
	mark func(*rgdcontroller.ConditionsMarker),
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &krov1alpha1.ResourceGraphDefinition{}
		if err := c.Get(ctx, types.NamespacedName{Name: name}, cur); err != nil {
			return err
		}
		if alreadyNotReady(cur, conditionType, message) {
			return nil
		}
		mark(rgdcontroller.NewConditionsMarkerFor(cur))
		cur.Status.State = krov1alpha1.ResourceGraphDefinitionStateInactive
		return c.Status().Update(ctx, cur)
	})
}

// alreadyNotReady reports whether the RGD already carries this exact failure.
func alreadyNotReady(rgd *krov1alpha1.ResourceGraphDefinition, conditionType krov1alpha1.ConditionType, message string) bool {
	if rgd.Status.State != krov1alpha1.ResourceGraphDefinitionStateInactive {
		return false
	}
	for _, cond := range rgd.Status.Conditions {
		if cond.Type != conditionType {
			continue
		}
		return cond.Status == metav1.ConditionFalse &&
			cond.Message != nil && *cond.Message == message &&
			cond.ObservedGeneration == rgd.Generation
	}
	return false
}
