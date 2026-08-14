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

// Package composition wraps kro's graph engine (used as a library) to compile a
// ResourceGraphDefinition into its generated CRD and resource graph.
package composition

import (
	"fmt"

	krov1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"github.com/kubernetes-sigs/kro/pkg/graph"

	"k8s.io/client-go/rest"
)

// Limits mirror kro's controller defaults for collection expansion: at most 1000
// resources per expanded collection, and at most 10 forEach dimensions on a single
// resource. The dimension cap is validated when the RGD is processed, before any
// expansion is evaluated — the cheap gate against deeply nested cartesian products
// in a consumer-authored graph.
const (
	maxCollectionSize          = 1000
	maxCollectionDimensionSize = 10
)

// Compiler builds kro graphs against a target (a kcp workspace). The rest.Config
// must point at the workspace whose served APIs the RGD composes, because kro's
// builder resolves the RGD's referenced resources via discovery against it.
type Compiler struct {
	builder *graph.Builder
}

// NewCompiler constructs a Compiler bound to the given workspace rest.Config.
func NewCompiler(cfg *rest.Config) (*Compiler, error) {
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("http client: %w", err)
	}
	b, err := graph.NewBuilder(cfg, httpClient)
	if err != nil {
		return nil, fmt.Errorf("graph builder: %w", err)
	}
	return &Compiler{builder: b}, nil
}

// Compile turns an RGD into its processed graph. graph.CRD is the composite type
// to publish as a bound API in the workspace; graph.TopologicalOrder / Nodes drive
// child materialization. Fails if the RGD references resources not served by the target
// workspace (the composed provider APIs must already be available there).
func (c *Compiler) Compile(rgd *krov1alpha1.ResourceGraphDefinition) (*graph.Graph, error) {
	g, err := c.builder.NewResourceGraphDefinition(rgd, graph.RGDConfig{
		MaxCollectionSize:          maxCollectionSize,
		MaxCollectionDimensionSize: maxCollectionDimensionSize,
	})
	if err != nil {
		return nil, fmt.Errorf("compile RGD %q: %w", rgd.Name, err)
	}
	return g, nil
}
