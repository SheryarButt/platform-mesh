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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"
)

// PrintExecCredential writes the client-go credential plugin response.
//
// The ID token, not the access token: kcp and the tenancy VW authenticate the
// id_token, and handing over an access token produces a 401 that looks like a
// broken login rather than the wrong token type.
func PrintExecCredential(w io.Writer, idToken string, expiry time.Time) error {
	cred := map[string]any{
		"apiVersion": "client.authentication.k8s.io/v1beta1",
		"kind":       "ExecCredential",
		"status": map[string]any{
			"token": idToken,
		},
	}
	if !expiry.IsZero() {
		// Letting client-go know when to call again is what makes refresh
		// invisible; without it every request re-invokes this binary.
		cred["status"].(map[string]any)["expirationTimestamp"] = expiry.UTC().Format(time.RFC3339)
	}
	return json.NewEncoder(w).Encode(cred)
}

// Claims are the fields of an id_token this CLI reports.
type Claims struct {
	Issuer  string   `json:"iss"`
	Subject string   `json:"sub"`
	Email   string   `json:"email"`
	Name    string   `json:"name"`
	Groups  []string `json:"groups"`
	Expiry  int64    `json:"exp"`
}

// ParseClaims decodes an id_token's payload WITHOUT verifying it.
//
// Safe only because nothing here is a security decision: this is a local display
// of a token the user already holds. Anything that authorizes must verify the
// signature — which is what the virtual workspace does.
func ParseClaims(idToken string) (Claims, error) {
	var c Claims
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return c, fmt.Errorf("not a JWT: expected 3 dot-separated parts, got %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, fmt.Errorf("decoding the token payload: %w", err)
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, fmt.Errorf("parsing the token payload: %w", err)
	}
	return c, nil
}

// PrintIdentity shows who the cached token says you are, and the two derived keys
// the model actually uses.
//
// Both are worth showing because neither is guessable: the User's name is a
// digest of issuer+subject, and rbacIdentity is a mirror of kcp's username
// convention. When someone is mysteriously 403'd in a workspace they belong to,
// comparing these two against the cluster is the first useful step.
func PrintIdentity(w io.Writer, idToken string, expiry time.Time) error {
	claims, err := ParseClaims(idToken)
	if err != nil {
		return err
	}

	userName, err := identity.UserName(claims.Issuer, claims.Subject)
	if err != nil {
		return err
	}

	bw := &errWriter{w: w}
	bw.printf("issuer:   %s\n", claims.Issuer)
	bw.printf("subject:  %s\n", claims.Subject)
	if claims.Email != "" {
		bw.printf("email:    %s\n", claims.Email)
	}
	if len(claims.Groups) > 0 {
		bw.printf("groups:   %s\n", strings.Join(claims.Groups, ", "))
	}
	bw.printf("\nUser name (sha256(issuer + \"\\n\" + sub)):\n  %s\n", userName)
	bw.printf("\nThis is the User object's metadata.name. It exists only after\n" +
		"`create users` against the tenancy virtual workspace.\n")

	if !expiry.IsZero() {
		bw.printf("\ntoken expires: %s (%s)\n", expiry.UTC().Format(time.RFC3339), time.Until(expiry).Round(time.Second))
	}
	return bw.err
}

// errWriter keeps the first write error so a long series of Fprintf calls does
// not need one check each — stdout can be a closed pipe.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}

// BaseURL strips a trailing /clusters/<path> from a kcp server URL, so a base
// given in either form works.
func BaseURL(server string) string {
	if i := strings.Index(server, "/clusters/"); i >= 0 {
		return strings.TrimSuffix(server[:i], "/")
	}
	return strings.TrimSuffix(server, "/")
}

