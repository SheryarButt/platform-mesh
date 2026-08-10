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

package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SelfAlias is what a caller passes instead of computing their own name. It must
// match the virtual workspace's own constant.
const SelfAlias = "~"

// DefaultVWPrefix is where the tenancy virtual workspace is mounted.
const DefaultVWPrefix = "/services/tenancy"

// VWOptions addresses the tenancy virtual workspace.
type VWOptions struct {
	// Server is the VW base URL, e.g. https://localhost:6443.
	Server string

	// Prefix is the VW's mount point. Configuration on the server side, so it is
	// configuration here too.
	Prefix string

	// Cluster is the /clusters/{x} segment. `users` is served from the directory
	// workspace whichever value is used — the storage is scoped there — but the
	// segment is structural and the request 404s without it.
	Cluster string

	// CAFile verifies the VW's serving certificate.
	CAFile string

	// Token is the id_token. The VW authenticates it exactly as kcp does.
	Token string
}

// UserSelf fetches the caller's own User: GET <prefix>/clusters/<c>/apis/…/users/~
//
// Returns a NotProvisionedError when the identity has authenticated but has not
// created its User yet, which is the normal first-run state rather than a
// failure — the caller decides whether to report it or act on it.
func UserSelf(ctx context.Context, opts VWOptions) (*pmtenancyv1alpha1.User, error) {
	if opts.Server == "" {
		return nil, fmt.Errorf("a virtual workspace URL is required")
	}
	if opts.Token == "" {
		return nil, fmt.Errorf("no token: sign in with `tenancyctl login` first")
	}

	client, err := vwClient(opts.CAFile)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usersURL(opts)+"/"+SelfAlias, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+opts.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching the tenancy virtual workspace at %s: %w", opts.Server, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		user := &pmtenancyv1alpha1.User{}
		if err := json.Unmarshal(body, user); err != nil {
			return nil, fmt.Errorf("decoding the User: %w", err)
		}
		return user, nil

	case http.StatusNotFound:
		// The server's message names the call that fixes it; carrying it through
		// verbatim is the point, rather than inventing a second wording here.
		return nil, &NotProvisionedError{Detail: statusMessage(body)}

	case http.StatusUnauthorized:
		return nil, fmt.Errorf("the tenancy virtual workspace rejected the token: %s\n"+
			"the CLI's --oidc-issuer-url and --oidc-client-id must match what the virtual workspace was configured with",
			statusMessage(body))

	default:
		return nil, fmt.Errorf("%s from the tenancy virtual workspace: %s", resp.Status, statusMessage(body))
	}
}

// usersURL builds the collection URL. Both calls go through it so a changed
// prefix or cluster cannot apply to one and not the other.
func usersURL(opts VWOptions) string {
	prefix := opts.Prefix
	if prefix == "" {
		prefix = DefaultVWPrefix
	}
	cluster := opts.Cluster
	if cluster == "" {
		// The wildcard: `users` is self-scoped, so there is no single tenant cluster
		// this could name, and the VW resolves it without one.
		cluster = "*"
	}
	return fmt.Sprintf("%s%s/clusters/%s/apis/%s/%s/users",
		strings.TrimSuffix(opts.Server, "/"),
		"/"+strings.Trim(prefix, "/"),
		cluster,
		pmtenancyv1alpha1.GroupName,
		pmtenancyv1alpha1.GroupVersion,
	)
}

// NotProvisionedError means the caller authenticated but has no User yet.
type NotProvisionedError struct {
	Detail string
}

func (e *NotProvisionedError) Error() string {
	if e.Detail == "" {
		return "this identity has not provisioned itself yet"
	}
	return e.Detail
}

// statusMessage pulls the message out of a metav1.Status body, falling back to
// the raw body when it is not one.
func statusMessage(body []byte) string {
	var status struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &status); err == nil && status.Message != "" {
		return status.Message
	}
	return strings.TrimSpace(string(body))
}

