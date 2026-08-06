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

// Package module resolves OCMModules against OCM and fans their components out
// over the engaged clusters.
package ocmmodule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	descriptorruntime "ocm.software/open-component-model/bindings/go/descriptor/runtime"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocm"
)

// Labels set on every object the deployer applies for a module, used for
// teardown and for finding an instance's objects again.
const (
	LabelPlatformMesh = "deploy.platform-mesh.io/platform-mesh"
	LabelModule       = "deploy.platform-mesh.io/ocmmodule"
	LabelComponent    = "deploy.platform-mesh.io/ocmmodule-component"
	LabelCluster      = "deploy.platform-mesh.io/cluster"
)

// Resolved carries a OCMModule together with its resolved component version, so
// the subroutines after resolve do not each re-fetch it.
type Resolved struct {
	OCMModule *pmdeployv1alpha1.OCMModule
	CV        ocm.ComponentVersion
}

// Resolve fetches the module's component version and checks that every
// component's payload resource exists in it.
func Resolve(ctx context.Context, resolver ocm.Resolver, mod *pmdeployv1alpha1.OCMModule, fallback *pmdeployv1alpha1.OCMRepository) (*Resolved, error) {
	repo := fallback
	if mod.Spec.OCM != nil {
		repo = mod.Spec.OCM
	}
	if repo == nil || repo.URL == "" {
		return nil, fmt.Errorf("no OCM repository for module %q", mod.Name)
	}

	// TODO(ntnn): build credentials.Resolver from repo.SecretRef for private registries.
	cv, err := resolver.Resolve(ctx, ocm.OCMRepositorySpec{URL: repo.URL}, mod.Spec.Component, mod.Spec.Version)
	if err != nil {
		return nil, fmt.Errorf("resolving %s:%s: %w", mod.Spec.Component, mod.Spec.Version, err)
	}

	for _, component := range mod.Spec.Components {
		if _, err := Resource(cv, component.Resource); err != nil {
			return nil, fmt.Errorf("component %q: %w", component.Name, err)
		}
	}
	return &Resolved{OCMModule: mod, CV: cv}, nil
}

// Resource looks a resource up by name in the component version.
// The lookup goes through the descriptor rather than the component version's
// identity index: a resource identity carries its version as well as its name,
// and a module only names the resource.
func Resource(cv ocm.ComponentVersion, name string) (*descriptorruntime.Resource, error) {
	desc := cv.Descriptor()
	if desc == nil {
		return nil, fmt.Errorf("resource %q: component version has no descriptor", name)
	}
	for i := range desc.Component.Resources {
		if desc.Component.Resources[i].Name == name {
			return &desc.Component.Resources[i], nil
		}
	}
	return nil, fmt.Errorf("resource %q: %w", name, ocm.ErrNotFound)
}

// Digest returns the digest of the signed component descriptor, empty when the
// component version is unsigned.
func (r *Resolved) Digest() string {
	desc := r.CV.Descriptor()
	if desc == nil || len(desc.Signatures) == 0 {
		return ""
	}
	d := desc.Signatures[0].Digest
	if d.Value == "" {
		return ""
	}
	return d.HashAlgorithm + ":" + d.Value
}

// Values decodes the module's spec.values for templating.
func (r *Resolved) Values() (map[string]any, error) {
	raw := r.OCMModule.Spec.Values
	if raw == nil || len(raw.Raw) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw.Raw))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding spec.values: %w", err)
	}
	normalized, err := normalizeNumbers(out)
	if err != nil {
		return nil, fmt.Errorf("decoding spec.values: %w", err)
	}
	return normalized.(map[string]any), nil
}

// normalizeNumbers turns json.Number into int64 or float64. Kubernetes objects
// may only hold int64, so a whole number must not stay a float.
func normalizeNumbers(v any) (any, error) {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i, nil
		}
		f, err := t.Float64()
		if err != nil {
			return nil, fmt.Errorf("number %q: %w", t.String(), err)
		}
		return f, nil
	case map[string]any:
		for k, val := range t {
			n, err := normalizeNumbers(val)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			t[k] = n
		}
		return t, nil
	case []any:
		for i, val := range t {
			n, err := normalizeNumbers(val)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			t[i] = n
		}
		return t, nil
	default:
		return v, nil
	}
}