// PrintUser reports the User the platform holds for the caller.
//
// The two seed fields are shown because they are the answer to "why do I have
// nothing?": both empty on a User that is otherwise Ready means seeding was off,
// not that provisioning failed.
func PrintUser(w io.Writer, u *pmtenancyv1alpha1.User) error {
	bw := &errWriter{w: w}
	bw.printf("\nUser (from the tenancy virtual workspace):\n")
	bw.printf("  name:        %s\n", u.Name)
	if u.Spec.Email != "" {
		bw.printf("  email:       %s\n", u.Spec.Email)
	}
	if u.Spec.RBACIdentity != "" {
		// The join to kcp RBAC. If this does not match what kcp derives from the
		// same token, every binding names a subject that never authenticates.
		bw.printf("  rbacIdentity: %s\n", u.Spec.RBACIdentity)
	}
	bw.printf("  seeding:     tenant=%t workspace=%t\n",
		u.Spec.Tenancy.SeedTenant, u.Spec.Tenancy.SeedProject)

	if u.Status.DefaultTenant != "" {
		bw.printf("  tenant: %s\n", u.Status.DefaultTenant)
	}
	if u.Status.DefaultProject != "" {
		bw.printf("  workspace:    %s\n", u.Status.DefaultProject)
	}

	if len(u.Status.Conditions) > 0 {
		bw.printf("  conditions:\n")
		for _, c := range u.Status.Conditions {
			line := fmt.Sprintf("    %-24s %s", c.Type, c.Status)
			if c.Message != "" {
				line += " — " + c.Message
			}
			bw.printf("%s\n", line)
		}
	}
	return bw.err
}

// PrintTenants renders the Tenants a caller belongs to.
func PrintTenants(w io.Writer, tenants []pmtenancyv1alpha1.Tenant) error {
	if len(tenants) == 0 {
		_, err := fmt.Fprintf(w, "No tenants.\n\n"+
			"This identity belongs to none yet. If you expected a personal one, check\n"+
			"`tenancyctl whoami` — seeding is one-shot and may be off for this User.\n")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	bw := &errWriter{w: tw}
	bw.printf("UUID\tDISPLAY NAME\tPERSONAL\tCLUSTER\n")
	for i := range tenants {
		o := &tenants[i]
		bw.printf("%s\t%s\t%t\t%s\n", o.Name, o.Spec.DisplayName, o.Spec.Personal, o.Status.ClusterID)
	}
	if bw.err != nil {
		return bw.err
	}
	return tw.Flush()
}

// PrintProjects renders the Projects a caller may work in.
//
// The cluster ID is shown because it is the one value a client needs next: it is
// what `tenancyctl kubeconfig --cluster` takes. The kcp Workspace behind the
// Project is never named, here or anywhere else.
func PrintProjects(w io.Writer, projects []pmtenancyv1alpha1.Project) error {
	if len(projects) == 0 {
		_, err := fmt.Fprintf(w, "No projects.\n")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	bw := &errWriter{w: tw}
	bw.printf("UUID\tDISPLAY NAME\tTENANT\tCLUSTER\tREADY\n")
	for i := range projects {
		a := &projects[i]
		ready := "Unknown"
		for _, c := range a.Status.Conditions {
			if c.Type == pmtenancyv1alpha1.ProjectConditionReady {
				ready = string(c.Status)
			}
		}
		bw.printf("%s\t%s\t%s\t%s\t%s\n",
			a.Name, a.Spec.DisplayName, a.Labels[pmtenancyv1alpha1.LabelTenant], a.Status.ClusterID, ready)
	}
	if bw.err != nil {
		return bw.err
	}
	return tw.Flush()
}

// PrintMemberships renders the roster of a Tenant.
//
// USER is a 64-hex identity digest, so it is abbreviated: the full value is
// unreadable in a column and nobody types one from a terminal. `tenancyctl
// memberships` names a grant by its own name, and both are shown.
func PrintMemberships(w io.Writer, memberships []pmtenancyv1alpha1.Membership) error {
	if len(memberships) == 0 {
		_, err := fmt.Fprintf(w, "No memberships.\n")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	bw := &errWriter{w: tw}
	bw.printf("NAME\tUSER\tSCOPE\tPROJECT\tROLE\tTENANT\n")
	for i := range memberships {
		m := &memberships[i]
		project := m.Spec.Project
		if project == "" {
			project = "-"
		}
		bw.printf("%s\t%s\t%s\t%s\t%s\t%s\n",
			m.Name, abbreviate(m.Spec.User), m.Spec.Scope, project, m.Spec.Role,
			m.Labels[pmtenancyv1alpha1.LabelTenant])
	}
	if bw.err != nil {
		return bw.err
	}
	return tw.Flush()
}

// abbreviate shortens an identity digest for display only. Never for input: a
// truncated digest is not a name, and accepting one would make two identities
// collide at exactly the moment it mattered.
func abbreviate(s string) string {
	const shown = 12
	if len(s) <= shown {
		return s
	}
	return s[:shown] + "…"
}
