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
	"sort"
	"strings"
	"time"

	"go.platform-mesh.io/tenancy-operator/deploy"
	"go.platform-mesh.io/tenancy-operator/pkg/paths"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
)

// Placeholders the embedded manifests carry. Neither value can be committed:
//
//	exportsRef   an APIBinding and a WorkspaceType reference their target, and
//	             where the install is rooted is a flag.
//	identityHash kcp rejects a permission claim on a non-built-in type without
//	             one, and it is per-kcp-install.
const (
	placeholderExportsRef   = "__EXPORTS_REF__"
	placeholderIdentityHash = "__TENANCY_KCP_IO_IDENTITY_HASH__"

	// placeholderExportsPath is the exports workspace as a PATH, where
	// placeholderExportsRef is the same workspace as a logical cluster ID.
	//
	// Both are needed because kcp compares the two forms in different places. A
	// WorkspaceType reference resolves either way, but limitAllowedParents is
	// matched as a string against the parent's own spec.type.path — which kcp
	// always writes in canonical path form. A cluster ID there produces
	//
	//	only allows [<cluster>:tenant] parent workspaces, but parent type
	//	<path>:tenant only implements [<path>:tenant root:universal]
	//
	// at child-workspace creation, long after the install looked successful.
	placeholderExportsPath = "__EXPORTS_PATH__"
)

// The kcp types this installer touches directly.
var (
	gvkWorkspace      = schema.GroupVersionKind{Group: "tenancy.kcp.io", Version: "v1alpha1", Kind: "Workspace"}
	gvkAPIExport      = schema.GroupVersionKind{Group: "apis.kcp.io", Version: "v1alpha2", Kind: "APIExport"}
	gvkLogicalCluster = schema.GroupVersionKind{Group: "core.kcp.io", Version: "v1alpha1", Kind: "LogicalCluster"}
)

// tenancyKcpExport is the export that owns `workspaces`. Our provisioner claim
// must name its identity hash, which pins the claim to THAT export's schema
// rather than to any other API sharing the group/resource name.
const tenancyKcpExport = "tenancy.kcp.io"

// Options configures an install.
type Options struct {
	// Layout is the resolved workspace tree. It must match what the operator
	// runs with, which is why both read it from the same --paths-* flags.
	Layout paths.Layout

	// WorkspaceReadyTimeout bounds the wait for each created workspace.
	WorkspaceReadyTimeout time.Duration
}

// Logf is a minimal sink so the caller owns the logging style.
type Logf func(format string, args ...any)

// Installer applies the tenancy tree.
type Installer struct {
	clients *clients
	opts    Options
	logf    Logf
}

