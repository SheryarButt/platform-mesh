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

package bootstrap

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	k8syaml "sigs.k8s.io/yaml"
)

// FieldManager identifies this installer's server-side-apply ownership. Using one
// stable name means a re-run updates the fields it owns instead of conflicting
// with itself.
const FieldManager = "tenancy-operator-init"

// clusterSuffix matches the /clusters/<path> segment kcp URLs carry, so a base
// config aimed at any workspace can be re-pointed at another.
var clusterSuffix = regexp.MustCompile(`/clusters/[^/]+/?$`)

// BaseURL strips any /clusters/<path> suffix from a kcp server URL, leaving the
// front-proxy address every workspace hangs off.
//
// Exported because the operator needs the same operation: it is handed one
// kubeconfig and computes its own workspace URL from --paths-*, so a single
// credential serves both the installer and the controller without either needing
// a pre-scoped kubeconfig baked for it.
func BaseURL(server string) string {
	return strings.TrimSuffix(clusterSuffix.ReplaceAllString(server, ""), "/")
}

// WorkspaceConfig returns a copy of cfg pointed at one kcp workspace.
func WorkspaceConfig(cfg *rest.Config, workspacePath string) *rest.Config {
	out := rest.CopyConfig(cfg)
	out.Host = fmt.Sprintf("%s/clusters/%s", BaseURL(cfg.Host), workspacePath)
	return out
}

// Retarget points cfg at a different kcp address, keeping certificate
// verification honest.
//
// server replaces host and port; serverName is what TLS is checked against, so a
// pod can dial the front-proxy Service directly while the certificate is still
// verified as the public hostname. Both are optional.
//
// kcp's published hostnames are frequently unroutable from inside the cluster, and
// a pod that can resolve a name but not connect to it fails in a way that looks
// like a broken credential rather than a broken route.
func Retarget(cfg *rest.Config, server, serverName string) *rest.Config {
	out := rest.CopyConfig(cfg)
	if server != "" {
		out.Host = BaseURL(server)
	}
	if serverName != "" {
		out.ServerName = serverName
	}
	return out
}

// credentialSummary describes which credential the config carries, without
// printing any of it. A kubeconfig silently falling back to the pod's
// ServiceAccount is a common and confusing failure — the address looks right and
// the identity is wrong — so name what is actually being presented.
func (c *clients) credentialSummary() string {
	switch {
	case len(c.base.CertData) > 0 || c.base.CertFile != "":
		return "client certificate"
	case c.base.BearerToken != "" || c.base.BearerTokenFile != "":
		return "bearer token"
	case c.base.ExecProvider != nil:
		return "exec credential plugin"
	default:
		return "no credential (anonymous)"
	}
}

// workspaceClient is a dynamic client plus RESTMapper scoped to one workspace.
// kcp serves discovery per logical cluster, so the mapper cannot be shared.
type workspaceClient struct {
	dyn       dynamic.Interface
	mapper    meta.RESTMapper
	discovery discovery.DiscoveryInterface
}

// clients lazily builds and caches one workspaceClient per workspace path.
type clients struct {
	base *rest.Config

	mu     sync.Mutex
	byPath map[string]*workspaceClient
}

func newClients(cfg *rest.Config) *clients {
	return &clients{base: cfg, byPath: map[string]*workspaceClient{}}
}

func (c *clients) for_(workspacePath string) (*workspaceClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if wc, ok := c.byPath[workspacePath]; ok {
		return wc, nil
	}

	cfg := WorkspaceConfig(c.base, workspacePath)

	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("discovery client for %s: %w", workspacePath, err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client for %s: %w", workspacePath, err)
	}

	wc := &workspaceClient{
		dyn:       dyn,
		discovery: dc,
		// Deferred discovery: a workspace created moments ago may not be serving
		// its API surface yet, and an eager mapper would cache the gap.
		mapper: restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc)),
	}
	c.byPath[workspacePath] = wc
	return wc, nil
}