func vwClient(caFile string) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading the CA bundle %s: %w", caFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s contains no certificates", caFile)
		}
		tlsCfg.RootCAs = pool
	}

	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
			// Same `.localhost` handling as the IdP client: Go does not implement
			// RFC 6761, so a VW addressed by a *.localhost name would fail to
			// resolve while curl to the same URL works.
			DialContext: dialer(nil),
		},
	}, nil
}

// CreateUserSelf provisions the caller's own User: POST …/users with an empty body.
//
// The body is deliberately empty. The server ignores every field of a submitted
// spec and fills all of them from the verified token, so sending anything would
// only create the illusion that a client can influence its own record. That is
// also what makes the call idempotent: two concurrent attempts for one identity
// collide on AlreadyExists and both get the same object back, so two browser tabs
// cannot mint two projects.
func CreateUserSelf(ctx context.Context, opts VWOptions) (*pmtenancyv1alpha1.User, error) {
	if opts.Server == "" {
		return nil, fmt.Errorf("a virtual workspace URL is required")
	}
	if opts.Token == "" {
		return nil, fmt.Errorf("no token: sign in with `tenancyctl login` first")
	}

	body, err := json.Marshal(&pmtenancyv1alpha1.User{
		TypeMeta: metav1.TypeMeta{
			APIVersion: pmtenancyv1alpha1.GroupName + "/" + pmtenancyv1alpha1.GroupVersion,
			Kind:       "User",
		},
	})
	if err != nil {
		return nil, err
	}

	client, err := vwClient(opts.CAFile)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, usersURL(opts), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+opts.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching the tenancy virtual workspace at %s: %w", opts.Server, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		user := &pmtenancyv1alpha1.User{}
		if err := json.Unmarshal(respBody, user); err != nil {
			return nil, fmt.Errorf("decoding the User: %w", err)
		}
		return user, nil

	case http.StatusUnauthorized:
		return nil, fmt.Errorf("the tenancy virtual workspace rejected the token: %s\n"+
			"the CLI's --oidc-issuer-url and --oidc-client-id must match what the virtual workspace was configured with",
			statusMessage(respBody))

	default:
		return nil, fmt.Errorf("%s from the tenancy virtual workspace: %s", resp.Status, statusMessage(respBody))
	}
}

