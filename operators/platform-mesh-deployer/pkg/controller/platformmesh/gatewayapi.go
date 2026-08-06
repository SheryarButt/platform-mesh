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

package platformmesh

import (
	"context"
	"fmt"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

type gatewayAPIRenderer struct {
	values pmdeployv1alpha1.GatewayAPIValues
}

func newGatewayAPIRenderer(values *pmdeployv1alpha1.GatewayAPIValues) (stackRenderer, error) {
	if values == nil {
		return nil, fmt.Errorf("gatewayAPI configuration is required")
	}
	return gatewayAPIRenderer{values: *values}, nil
}

func (g gatewayAPIRenderer) ensure(ctx context.Context, workload ctrlruntimeclient.Client, p routeParams) error {
	route := &gwapiv1alpha2.TLSRoute{ObjectMeta: metav1.ObjectMeta{Name: p.name, Namespace: p.namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, workload, route, func() error {
		route.Labels = map[string]string{
			components.LabelPlatformMesh: p.pmName,
			components.LabelComponent:    p.component,
			components.LabelCluster:      p.clusterID,
		}
		parent := gwapiv1alpha2.ParentReference{Name: gwapiv1.ObjectName(g.values.GatewayName)}
		if g.values.GatewayNamespace != "" {
			ns := gwapiv1.Namespace(g.values.GatewayNamespace)
			parent.Namespace = &ns
		}
		if g.values.SectionName != "" {
			sn := gwapiv1.SectionName(g.values.SectionName)
			parent.SectionName = &sn
		}
		//nolint:unconvert // int32 -> gwapiv1.PortNumber is required; unconvert false positive.
		pn := gwapiv1.PortNumber(p.port)
		route.Spec.ParentRefs = []gwapiv1alpha2.ParentReference{parent}
		route.Spec.Hostnames = []gwapiv1alpha2.Hostname{gwapiv1.Hostname(p.host)}
		route.Spec.Rules = []gwapiv1alpha2.TLSRouteRule{{
			BackendRefs: []gwapiv1alpha2.BackendRef{{
				BackendObjectReference: gwapiv1.BackendObjectReference{
					Name: gwapiv1.ObjectName(p.service),
					Port: &pn,
				},
			}},
		}}
		return nil
	})
	return err
}

func (gatewayAPIRenderer) teardown(ctx context.Context, workload ctrlruntimeclient.Client, pmName, namespace string, desired map[string]struct{}) error {
	list := &gwapiv1alpha2.TLSRouteList{}
	if err := workload.List(ctx, list,
		ctrlruntimeclient.InNamespace(namespace),
		ctrlruntimeclient.MatchingLabels{components.LabelPlatformMesh: pmName},
		// Select positively on a label only this step writes; modules label
		// their own routes with the same platform mesh and would be deleted too.
		ctrlruntimeclient.HasLabels{components.LabelComponent},
	); err != nil {
		return err
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return err
	}
	for _, item := range items {
		route := item.(ctrlruntimeclient.Object)
		if _, ok := desired[route.GetName()]; ok {
			continue
		}
		if err := workload.Delete(ctx, route); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
