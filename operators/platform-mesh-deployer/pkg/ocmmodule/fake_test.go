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

package ocmmodule_test

import (
	"context"
	"io"
	"strings"

	"ocm.software/open-component-model/bindings/go/blob"
	descriptorruntime "ocm.software/open-component-model/bindings/go/descriptor/runtime"
	"ocm.software/open-component-model/bindings/go/runtime"

	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocm"
)

// fakeCV is an in-memory component version: resource name -> payload.
type fakeCV struct {
	contents map[string]string
	desc     *descriptorruntime.Descriptor
}

func newFakeCV(contents map[string]string) *fakeCV {
	desc := &descriptorruntime.Descriptor{}
	for name := range contents {
		// A real resource identity carries its version too, so the fake
		// must set one or it would accept lookups the store rejects.
		desc.Component.Resources = append(desc.Component.Resources, descriptorruntime.Resource{
			ElementMeta: descriptorruntime.ElementMeta{
				ObjectMeta: descriptorruntime.ObjectMeta{Name: name, Version: "0.1.0"},
			},
			Type: "platform-mesh.io/manifests",
		})
	}
	return &fakeCV{contents: contents, desc: desc}
}

func (f *fakeCV) Descriptor() *descriptorruntime.Descriptor { return f.desc }

func (f *fakeCV) Resource(id runtime.Identity) (*descriptorruntime.Resource, error) {
	for i := range f.desc.Component.Resources {
		if f.desc.Component.Resources[i].ToIdentity().Equal(id) {
			return &f.desc.Component.Resources[i], nil
		}
	}
	return nil, ocm.ErrNotFound
}

func (f *fakeCV) ResourcesByType(typ string) []*descriptorruntime.Resource {
	var out []*descriptorruntime.Resource
	for i := range f.desc.Component.Resources {
		if f.desc.Component.Resources[i].Type == typ {
			out = append(out, &f.desc.Component.Resources[i])
		}
	}
	return out
}

func (f *fakeCV) Download(_ context.Context, res *descriptorruntime.Resource) (blob.ReadOnlyBlob, error) {
	content, ok := f.contents[res.Name]
	if !ok {
		return nil, ocm.ErrNotFound
	}
	return stringBlob(content), nil
}

type stringBlob string

func (s stringBlob) ReadCloser() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(s))), nil
}

// fakeResolver serves one component version, or an error.
type fakeResolver struct {
	cv  ocm.ComponentVersion
	err error
	// gotURL records the repository the resolver was called with.
	gotURL string
}

func (f *fakeResolver) Resolve(_ context.Context, repo ocm.OCMRepositorySpec, _, _ string) (ocm.ComponentVersion, error) {
	f.gotURL = repo.URL
	if f.err != nil {
		return nil, f.err
	}
	return f.cv, nil
}
