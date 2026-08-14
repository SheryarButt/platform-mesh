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

// writeRGDStatus marks the RGD Active with its topological order and readiness
// conditions — mirroring upstream kro's RGD reconciler — so the portal and kubectl
// report the graph as serving (the portal's readiness check reads status.state). The
// generated CRD and dynamic controller are already up by the time this is called.
func writeRGDStatus(ctx context.Context, c ctrlruntimeclient.Client, name string, topologicalOrder []string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &krov1alpha1.ResourceGraphDefinition{}
		if err := c.Get(ctx, types.NamespacedName{Name: name}, cur); err != nil {
			return err
		}
		cur.Status.State = krov1alpha1.ResourceGraphDefinitionStateActive
		cur.Status.TopologicalOrder = topologicalOrder
		now := metav1.Now()
		for _, t := range []krov1alpha1.ConditionType{
			krov1alpha1.RGDConditionTypeGraphAccepted,
			krov1alpha1.RGDConditionTypeKindReady,
			krov1alpha1.RGDConditionTypeControllerReady,
		} {
			cur.Status.Conditions = cur.Status.Conditions.Set(krov1alpha1.Condition{
				Type: t, Status: metav1.ConditionTrue, LastTransitionTime: &now,
			})
		}
		return c.Status().Update(ctx, cur)
	})
}
