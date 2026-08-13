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

package ocmmodulesetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// applyContent applies the manifests declared for one workspace into it.
func (r *reconciler) applyContent(ctx context.Context, ws ctrlruntimeclient.Client, workspace pmdeployv1alpha1.OCMModuleSetupWorkspace) error {
	if len(workspace.Content) == 0 {
		return nil
	}

	resolved, err := r.resolveModule(ctx)
	if err != nil {
		return err
	}

	for _, ref := range workspace.Content {
		raw, err := r.opts.DownloadResource(ctx, resolved, ref.Name)
		if err != nil {
			return err
		}
		objs, err := decode(raw)
		if err != nil {
			return fmt.Errorf("decoding %q: %w", ref.Name, err)
		}
		for _, o := range objs {
			if err := r.opts.ApplyObject(ctx, ws, o); err != nil {
				return fmt.Errorf("applying %q into %s: %w", ref.Name, workspace.Path, err)
			}
		}
	}
	return nil
}

// resolveModule fetches the component version the setup belongs to.
func (r *reconciler) resolveModule(ctx context.Context) (*ocmmodule.Resolved, error) {
	key := ctrlruntimeclient.ObjectKey{Namespace: r.setup.Namespace, Name: r.setup.Spec.OCMModuleRef.Name}
	mod, err := r.opts.GetModule(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("getting OCMModule %q: %w", key.Name, err)
	}

	resolved, err := r.opts.ResolveModule(ctx, mod, &r.pm.Spec.OCM)
	if err != nil {
		return nil, fmt.Errorf("resolving module %q: %w", mod.Name, err)
	}
	return resolved, nil
}

// downloadResource fetches one OCM resource of the module's component version.
func downloadResource(ctx context.Context, resolved *ocmmodule.Resolved, name string) ([]byte, error) {
	res, err := ocmmodule.Resource(resolved.CV, name)
	if err != nil {
		return nil, err
	}
	blob, err := resolved.CV.Download(ctx, res)
	if err != nil {
		return nil, fmt.Errorf("downloading %q: %w", name, err)
	}
	rc, err := blob.ReadCloser()
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", name, err)
	}
	defer rc.Close() //nolint:errcheck // read-only
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", name, err)
	}
	return raw, nil
}

// decode splits a multi-document manifest. Kept free of the client so the
// document handling is testable without kcp.
func decode(raw []byte) ([]*unstructured.Unstructured, error) {
	reader := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var out []*unstructured.Unstructured
	for {
		fields := map[string]any{}
		if err := reader.Decode(&fields); err != nil {
			if errors.Is(err, io.EOF) {
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