// resourceFor resolves an object's GVK to the resource interface for its
// workspace, invalidating the discovery cache once on a miss — which is the
// normal case right after a workspace or an APIExport is created.
func (c *clients) resourceFor(workspacePath string, obj *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	wc, err := c.for_(workspacePath)
	if err != nil {
		return nil, err
	}

	gvk := obj.GroupVersionKind()
	mapping, err := wc.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		if m, ok := wc.mapper.(meta.ResettableRESTMapper); ok {
			m.Reset()
			mapping, err = wc.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		}
		if err != nil {
			return nil, fmt.Errorf("no mapping for %s in %s: %w", gvk, workspacePath, err)
		}
	}

	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = "default"
		}
		return wc.dyn.Resource(mapping.Resource).Namespace(ns), nil
	}
	return wc.dyn.Resource(mapping.Resource), nil
}

// apply server-side-applies one object into a workspace.
//
// Server-side apply rather than get-then-create-or-update: it is a single call,
// it is idempotent by construction, and re-running the installer converges
// instead of fighting whatever it wrote last time.
func (c *clients) apply(ctx context.Context, workspacePath string, obj *unstructured.Unstructured) error {
	ri, err := c.resourceFor(workspacePath, obj)
	if err != nil {
		return err
	}

	data, err := obj.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling %s/%s: %w", obj.GetKind(), obj.GetName(), err)
	}

	_, err = ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: FieldManager,
		Force:        ptr(true),
	})
	if err != nil {
		return fmt.Errorf("applying %s/%s in %s: %w", obj.GetKind(), obj.GetName(), workspacePath, err)
	}
	return nil
}

// create creates obj, leaving an existing object untouched. Used for objects that
// are immutable by design, where "already there" is the success case.
func (c *clients) create(ctx context.Context, workspacePath string, obj *unstructured.Unstructured) error {
	ri, err := c.resourceFor(workspacePath, obj)
	if err != nil {
		return err
	}
	if _, err := ri.Create(ctx, obj, metav1.CreateOptions{FieldManager: FieldManager}); err != nil {
		return fmt.Errorf("creating %s/%s in %s: %w", obj.GetKind(), obj.GetName(), workspacePath, err)
	}
	return nil
}

// applyOrRecreate applies obj, and on rejection of an immutable field deletes and
// re-applies it.
//
// APIExportEndpointSlice.spec.export is immutable, so a slice left over from an
// install at a different root cannot be patched onto the new one. Recreating is
// the only convergent option; the alternative is a slice quietly publishing
// another install's export, which the operator would happily watch.
func (c *clients) applyOrRecreate(ctx context.Context, workspacePath string, obj *unstructured.Unstructured) error {
	err := c.apply(ctx, workspacePath, obj)
	if err == nil || !isImmutableFieldError(err) {
		return err
	}

	ri, rerr := c.resourceFor(workspacePath, obj)
	if rerr != nil {
		return rerr
	}
	if derr := ri.Delete(ctx, obj.GetName(), metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
		return fmt.Errorf("deleting %s/%s before recreate: %w", obj.GetKind(), obj.GetName(), derr)
	}
	return c.apply(ctx, workspacePath, obj)
}

func isImmutableFieldError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "must not be changed") || strings.Contains(s, "immutable")
}

// get reads one object from a workspace.
func (c *clients) get(ctx context.Context, workspacePath string, gvk schema.GroupVersionKind, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	ri, err := c.resourceFor(workspacePath, obj)
	if err != nil {
		return nil, err
	}
	return ri.Get(ctx, name, metav1.GetOptions{})
}

// list reads every object of one kind from a workspace.
func (c *clients) list(ctx context.Context, workspacePath string, gvk schema.GroupVersionKind) ([]unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	ri, err := c.resourceFor(workspacePath, obj)
	if err != nil {
		return nil, err
	}
	l, err := ri.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return l.Items, nil
}

// decode splits a multi-document YAML stream into objects, skipping empty
// documents (a trailing `---` or a comment-only block).
func decode(data []byte) ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured
	for i, doc := range strings.Split(string(data), "\n---") {
		if strings.TrimSpace(stripComments(doc)) == "" {
			continue
		}
		m := map[string]any{}
		if err := k8syaml.Unmarshal([]byte(doc), &m); err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		if len(m) == 0 {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: m})
	}
	return out, nil
}

func stripComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

func ptr[T any](v T) *T { return &v }
