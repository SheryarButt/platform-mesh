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
	"bytes"
	"context"
	"fmt"
	"io"

	"ocm.software/open-component-model/bindings/go/blob"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/celtemplate"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

// Render downloads an instance's payload resource, interpolates it and returns
// the objects to apply, with the generated ConfigMap first so a component can
// rely on it existing.
func (r *Resolved) Render(ctx context.Context, inst Instance, endpoints map[string]string) ([]*unstructured.Unstructured, error) {
	celCtx, err := r.Context(inst)
	if err != nil {
		return nil, err
	}
	celCtx.Endpoints = endpoints

	res, err := Resource(r.CV, inst.Component.Resource)
	if err != nil {
		return nil, err
	}
	data, err := r.CV.Download(ctx, res)
	if err != nil {
		return nil, fmt.Errorf("downloading resource %q: %w", inst.Component.Resource, err)
	}
	raw, err := readBlob(data)
	if err != nil {
		return nil, fmt.Errorf("reading resource %q: %w", inst.Component.Resource, err)
	}

	objs, err := decode(raw)
	if err != nil {
		return nil, fmt.Errorf("decoding resource %q: %w", inst.Component.Resource, err)
	}

	out := []*unstructured.Unstructured{r.configMap(inst, celCtx)}
	for _, obj := range objs {
		templated, err := celtemplate.InterpolateValue(obj.Object, celCtx)
		if err != nil {
			return nil, fmt.Errorf("templating resource %q: %w", inst.Component.Resource, err)
		}
		fields, ok := templated.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("templating resource %q: object became %T", inst.Component.Resource, templated)
		}
		out = append(out, &unstructured.Unstructured{Object: fields})
	}

	for _, obj := range out {
		if obj.GetNamespace() == "" {
			obj.SetNamespace(inst.Component.Namespace)
		}
		r.label(obj, inst)
	}
	return out, nil
}

// Context builds the templating context of an instance.
func (r *Resolved) Context(inst Instance) (celtemplate.Context, error) {
	values, err := r.Values()
	if err != nil {
		return celtemplate.Context{}, err
	}
	desc, err := Descriptor(r.CV.Descriptor())
	if err != nil {
		return celtemplate.Context{}, err
	}
	workspace, workspaces := r.WorkspacePaths()
	secrets := map[string]string{}
	for _, kc := range r.Kubeconfigs(inst.Component) {
		secrets[kc.Name] = KubeconfigSecretName(r.OCMModule.Name, kc.Name)
	}

	return celtemplate.Context{
		PlatformMesh:      r.OCMModule.Spec.PlatformMeshRef.Name,
		Component:         inst.Component.Name,
		ShardGroup:        inst.ShardGroup,
		Cluster:           inst.Cluster.ClusterID,
		OCMModule:         r.OCMModule.Name,
		Placement:         string(inst.Component.Placement),
		TargetNamespace:   inst.Component.Namespace,
		ConfigMap:         ConfigMapName(r.OCMModule.Name, inst.Component.Name),
		Workspace:         workspace,
		Workspaces:        workspaces,
		KubeconfigSecrets: secrets,
		Values:            values,
		OCM:               desc,
	}, nil
}

// ConfigMapName is the deterministic name of an instance's generated ConfigMap.
// It is stable per cluster because a cluster holds at most one instance of a
// component, so a payload can reference it without knowing the cluster.
func ConfigMapName(module, component string) string {
	return module + "-" + component
}

// configMap holds the resolved facts a component reads at runtime.
func (r *Resolved) configMap(inst Instance, celCtx celtemplate.Context) *unstructured.Unstructured {
	data := map[string]any{
		"MODULE":        r.OCMModule.Name,
		"COMPONENT":     inst.Component.Name,
		"PLACEMENT":     string(inst.Component.Placement),
		"CLUSTER":       inst.Cluster.ClusterID,
		"PLATFORM_MESH": r.OCMModule.Spec.PlatformMeshRef.Name,
	}
	if inst.ShardGroup != "" {
		data["SHARD_GROUP"] = inst.ShardGroup
	}
	for name, path := range celCtx.Workspaces {
		data["WORKSPACE_"+envSuffix(name)] = path
	}
	if celCtx.Workspace != "" {
		data["WORKSPACE"] = celCtx.Workspace
	}
	for name, url := range celCtx.Endpoints {
		data["ENDPOINT_"+envSuffix(name)] = url
	}

	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      celCtx.ConfigMap,
			"namespace": inst.Component.Namespace,
		},
		"data": data,
	}}
	return obj
}

// label marks an object as owned by this module instance so it can be found
// and pruned later.
func (r *Resolved) label(obj *unstructured.Unstructured, inst Instance) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[LabelPlatformMesh] = r.OCMModule.Spec.PlatformMeshRef.Name
	labels[LabelModule] = r.OCMModule.Name
	labels[LabelComponent] = inst.Component.Name
	labels[LabelCluster] = inst.Cluster.ClusterID
	obj.SetLabels(labels)
}

// InstanceSelector matches every object applied for one component instance.
func (r *Resolved) InstanceSelector(inst Instance) map[string]string {
	return map[string]string{
		LabelPlatformMesh: r.OCMModule.Spec.PlatformMeshRef.Name,
		LabelModule:       r.OCMModule.Name,
		LabelComponent:    inst.Component.Name,
		LabelCluster:      inst.Cluster.ClusterID,
	}
}

// OCMModuleSelector matches every object applied for a module on one cluster.
func OCMModuleSelector(mod *pmdeployv1alpha1.OCMModule, clusterID string) map[string]string {
	return map[string]string{
		LabelPlatformMesh: mod.Spec.PlatformMeshRef.Name,
		LabelModule:       mod.Name,
		LabelCluster:      clusterID,
	}
}

func readBlob(b blob.ReadOnlyBlob) ([]byte, error) {
	rc, err := b.ReadCloser()
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read-only
	return io.ReadAll(rc)
}

// decode splits a multi-document YAML/JSON manifest into objects, skipping
// empty documents.
func decode(raw []byte) ([]*unstructured.Unstructured, error) {
	reader := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var out []*unstructured.Unstructured
	for {
		fields := map[string]any{}
		if err := reader.Decode(&fields); err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, err
		}
		if len(fields) == 0 {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: fields})
	}
}
