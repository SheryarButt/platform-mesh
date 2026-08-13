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
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/modfile"
)

const (
	ctfModule = "ocm.software/open-component-model/bindings/go/ctf"

	// fixture01 is fixture used in upstream
	fixture01Component = "github.com/acme.org/helloworld"
	fixture01Version   = "1.0.0"
	fixture01Path      = "bindings/go/ctf/testdata/compatibility/01/transport-archive.tar.gz"
)

var pseudoVersion = regexp.MustCompile(`-\d{14}-([0-9a-f]{12})$`)

// ctfBindingVersion returns the version of the ctf binding module.
func ctfBindingVersion(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)

	for {
		gomod := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(gomod); err == nil {
			f, err := modfile.Parse(gomod, data, nil)
			require.NoError(t, err)
			for _, r := range f.Replace {
				if r.Old.Path == ctfModule {
					return r.New.Version
				}
			}
			for _, r := range f.Require {
				if r.Mod.Path == ctfModule {
					return r.Mod.Version
				}
			}
			t.Fatalf("module %s not found in %s", ctfModule, gomod)
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir, "go.mod not found above the test working directory")
		dir = parent
	}
}

// gitRef maps a module version to the upstream git ref that carries the fixture.
func gitRef(version string) string {
	if m := pseudoVersion.FindStringSubmatch(version); m != nil {
		return m[1] // pseudo-version → commit SHA
	}
	return "bindings/go/ctf/" + version // tagged submodule
}

// fetchFixture downloads and caches the CTF archive.
// Offline with no cache is a hard failure.
func fetchFixture(t *testing.T) string {
	t.Helper()

	version := ctfBindingVersion(t)
	cacheDir := filepath.Join("testdata", ".cache", version)
	dst := filepath.Join(cacheDir, "transport-archive.tar.gz")
	if _, err := os.Stat(dst); err == nil {
		return dst
	}

	url := fmt.Sprintf(
		"https://raw.githubusercontent.com/open-component-model/open-component-model/%s/%s",
		gitRef(version), fixture01Path,
	)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))

	resp, err := http.Get(url) //nolint:gosec // fixed upstream host, version-pinned path
	require.NoErrorf(t, err, "fetch CTF fixture %s", url)
	defer func() { _ = resp.Body.Close() }()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "fetch CTF fixture %s", url)

	f, err := os.Create(dst)
	require.NoError(t, err)
	_, err = io.Copy(f, resp.Body)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return dst
}

func fixtureResolver(t *testing.T) Resolver {
	t.Helper()

	r, err := NewCTFResolver(fetchFixture(t))
	require.NoError(t, err)
	return r
}

func TestResolve(t *testing.T) {
	cv, err := fixtureResolver(t).Resolve(t.Context(), OCMRepositorySpec{}, fixture01Component, fixture01Version)
	require.NoError(t, err)

	desc := cv.Descriptor()
	require.NotNil(t, desc)
	require.Equal(t, fixture01Component, desc.Component.Name)
	require.Equal(t, fixture01Version, desc.Component.Version)

	res := cv.ResourcesByType("something")
	require.Len(t, res, 1)
	require.Equal(t, "resource", res[0].Name)

	byID, err := cv.Resource(res[0].ToIdentity())
	require.NoError(t, err)
	require.Equal(t, res[0].Name, byID.Name)

	b, err := cv.Download(t.Context(), byID)
	require.NoError(t, err)
	rc, err := b.ReadCloser()
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	content, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, "test", string(content))
}

func TestResolveNotFound(t *testing.T) {
	r := fixtureResolver(t)

	_, err := r.Resolve(t.Context(), OCMRepositorySpec{}, fixture01Component, "9.9.9")
	require.ErrorIs(t, err, ErrNotFound)

	_, err = r.Resolve(t.Context(), OCMRepositorySpec{}, "github.com/acme.org/nope", fixture01Version)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestResourceNotFound(t *testing.T) {
	cv, err := fixtureResolver(t).Resolve(t.Context(), OCMRepositorySpec{}, fixture01Component, fixture01Version)
	require.NoError(t, err)

	_, err = cv.Resource(map[string]string{"name": "missing"})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestParseRepositoryURL(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantBase      string
		wantPlainHTTP bool
	}{
		// The scheme must not survive: it ends up verbatim in the OCI
		// reference, which then fails to parse.
		{name: "http selects plain HTTP", raw: "http://registry:5000", wantBase: "registry:5000", wantPlainHTTP: true},
		{name: "https", raw: "https://ghcr.io/platform-mesh", wantBase: "ghcr.io/platform-mesh"},
		{name: "oci", raw: "oci://ghcr.io/platform-mesh", wantBase: "ghcr.io/platform-mesh"},
		{name: "no scheme", raw: "ghcr.io/platform-mesh", wantBase: "ghcr.io/platform-mesh"},
		{name: "empty", raw: "", wantBase: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, plainHTTP := ParseRepositoryURL(tt.raw)
			assert.Equal(t, tt.wantBase, base)
			assert.Equal(t, tt.wantPlainHTTP, plainHTTP)
		})
	}
}