// New returns an Installer using cfg, which must carry kcp admin credentials.
// cfg may point at any workspace; every URL is recomputed from the layout.
func New(cfg *rest.Config, opts Options, logf Logf) *Installer {
	if opts.WorkspaceReadyTimeout == 0 {
		opts.WorkspaceReadyTimeout = 2 * time.Minute
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Installer{clients: newClients(cfg), opts: opts, logf: logf}
}

// Run installs the tree. It is idempotent: every step converges, so it is safe to
// run on every start of the init container and on every upgrade.
//
// The order is load-bearing:
//   - workspaces first, because everything else is written into one;
//   - schemas before exports, because an APIExport referencing a missing
//     APIResourceSchema is rejected;
//   - bind RBAC before slices and bindings, because kcp gates both on a `bind`
//     verb evaluated in the export's own workspace — a separate admission check
//     that an admin credential does not satisfy.
func (i *Installer) Run(ctx context.Context) error {
	l := i.opts.Layout
	i.logf("layout root=%s fleet=%s directory=%s exports=%s", l.Root, l.TenantFleetRoot, l.Directory, l.Exports)

	// Probe kcp before doing anything, so an unreachable or unauthorized server is
	// reported as itself. Without this the first symptom is a RESTMapper miss —
	// "no mapping for tenancy.kcp.io/v1alpha1, Kind=Workspace" — which reads like a
	// missing CRD and sends the reader looking in entirely the wrong place.
	if err := i.preflight(ctx); err != nil {
		return err
	}

	if err := i.ensureWorkspaces(ctx); err != nil {
		return err
	}

	identityHash, err := i.tenancyIdentityHash(ctx)
	if err != nil {
		return err
	}
	i.logf("%s identityHash %s", tenancyKcpExport, identityHash)

	exportsRef, err := i.exportsClusterRef(ctx)
	if err != nil {
		return err
	}
	i.logf("exports workspace %s is cluster %s", l.Exports, exportsRef)

	rep := strings.NewReplacer(
		placeholderExportsRef, exportsRef,
		placeholderExportsPath, l.Exports,
		placeholderIdentityHash, identityHash,
	)

	if err := i.applyAPISurface(ctx, rep); err != nil {
		return err
	}
	if err := i.applyBindings(ctx, rep); err != nil {
		return err
	}

	i.logf("tenancy tree ready")
	return nil
}

// preflight confirms the credential can actually talk to kcp's root workspace.
func (i *Installer) preflight(ctx context.Context) error {
	wc, err := i.clients.for_("root")
	if err != nil {
		return err
	}
	groups, err := wc.discovery.ServerGroups()
	if err != nil {
		// %T as well as %v: discovery collapses several very different failures
		// (TLS, 401/403, unparseable body) into terse messages, and the type is
		// often the only thing that says which one happened.
		return fmt.Errorf("cannot reach kcp at %s (as user of %s): %T: %v",
			BaseURL(i.clients.base.Host), i.clients.credentialSummary(), err, err)
	}
	for _, g := range groups.Groups {
		if g.Name == gvkWorkspace.Group {
			return nil
		}
	}
	return fmt.Errorf("kcp at %s does not serve %s in the root workspace — is this a kcp front-proxy?",
		BaseURL(i.clients.base.Host), gvkWorkspace.Group)
}

// ensureWorkspaces creates every missing ancestor of each configured path.
//
// The first segment is kcp's own root, which always exists; everything below it
// is ours to make. This is what lets the install root be an arbitrary sub-tree
// (root:tenancy, root:acme:platform, ...) rather than a shape the installer has
// to know.
func (i *Installer) ensureWorkspaces(ctx context.Context) error {
	l := i.opts.Layout
	for _, p := range []string{l.Exports, l.Directory, l.TenantFleetRoot} {
		segments := strings.Split(p, paths.Separator)
		acc := segments[0]
		for _, seg := range segments[1:] {
			if err := i.ensureWorkspace(ctx, acc, seg); err != nil {
				return err
			}
			acc = acc + paths.Separator + seg
		}
	}
	return nil
}

func (i *Installer) ensureWorkspace(ctx context.Context, parent, name string) error {
	if _, err := i.clients.get(ctx, parent, gvkWorkspace, name); err == nil {
		i.logf("  %s:%s exists", parent, name)
		return i.waitWorkspaceReady(ctx, parent, name)
	}

	i.logf("  creating %s:%s", parent, name)
	ws := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvkWorkspace.GroupVersion().String(),
		"kind":       gvkWorkspace.Kind,
		"metadata":   map[string]any{"name": name},
		// `universal` for the platform's own scaffolding. The tenant tiers use the
		// tenant/workspace types this installer registers further down.
		"spec": map[string]any{
			"type": map[string]any{"name": "universal", "path": "root"},
		},
	}}
	if err := i.clients.apply(ctx, parent, ws); err != nil {
		return err
	}
	return i.waitWorkspaceReady(ctx, parent, name)
}

// waitWorkspaceReady blocks until the workspace is serving. Workspace creation is
// asynchronous with no rollback, so writing into one before it is Ready fails in
// ways that look like permission problems.
func (i *Installer) waitWorkspaceReady(ctx context.Context, parent, name string) error {
	err := wait.PollUntilContextTimeout(ctx, time.Second, i.opts.WorkspaceReadyTimeout, true,
		func(ctx context.Context) (bool, error) {
			ws, err := i.clients.get(ctx, parent, gvkWorkspace, name)
			if err != nil {
				// Keep polling rather than failing: a workspace created moments
				// ago is expected to 404 until it is scheduled, and the enclosing
				// PollUntilContextTimeout is what bounds the wait.
				//nolint:nilerr // deliberate: not-yet-there is a wait, not a failure
				return false, nil
			}
			phase, _, _ := unstructured.NestedString(ws.Object, "status", "phase")
			return phase == "Ready", nil
		})
	if err != nil {
		return fmt.Errorf("workspace %s:%s did not become Ready: %w", parent, name, err)
	}
	return nil
}

