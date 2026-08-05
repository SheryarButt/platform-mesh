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
	"encoding/json"
	"fmt"

	descriptorruntime "ocm.software/open-component-model/bindings/go/descriptor/runtime"
	ocmruntime "ocm.software/open-component-model/bindings/go/runtime"
)

// Descriptor renders a component descriptor for the payload templating, so a
// module's manifests can read the component's own resources rather than have
// them passed through spec.values.
//
// Resources, sources and references stay lists because a name does not identify
// an element on its own; the payload picks one with byName() or filter().
func Descriptor(desc *descriptorruntime.Descriptor) (map[string]any, error) {
	if desc == nil {
		return map[string]any{}, nil
	}
	c := desc.Component

	resources := make([]any, 0, len(c.Resources))
	for i := range c.Resources {
		r, err := resourceMap(&c.Resources[i])
		if err != nil {
			return nil, err
		}
		resources = append(resources, r)
	}

	sources := make([]any, 0, len(c.Sources))
	for i := range c.Sources {
		s, err := sourceMap(&c.Sources[i])
		if err != nil {
			return nil, err
		}
		sources = append(sources, s)
	}

	references := make([]any, 0, len(c.References))
	for i := range c.References {
		references = append(references, map[string]any{
			"name":          c.References[i].Name,
			"version":       c.References[i].Version,
			"component":     c.References[i].Component,
			"extraIdentity": identityMap(c.References[i].ExtraIdentity),
			"labels":        labelMap(c.References[i].Labels),
		})
	}

	out := map[string]any{
		"name":       c.Name,
		"version":    c.Version,
		"provider":   c.Provider.Name,
		"labels":     labelMap(c.Labels),
		"resources":  resources,
		"sources":    sources,
		"references": references,
	}
	normalized, err := normalizeNumbers(out)
	if err != nil {
		return nil, err
	}
	return normalized.(map[string]any), nil
}

func resourceMap(r *descriptorruntime.Resource) (map[string]any, error) {
	access, err := accessMap(r.Access)
	if err != nil {
		return nil, fmt.Errorf("resource %q: %w", r.Name, err)
	}
	return map[string]any{
		"name":          r.Name,
		"version":       r.Version,
		"type":          r.Type,
		"relation":      string(r.Relation),
		"extraIdentity": identityMap(r.ExtraIdentity),
		"labels":        labelMap(r.Labels),
		"access":        access,
		"digest":        digestMap(r.Digest),
	}, nil
}

func sourceMap(s *descriptorruntime.Source) (map[string]any, error) {
	access, err := accessMap(s.Access)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", s.Name, err)
	}
	return map[string]any{
		"name":          s.Name,
		"version":       s.Version,
		"type":          s.Type,
		"extraIdentity": identityMap(s.ExtraIdentity),
		"labels":        labelMap(s.Labels),
		"access":        access,
	}, nil
}

// accessMap renders an access specification as the attribute set the payload
// reads, such as the imageReference of an ociArtifact access. Reading a
// descriptor from a repository always yields a *runtime.Raw, so the spec
// arrives as the JSON it was written as.
func accessMap(access ocmruntime.Typed) (map[string]any, error) {
	if access == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(access)
	if err != nil {
		return nil, fmt.Errorf("marshalling access: %w", err)
	}
	out := map[string]any{}
	if err := decodeJSON(raw, &out); err != nil {
		return nil, fmt.Errorf("decoding access: %w", err)
	}
	return out, nil
}

// decodeJSON decodes with UseNumber so normalizeNumbers can hold whole numbers
// as int64, which is all a Kubernetes object may carry.
func decodeJSON(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(out)
}

func digestMap(d *descriptorruntime.Digest) map[string]any {
	if d == nil {
		return map[string]any{}
	}
	return map[string]any{
		"hashAlgorithm":          d.HashAlgorithm,
		"normalisationAlgorithm": d.NormalisationAlgorithm,
		"value":                  d.Value,
	}
}

func identityMap(id ocmruntime.Identity) map[string]any {
	out := make(map[string]any, len(id))
	for k, v := range id {
		out[k] = v
	}
	return out
}

// labelMap keys labels by name, which OCM requires to be unique per element.
func labelMap(labels []descriptorruntime.Label) map[string]any {
	out := make(map[string]any, len(labels))
	for _, l := range labels {
		var v any
		if err := decodeJSON(l.Value, &v); err != nil {
			// A label value is arbitrary JSON; an undecodable one is surfaced
			// as its raw text rather than failing the whole render.
			v = string(l.Value)
		}
		out[l.Name] = v
	}
	return out
}
