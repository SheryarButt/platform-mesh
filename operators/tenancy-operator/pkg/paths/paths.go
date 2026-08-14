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

// Package paths is the single, dependency-free source of the workspace layout.
//
// Every component that computes a path — the virtual workspace, the controller,
// the bootstrapper — reads it from here, so they cannot drift. Nothing else may
// build a tenancy workspace path by string concatenation.
//
// Every root here is configuration, not a constant:
//
//   - The platform must not claim `root:`. Installing under `root:acme:platform`
//     has to be a supported layout, not a fork.
//   - Two installs on one kcp (dev and staging) need disjoint subtrees, and
//     disjoint export workspaces — an APIBinding references its export BY PATH.
//
// What is not configurable is the shape: a Tenant is always a direct child
// of the fleet root, and a Workspace always a direct child of a Tenant.
package paths

import (
	"fmt"
	"strings"
)

// Separator joins kcp workspace path segments.
const Separator = ":"

// Defaults for the layout, relative to the install root.
const (
	// DefaultRoot is the install root everything else hangs off.
	DefaultRoot = "root"

	// SegmentTenants is the fleet root's segment under the install root. Its
	// children are Tenants.
	SegmentTenants = "tenants"

	// SegmentSystem is the platform's own subtree.
	SegmentSystem = "system"

	// SegmentDirectory holds User / Tenant / UserMembershipIndex objects.
	//
	// It is a directory — the record of who exists, what tenants exist, and
	// what each person belongs to. An earlier draft called it `system:tenants`
	// for symmetry with the `tenants` fleet; the symmetry read well and confused
	// everyone who met it.
	SegmentDirectory = "directory"

	// SegmentControllers is the only workspace that DEFINES APIs. It holds the
	// APIExports and their APIResourceSchemas and stores no objects at all.
	SegmentControllers = "controllers"

	// SegmentProviders holds Provider / ProvidersMetadata objects.
	SegmentProviders = "providers"
)

// DefaultVirtualWorkspacePrefix is where the tenancy virtual workspace singleton
// mounts. It must be namespaced per install, or two installs collide here and
// each one's singleton answers for both.
const DefaultVirtualWorkspacePrefix = "/services/tenancy"

// Layout is the resolved set of workspace paths for one install. Build it with
// [New] and pass it around; do not assemble paths anywhere else.
type Layout struct {
	// Root is the install root, e.g. "root" or "root:acme:platform".
	Root string

	// TenantFleetRoot is where Tenant workspaces are created, and where the
	// tenancy-provisioner export is bound so they can be created without admin.
	TenantFleetRoot string

	// Directory is where User / Tenant / UserMembershipIndex live.
	Directory string

	// Exports is where the four APIExports are defined, and what every APIBinding
	// references by path.
	Exports string

	// Providers is where Provider / ProvidersMetadata objects live.
	Providers string

	// VirtualWorkspacePrefix is the tenancy singleton's mount point.
	VirtualWorkspacePrefix string
}

// Options are the overridable roots. An empty field takes the default derived
// from Root, so a sub-tree install is a configuration change and not a code one.
type Options struct {
	Root                   string
	TenantFleetRoot        string
	Directory              string
	Exports                string
	Providers              string
	VirtualWorkspacePrefix string
}

// New resolves Options into a Layout, filling every empty field from Root.
// It returns an error rather than silently accepting a malformed root, because a
// bad path here produces bindings that reference an export that does not exist —
// a failure that surfaces far from its cause.
func New(opts Options) (Layout, error) {
	root := strings.Trim(strings.TrimSpace(opts.Root), Separator)
	if root == "" {
		root = DefaultRoot
	}
	if err := validate(root); err != nil {
		return Layout{}, fmt.Errorf("install root %q: %w", opts.Root, err)
	}

	l := Layout{
		Root:                   root,
		TenantFleetRoot:        or(opts.TenantFleetRoot, Join(root, SegmentTenants)),
		Directory:              or(opts.Directory, Join(root, SegmentSystem, SegmentDirectory)),
		Exports:                or(opts.Exports, Join(root, SegmentSystem, SegmentControllers)),
		Providers:              or(opts.Providers, Join(root, SegmentSystem, SegmentProviders)),
		VirtualWorkspacePrefix: or(opts.VirtualWorkspacePrefix, DefaultVirtualWorkspacePrefix),
	}

	for name, p := range map[string]string{
		"tenant fleet root":   l.TenantFleetRoot,
		"directory workspace": l.Directory,
		"exports workspace":   l.Exports,
		"providers workspace": l.Providers,
	} {
		if err := validate(p); err != nil {
			return Layout{}, fmt.Errorf("%s %q: %w", name, p, err)
		}
	}

	if !strings.HasPrefix(l.VirtualWorkspacePrefix, "/") {
		return Layout{}, fmt.Errorf("virtual workspace prefix %q: must start with %q", l.VirtualWorkspacePrefix, "/")
	}

	return l, nil
}

// Tenant returns the workspace path of one Tenant. The UUID is the
// workspace name; display names never appear in a path.
func (l Layout) Tenant(tenantUUID string) string {
	return Join(l.TenantFleetRoot, tenantUUID)
}

// Workspace returns the path of a child Workspace inside a Tenant.
func (l Layout) Workspace(tenantUUID, workspaceUUID string) string {
	return Join(l.TenantFleetRoot, tenantUUID, workspaceUUID)
}

// Join assembles a kcp workspace path from segments, dropping empty ones.
func Join(segments ...string) string {
	parts := make([]string, 0, len(segments))
	for _, s := range segments {
		if s = strings.Trim(strings.TrimSpace(s), Separator); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, Separator)
}

// Parent returns everything above the last segment of a path, and false when the
// path has no parent.
func Parent(path string) (string, bool) {
	i := strings.LastIndex(path, Separator)
	if i <= 0 {
		return "", false
	}
	return path[:i], true
}

// Base returns the last segment of a path — for a tenant workspace, its UUID.
func Base(path string) string {
	if i := strings.LastIndex(path, Separator); i >= 0 {
		return path[i+1:]
	}
	return path
}

func or(v, fallback string) string {
	if v = strings.Trim(strings.TrimSpace(v), Separator); v != "" {
		return v
	}
	return fallback
}

func validate(path string) error {
	if path == "" {
		return fmt.Errorf("must not be empty")
	}
	for _, seg := range strings.Split(path, Separator) {
		if seg == "" {
			return fmt.Errorf("must not contain an empty segment")
		}
		if strings.ContainsAny(seg, " /\t") {
			return fmt.Errorf("segment %q must not contain whitespace or %q", seg, "/")
		}
	}
	return nil
}
