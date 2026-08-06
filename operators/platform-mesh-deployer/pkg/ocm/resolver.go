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
	"os"
	"strings"

	"ocm.software/open-component-model/bindings/go/blob"
	"ocm.software/open-component-model/bindings/go/ctf"
	descriptorruntime "ocm.software/open-component-model/bindings/go/descriptor/runtime"
	"ocm.software/open-component-model/bindings/go/oci"
	ocictf "ocm.software/open-component-model/bindings/go/oci/ctf"
	ociurl "ocm.software/open-component-model/bindings/go/oci/resolver/url"
	"ocm.software/open-component-model/bindings/go/repository"
	"ocm.software/open-component-model/bindings/go/runtime"
)

// New returns a resolver for OCI-registry OCM repositories.
func New() Resolver {
	return ociResolver{}
}

type ociResolver struct{}

func (ociResolver) Resolve(ctx context.Context, repo OCMRepositorySpec, component, version string) (ComponentVersion, error) {
	base, plainHTTP := ParseRepositoryURL(repo.URL)
	opts := []ociurl.Option{ociurl.WithBaseURL(base)}
	if plainHTTP {
		opts = append(opts, ociurl.WithPlainHTTP(true))
	}
	res, err := ociurl.New(opts...)
	if err != nil {
		return nil, err
	}
	// TODO(ntnn): apply repo.Creds to the remote client for authenticated registries.
	r, err := oci.NewRepository(oci.WithResolver(res))
	if err != nil {
		return nil, err
	}
	return resolve(ctx, r, component, version)
}

// ParseRepositoryURL splits a repository URL into the registry reference and whether to talk plain HTTP.
// The scheme has to go: it ends up verbatim in the OCI reference, which then fails to parse.
func ParseRepositoryURL(raw string) (string, bool) {
	for _, scheme := range []string{"http://", "https://", "oci://"} {
		if rest, ok := strings.CutPrefix(raw, scheme); ok {
			return rest, scheme == "http://"
		}
	}
	return raw, false
}

func resolve(ctx context.Context, repo repository.ComponentVersionRepository, component, version string) (ComponentVersion, error) {
	desc, err := repo.GetComponentVersion(ctx, component, version)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &componentVersion{repo: repo, desc: desc, component: component, version: version}, nil
}

// NewCTFResolver returns a resolver targeting an on-disk CTF archive.
func NewCTFResolver(path string) (Resolver, error) {
	archive, _, err := ctf.OpenCTFByFileExtension(
		context.Background(),
		ctf.OpenCTFOptions{
			Path: path,
			Flag: os.O_RDONLY,
		},
	)
	if err != nil {
		return nil, err
	}
	r, err := oci.NewRepository(ocictf.WithCTF(ocictf.NewFromCTF(archive)))
	if err != nil {
		return nil, err
	}
	return ctfResolver{repo: r}, nil
}

type ctfResolver struct {
	repo repository.ComponentVersionRepository
}

func (c ctfResolver) Resolve(ctx context.Context, _ OCMRepositorySpec, component, version string) (ComponentVersion, error) {
	return resolve(ctx, c.repo, component, version)
}

type componentVersion struct {
	repo               repository.ComponentVersionRepository
	desc               *descriptorruntime.Descriptor
	component, version string
}

func (cv *componentVersion) Descriptor() *descriptorruntime.Descriptor {
	return cv.desc
}

func (cv *componentVersion) Resource(id runtime.Identity) (*descriptorruntime.Resource, error) {
	for i := range cv.desc.Component.Resources {
		r := &cv.desc.Component.Resources[i]
		if r.ToIdentity().Equal(id) {
			return r, nil
		}
	}
	return nil, ErrNotFound
}

func (cv *componentVersion) ResourcesByType(typ string) []*descriptorruntime.Resource {
	var out []*descriptorruntime.Resource
	for i := range cv.desc.Component.Resources {
		if cv.desc.Component.Resources[i].Type == typ {
			out = append(out, &cv.desc.Component.Resources[i])
		}
	}
	return out
}

// Download returns the content of a local resource.
func (cv *componentVersion) Download(ctx context.Context, res *descriptorruntime.Resource) (blob.ReadOnlyBlob, error) {
	b, _, err := cv.repo.GetLocalResource(ctx, cv.component, cv.version, res.ToIdentity())
	return b, err
}