// tenancyIdentityHash reads the identity of kcp's own tenancy export from root.
func (i *Installer) tenancyIdentityHash(ctx context.Context) (string, error) {
	export, err := i.clients.get(ctx, "root", gvkAPIExport, tenancyKcpExport)
	if err != nil {
		return "", fmt.Errorf("reading APIExport %s in root: %w", tenancyKcpExport, err)
	}
	hash, found, err := unstructured.NestedString(export.Object, "status", "identityHash")
	if err != nil || !found || hash == "" {
		return "", fmt.Errorf("APIExport %s in root has no status.identityHash yet", tenancyKcpExport)
	}
	return hash, nil
}

// exportsClusterRef returns the exports workspace's LOGICAL CLUSTER ID.
//
// References use the ID rather than the human-readable path. kcp accepts either —
// a reference field is a logicalcluster.Path, documented as "a canonical path or a
// cluster name" — but they resolve through different index keys, and the by-path
// key is unusable on a shard whose APIExport index has duplicate entries (which
// surfaces as kcp's unified "no permission to bind to export", indistinguishable
// from a real denial).
//
// The ID is also the better reference on its own terms: it is what the object is,
// it cannot be ambiguous between two installs sharing a kcp, and it survives a
// workspace being renamed or moved.
func (i *Installer) exportsClusterRef(ctx context.Context) (string, error) {
	lc, err := i.clients.get(ctx, i.opts.Layout.Exports, gvkLogicalCluster, "cluster")
	if err != nil {
		return "", fmt.Errorf("reading LogicalCluster of %s: %w", i.opts.Layout.Exports, err)
	}
	ref := lc.GetAnnotations()["kcp.io/cluster"]
	if ref == "" {
		return "", fmt.Errorf("LogicalCluster of %s carries no kcp.io/cluster annotation", i.opts.Layout.Exports)
	}
	return ref, nil
}

// applyAPISurface writes everything that lives in the exports workspace — the only
// workspace that DEFINES APIs, and which stores no objects at all.
func (i *Installer) applyAPISurface(ctx context.Context, rep *strings.Replacer) error {
	exports := i.opts.Layout.Exports

	// Schemas before exports: an APIExport referencing a schema that does not
	// exist yet is rejected.
	schemas, err := i.load(deploy.KcpResourcesDir, rep, func(n string) bool { return strings.HasPrefix(n, "apiresourceschema-") })
	if err != nil {
		return err
	}
	for _, o := range schemas {
		// CREATE, not apply: an APIResourceSchema is immutable in kcp. Changing an
		// API means publishing a NEW schema (apigen names them
		// v<date>-<githash>.<resource>.<group>) and pointing the export at it —
		// never editing one in place.
		//
		// So an existing name is success, not a conflict. The one case this hides
		// is a schema whose name did not change but whose content did, which only
		// happens with uncommitted local edits, since the name carries the commit
		// hash. That is a rebuild-and-commit problem, not an install problem, and
		// it is called out rather than silently swallowed.
		if err := i.clients.create(ctx, exports, o); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			if isImmutableFieldError(err) {
				return fmt.Errorf(
					"APIResourceSchema %s already exists with different content: schemas are immutable, so the name must change with the content. "+
						"apigen derives it from the last commit, so commit the API change and re-run `task generate-tenancy-operator`: %w",
					o.GetName(), err)
			}
			return err
		}
	}
	i.logf("  ensured %d APIResourceSchemas", len(schemas))

	exportObjs, err := i.load(deploy.KcpResourcesDir, rep, func(n string) bool { return strings.HasPrefix(n, "apiexport-") })
	if err != nil {
		return err
	}
	for _, o := range exportObjs {
		if err := i.clients.apply(ctx, exports, o); err != nil {
			return err
		}
	}
	i.logf("  applied %d APIExports", len(exportObjs))

	// Bind RBAC BEFORE the slices and the bindings below: kcp gates both on a
	// `bind` verb against the APIExport, evaluated here in the export's own
	// workspace, and it is a separate admission check that admin does not satisfy.
	if err := i.applyFile(ctx, exports, deploy.KcpBootstrapDir+"/rbac-bind.yaml", rep, false); err != nil {
		return err
	}
	i.logf("  applied bind RBAC")

	if err := i.applyFile(ctx, exports, deploy.KcpBootstrapDir+"/apiexportendpointslices.yaml", rep, true); err != nil {
		return err
	}
	i.logf("  applied APIExportEndpointSlices")

	if err := i.applyFile(ctx, exports, deploy.KcpBootstrapDir+"/workspacetypes.yaml", rep, false); err != nil {
		return err
	}
	i.logf("  applied WorkspaceTypes")

	return nil
}