// ListTenants returns the Tenants the caller belongs to.
func ListTenants(ctx context.Context, opts VWOptions) ([]pmtenancyv1alpha1.Tenant, error) {
	out := &pmtenancyv1alpha1.TenantList{}
	if err := getInto(ctx, opts, collectionURL(opts, "tenants"), out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// ListProjects returns the Projects the caller may work in, across every
// Tenant they belong to.
func ListProjects(ctx context.Context, opts VWOptions) ([]pmtenancyv1alpha1.Project, error) {
	out := &pmtenancyv1alpha1.ProjectList{}
	if err := getInto(ctx, opts, collectionURL(opts, "projects"), out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// ResolveTenant turns a name-or-UUID into one Tenant.
//
// Display names are NOT unique by design, so an ambiguous one is an error that
// lists the candidates rather than a silent pick — choosing for the caller here
// would act on the wrong tenant and look like it worked.
func ResolveTenant(ctx context.Context, opts VWOptions, nameOrUUID string) (*pmtenancyv1alpha1.Tenant, error) {
	tenants, err := ListTenants(ctx, opts)
	if err != nil {
		return nil, err
	}

	var matches []pmtenancyv1alpha1.Tenant
	for i := range tenants {
		if tenants[i].Name == nameOrUUID {
			// An exact UUID match is unambiguous by construction, so it wins
			// outright — a display name can never shadow it.
			return &tenants[i], nil
		}
		if tenants[i].Spec.DisplayName == nameOrUUID {
			matches = append(matches, tenants[i])
		}
	}

	switch len(matches) {
	case 1:
		return &matches[0], nil
	case 0:
		return nil, fmt.Errorf("no tenant %q: run `tenancyctl tenants` to see the ones you belong to", nameOrUUID)
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%d tenants are called %q; name one by UUID:\n", len(matches), nameOrUUID)
		for _, m := range matches {
			fmt.Fprintf(&b, "  %s\n", m.Name)
		}
		return nil, errors.New(strings.TrimRight(b.String(), "\n"))
	}
}

// collectionURL builds the URL of one resource collection.
func collectionURL(opts VWOptions, resource string) string {
	prefix := opts.Prefix
	if prefix == "" {
		prefix = DefaultVWPrefix
	}
	cluster := opts.Cluster
	if cluster == "" {
		cluster = "*"
	}
	return fmt.Sprintf("%s%s/clusters/%s/apis/%s/%s/%s",
		strings.TrimSuffix(opts.Server, "/"),
		"/"+strings.Trim(prefix, "/"),
		cluster,
		pmtenancyv1alpha1.GroupName,
		pmtenancyv1alpha1.GroupVersion,
		resource,
	)
}

// getInto performs an authenticated GET and decodes the body.
func getInto(ctx context.Context, opts VWOptions, url string, into any) error {
	if opts.Server == "" {
		return fmt.Errorf("a virtual workspace URL is required")
	}
	if opts.Token == "" {
		return fmt.Errorf("no token: sign in with `tenancyctl login` first")
	}

	client, err := vwClient(opts.CAFile)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+opts.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reaching the tenancy virtual workspace at %s: %w", opts.Server, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s from the tenancy virtual workspace: %s", resp.Status, statusMessage(body))
	}
	return json.Unmarshal(body, into)
}

// CreateTenant creates a Tenant owned by the caller.
//
// Only the display name is sent: metadata.name is server-assigned, because the
// name is also the workspace name and a client choosing it would be choosing a
// path.
func CreateTenant(ctx context.Context, opts VWOptions, displayName string) (*pmtenancyv1alpha1.Tenant, error) {
	body, err := json.Marshal(&pmtenancyv1alpha1.Tenant{
		TypeMeta: metav1.TypeMeta{
			APIVersion: pmtenancyv1alpha1.GroupName + "/" + pmtenancyv1alpha1.GroupVersion,
			Kind:       "Tenant",
		},
		Spec: pmtenancyv1alpha1.TenantSpec{DisplayName: displayName},
	})
	if err != nil {
		return nil, err
	}

	out := &pmtenancyv1alpha1.Tenant{}
	if err := postInto(ctx, opts, collectionURL(opts, "tenants"), body, out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateProject creates a Project inside one Tenant.
func CreateProject(ctx context.Context, opts VWOptions, displayName, tenantUUID string) (*pmtenancyv1alpha1.Project, error) {
	body, err := json.Marshal(&pmtenancyv1alpha1.Project{
		TypeMeta: metav1.TypeMeta{
			APIVersion: pmtenancyv1alpha1.GroupName + "/" + pmtenancyv1alpha1.GroupVersion,
			Kind:       "Project",
		},
		ObjectMeta: metav1.ObjectMeta{
			// The same label the server stamps on every Project it returns, so a
			// client writes back what it read.
			Labels: map[string]string{pmtenancyv1alpha1.LabelTenant: tenantUUID},
		},
		Spec: pmtenancyv1alpha1.ProjectSpec{DisplayName: displayName},
	})
	if err != nil {
		return nil, err
	}

	out := &pmtenancyv1alpha1.Project{}
	if err := postInto(ctx, opts, collectionURL(opts, "projects"), body, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListMemberships returns the Memberships of every Tenant the caller
// belongs to — the roster, including co-members.
//
// Readable by every role. Writing is admin-only, and the server decides that: this
// client does not pre-check, because a client-side role check is a UI affordance
// that becomes a lie the moment the two disagree.
func ListMemberships(ctx context.Context, opts VWOptions) ([]pmtenancyv1alpha1.Membership, error) {
	out := &pmtenancyv1alpha1.MembershipList{}
	if err := getInto(ctx, opts, collectionURL(opts, "memberships"), out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// CreateMembership grants a user access to a Tenant or to one Project.
//
// A USER subject is a User NAME — the sha256(issuer + "\n" + sub) digest, not an
// email. There is no lookup by email anywhere in this API: the VW is never a
// directory of the platform's people, so the caller must already know who they are
// granting to.
//
// A GROUP subject is the group as the identity provider emits it, WITHOUT the
// prefix kcp applies. It needs no lookup and no prior sign-in, which is the whole
// difference between the two.
// Subject is who a grant is for: exactly one of the two is set.
//
// A struct rather than two string parameters, because two adjacent strings at a
// call site is how you end up granting a group named after a user digest.
type Subject struct {
	User  string
	Group string
}

func CreateMembership(ctx context.Context, opts VWOptions, tenantUUID string, subject Subject, scope, project, role string) (*pmtenancyv1alpha1.Membership, error) {
	spec := pmtenancyv1alpha1.MembershipSpec{Scope: scope, Project: project, Role: role}
	if subject.Group != "" {
		spec.Group = subject.Group
	} else {
		spec.User = subject.User
	}

	body, err := json.Marshal(&pmtenancyv1alpha1.Membership{
		TypeMeta: metav1.TypeMeta{
			APIVersion: pmtenancyv1alpha1.GroupName + "/" + pmtenancyv1alpha1.GroupVersion,
			Kind:       "Membership",
		},
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{pmtenancyv1alpha1.LabelTenant: tenantUUID},
		},
		Spec: spec,
	})
	if err != nil {
		return nil, err
	}

	out := &pmtenancyv1alpha1.Membership{}
	if err := postInto(ctx, opts, collectionURL(opts, "memberships"), body, out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetMembershipRole changes an existing grant's role.
//
// Read-then-write rather than a patch: the server accepts a change to spec.role
// and refuses everything else, so sending the object back with one field altered
// is both the smallest correct request and the one whose rejection is legible.
func SetMembershipRole(ctx context.Context, opts VWOptions, name, role string) (*pmtenancyv1alpha1.Membership, error) {
	current := &pmtenancyv1alpha1.Membership{}
	if err := getInto(ctx, opts, collectionURL(opts, "memberships")+"/"+name, current); err != nil {
		return nil, err
	}

	current.TypeMeta = metav1.TypeMeta{
		APIVersion: pmtenancyv1alpha1.GroupName + "/" + pmtenancyv1alpha1.GroupVersion,
		Kind:       "Membership",
	}
	// Derived on read and not part of the stored object; sending it back would ask
	// the server to persist a projection of its own making.
	delete(current.Labels, pmtenancyv1alpha1.LabelTenant)
	current.Spec.Role = role

	body, err := json.Marshal(current)
	if err != nil {
		return nil, err
	}

	out := &pmtenancyv1alpha1.Membership{}
	if err := putInto(ctx, opts, collectionURL(opts, "memberships")+"/"+name, body, out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteMembership revokes a grant. An admin may revoke anyone's; anyone may
// revoke their own, which is what leaving a Tenant is.
func DeleteMembership(ctx context.Context, opts VWOptions, name string) error {
	return deleteResource(ctx, opts, collectionURL(opts, "memberships")+"/"+name)
}

// ResolveMembership finds one Membership by name, or by the user it names.
//
// Naming a user is the ergonomic form — nobody remembers a Membership UUID — but
// it is ambiguous whenever someone holds both a tenant-scope and a project-scope
// grant, so that case lists the candidates instead of picking.
func ResolveMembership(ctx context.Context, opts VWOptions, nameOrUser, tenantUUID string) (*pmtenancyv1alpha1.Membership, error) {
	memberships, err := ListMemberships(ctx, opts)
	if err != nil {
		return nil, err
	}

	var matches []pmtenancyv1alpha1.Membership
	for i := range memberships {
		m := &memberships[i]
		if tenantUUID != "" && m.Labels[pmtenancyv1alpha1.LabelTenant] != tenantUUID {
			continue
		}
		if m.Name == nameOrUser {
			return m, nil
		}
		if m.Spec.User == nameOrUser {
			matches = append(matches, *m)
		}
	}

	switch len(matches) {
	case 1:
		return &matches[0], nil
	case 0:
		return nil, fmt.Errorf("no membership %q: run `tenancyctl memberships` to see them", nameOrUser)
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%s holds %d memberships here; name one by its own name:\n", nameOrUser, len(matches))
		for _, m := range matches {
			scope := m.Spec.Scope
			if m.Spec.Project != "" {
				scope += " " + m.Spec.Project
			}
			fmt.Fprintf(&b, "  %s  (%s, %s)\n", m.Name, scope, m.Spec.Role)
		}
		return nil, errors.New(strings.TrimRight(b.String(), "\n"))
	}
}

// putInto performs an authenticated PUT and decodes the updated object.
func putInto(ctx context.Context, opts VWOptions, url string, body []byte, into any) error {
	if opts.Server == "" {
		return fmt.Errorf("a virtual workspace URL is required")
	}
	if opts.Token == "" {
		return fmt.Errorf("no token: sign in with `tenancyctl login` first")
	}

	client, err := vwClient(opts.CAFile)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+opts.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reaching the tenancy virtual workspace at %s: %w", opts.Server, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("%s from the tenancy virtual workspace: %s", resp.Status, statusMessage(respBody))
	}
	return json.Unmarshal(respBody, into)
}

// deleteResource performs an authenticated DELETE.
func deleteResource(ctx context.Context, opts VWOptions, url string) error {
	if opts.Server == "" {
		return fmt.Errorf("a virtual workspace URL is required")
	}
	if opts.Token == "" {
		return fmt.Errorf("no token: sign in with `tenancyctl login` first")
	}

	client, err := vwClient(opts.CAFile)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+opts.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reaching the tenancy virtual workspace at %s: %w", opts.Server, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// 200 with the object, or 202 while its finalizer removes the role bindings —
	// the grant is not gone until that finishes, and the server says so rather than
	// pretending the delete completed.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("%s from the tenancy virtual workspace: %s", resp.Status, statusMessage(body))
	}
	return nil
}

// postInto performs an authenticated POST and decodes the created object.
func postInto(ctx context.Context, opts VWOptions, url string, body []byte, into any) error {
	if opts.Server == "" {
		return fmt.Errorf("a virtual workspace URL is required")
	}
	if opts.Token == "" {
		return fmt.Errorf("no token: sign in with `tenancyctl login` first")
	}

	client, err := vwClient(opts.CAFile)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+opts.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reaching the tenancy virtual workspace at %s: %w", opts.Server, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("%s from the tenancy virtual workspace: %s", resp.Status, statusMessage(respBody))
	}
	return json.Unmarshal(respBody, into)
}

// ResolveProject finds one Project by UUID or display name, optionally within one
// Tenant.
//
// Scoping to a Tenant matters: display names are unique nowhere, and two
// teams in different tenants calling their space "platform" is entirely normal. An
// ambiguous name is an error listing the candidates rather than a silent pick,
// for the same reason it is on Tenants — guessing here writes a kubeconfig
// pointing at the wrong tenant.
func ResolveProject(ctx context.Context, opts VWOptions, nameOrUUID, tenantUUID string) (*pmtenancyv1alpha1.Project, error) {
	projects, err := ListProjects(ctx, opts)
	if err != nil {
		return nil, err
	}

	var matches []pmtenancyv1alpha1.Project
	for i := range projects {
		a := &projects[i]
		if tenantUUID != "" && a.Labels[pmtenancyv1alpha1.LabelTenant] != tenantUUID {
			continue
		}
		if a.Name == nameOrUUID {
			return a, nil
		}
		if a.Spec.DisplayName == nameOrUUID {
			matches = append(matches, *a)
		}
	}

	switch len(matches) {
	case 1:
		return &matches[0], nil
	case 0:
		return nil, fmt.Errorf("no project %q: run `tenancyctl projects` to see the ones you can reach", nameOrUUID)
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%d projects are called %q; name one by UUID:\n", len(matches), nameOrUUID)
		for _, m := range matches {
			fmt.Fprintf(&b, "  %s  (tenant %s)\n", m.Name, m.Labels[pmtenancyv1alpha1.LabelTenant])
		}
		return nil, errors.New(strings.TrimRight(b.String(), "\n"))
	}
}
