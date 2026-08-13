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

package ocm

import (
	"context"
	"errors"

	"ocm.software/open-component-model/bindings/go/blob"
	"ocm.software/open-component-model/bindings/go/credentials"
	descriptorruntime "ocm.software/open-component-model/bindings/go/descriptor/runtime"
	"ocm.software/open-component-model/bindings/go/runtime"
)

// ErrNotFound is returned when a component version or resource does not exist.
var ErrNotFound = errors.New("ocm: not found")

// OCMRepositorySpec defines an OCM repository.
type OCMRepositorySpec struct {
	URL   string
	Creds credentials.Resolver
}

// Resolver resolves a component version from a repository.
type Resolver interface {
	Resolve(ctx context.Context, repo OCMRepositorySpec, component, version string) (ComponentVersion, error)
}

// ComponentVersion is a resolved CV.
type ComponentVersion interface {
	Descriptor() *descriptorruntime.Descriptor
	Resource(id runtime.Identity) (*descriptorruntime.Resource, error)
	ResourcesByType(typ string) []*descriptorruntime.Resource
	Download(ctx context.Context, res *descriptorruntime.Resource) (blob.ReadOnlyBlob, error)
}