// applyBindings writes the two install-time APIBindings.
//
// Everything else is created by a controller or a WorkspaceType initializer.
// These two cannot be: one makes the directory able to STORE the global objects,
// the other makes the fleet root able to have Tenants CREATED in it — and
// without the second there is no way to make a Tenant at all.
//
// Which document goes where is decided by the binding's own name rather than by
// document order, so reordering the file cannot silently swap them.
func (i *Installer) applyBindings(ctx context.Context, rep *strings.Replacer) error {
	objs, err := i.load(deploy.KcpBootstrapDir, rep, func(n string) bool { return n == "apibindings.yaml" })
	if err != nil {
		return err
	}

	target := map[string]string{
		"tenancy-platform":    i.opts.Layout.Directory,
		"tenancy-provisioner": i.opts.Layout.TenantFleetRoot,
	}
	for _, o := range objs {
		ws, ok := target[o.GetName()]
		if !ok {
			return fmt.Errorf("APIBinding %q has no configured target workspace", o.GetName())
		}
		if err := i.clients.apply(ctx, ws, o); err != nil {
			return err
		}
		i.logf("  %s: %s", ws, o.GetName())
	}
	return nil
}

func (i *Installer) applyFile(ctx context.Context, workspacePath, name string, rep *strings.Replacer, recreateOnImmutable bool) error {
	data, err := deploy.KcpAssets.ReadFile(name)
	if err != nil {
		return fmt.Errorf("reading embedded %s: %w", name, err)
	}
	objs, err := decode([]byte(rep.Replace(string(data))))
	if err != nil {
		return fmt.Errorf("decoding %s: %w", name, err)
	}
	for _, o := range objs {
		apply := i.clients.apply
		if recreateOnImmutable {
			apply = i.clients.applyOrRecreate
		}
		if err := apply(ctx, workspacePath, o); err != nil {
			return err
		}
	}
	return nil
}

// load reads every matching file in a directory, substitutes placeholders and
// decodes. Files are sorted so a run is reproducible.
func (i *Installer) load(dir string, rep *strings.Replacer, match func(name string) bool) ([]*unstructured.Unstructured, error) {
	entries, err := deploy.KcpAssets.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading embedded %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && match(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var out []*unstructured.Unstructured
	for _, n := range names {
		data, err := deploy.KcpAssets.ReadFile(dir + "/" + n)
		if err != nil {
			return nil, fmt.Errorf("reading embedded %s/%s: %w", dir, n, err)
		}
		rendered := rep.Replace(string(data))
		// A placeholder reaching kcp would apply cleanly and fail much later — as
		// a binding pointing at a workspace literally named __EXPORTS_REF__.
		if strings.Contains(rendered, "__") && (strings.Contains(rendered, placeholderExportsRef) || strings.Contains(rendered, placeholderExportsPath) || strings.Contains(rendered, placeholderIdentityHash)) {
			return nil, fmt.Errorf("%s/%s still contains an unsubstituted placeholder", dir, n)
		}
		objs, err := decode([]byte(rendered))
		if err != nil {
			return nil, fmt.Errorf("decoding %s/%s: %w", dir, n, err)
		}
		out = append(out, objs...)
	}
	return out, nil
}
