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

// Package identity turns a verified OIDC identity into the two keys the tenancy
// model is built on:
//
//   - the USER NAME, sha256(issuer + "\n" + sub), the User's metadata.name. No
//     claim convention affects it, which is what makes self-provision idempotent:
//     two concurrent calls collide on AlreadyExists instead of minting two
//     projects.
//   - the RBAC IDENTITY, the username kcp sees after OIDC extraction, and the
//     subject of every ClusterRoleBinding the controller writes.
//
// The second mirrors kcp's own authenticator configuration, and mirrors drift. If
// the two disagree, every binding names a subject that never authenticates: the
// user is silently 403'd in a workspace they belong to, with a Membership and a
// binding that both look correct. Compute it from the same configuration kcp runs
// with, and treat a change of claim convention as an identity migration.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// UsernameClaim selects which verified claim becomes the kcp username. It mirrors
// kcp's Authentication.OIDC UsernameClaim.
type UsernameClaim string

const (
	// ClaimEmail is the common choice, and readable in a RoleBinding — but it is
	// MUTABLE. An address change silently invalidates that one user's bindings.
	ClaimEmail UsernameClaim = "email"

	// ClaimSub is stable and opaque. Safer, unreadable in a RoleBinding.
	ClaimSub UsernameClaim = "sub"
)

// Separator joins issuer and subject before hashing.
//
// A newline, not "/": neither an issuer URL nor a `sub` can contain a raw
// newline, so the concatenation is unambiguous. With "/" it was not —
// ("https://a.example/b", "c") and ("https://a.example", "b/c") produce the same
// digest, which would silently merge two identities into one project.
const Separator = "\n"

// Claims is the subset of a verified id_token this package needs.
type Claims struct {
	Issuer  string
	Subject string
	Email   string
	Name    string
	Groups  []string
}

// Config mirrors the kcp authenticator settings. Both fields are deployment
// configuration; nothing in the tenancy model depends on their values.
type Config struct {
	// UsernameClaim is the claim kcp turns into the username.
	UsernameClaim UsernameClaim

	// UsernamePrefix is prepended to that claim's value. Without one, usernames
	// can collide with kcp-internal or ServiceAccount subjects.
	UsernamePrefix string

	// GroupsPrefix is prepended to every group kcp extracts, and is the mirror
	// that matters for a Group subject in a ClusterRoleBinding.
	GroupsPrefix string
}

// Resolver derives identities under one authenticator configuration.
type Resolver struct {
	cfg Config
}

// NewResolver validates the configuration and returns a Resolver. An unknown
// username claim is an error rather than a fallback: guessing here produces
// bindings that silently never match.
func NewResolver(cfg Config) (*Resolver, error) {
	switch cfg.UsernameClaim {
	case ClaimEmail, ClaimSub:
	case "":
		return nil, fmt.Errorf("username claim must be set (it mirrors kcp's Authentication.OIDC UsernameClaim)")
	default:
		return nil, fmt.Errorf("unsupported username claim %q: expected %q or %q — a structured authentication CEL expression is not mirrorable here", cfg.UsernameClaim, ClaimEmail, ClaimSub)
	}
	return &Resolver{cfg: cfg}, nil
}

// RBACIdentity returns the username kcp will see for these claims — the subject
// every ClusterRoleBinding must name.
func (r *Resolver) RBACIdentity(c Claims) (string, error) {
	var value string
	switch r.cfg.UsernameClaim {
	case ClaimEmail:
		value = c.Email
	case ClaimSub:
		value = c.Subject
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("claim %q is empty: cannot derive an RBAC identity", r.cfg.UsernameClaim)
	}
	return r.cfg.UsernamePrefix + value, nil
}

// RBACGroup returns the name kcp will know a group by — the subject every
// group-scoped ClusterRoleBinding must name.
func (r *Resolver) RBACGroup(group string) (string, error) {
	if strings.TrimSpace(group) == "" {
		return "", fmt.Errorf("group must not be empty")
	}
	return r.cfg.GroupsPrefix + group, nil
}

// GroupName returns the object name of a group's read model: sha256 of the group,
// as 64 lowercase hex characters.
func GroupName(group string) (string, error) {
	if strings.TrimSpace(group) == "" {
		return "", fmt.Errorf("group must not be empty")
	}
	sum := sha256.Sum256([]byte(group))
	return hex.EncodeToString(sum[:]), nil
}

// Mutable reports whether the configured claim can change for a person who stays
// the same person. When it can, a claim change is a per-user identity migration:
// the User object survives (it is keyed on the subject hash) but its bindings do
// not.
func (r *Resolver) Mutable() bool {
	return r.cfg.UsernameClaim == ClaimEmail
}

// UserName returns sha256(issuer + "\n" + sub) as 64 lowercase hex characters —
// the User object's metadata.name.
//
// Never truncated: a name may be 253 characters, so there is no reason to shave
// the collision domain. Independent of the username claim, so changing that
// convention never orphans a User — only their RBAC bindings.
func UserName(issuer, subject string) (string, error) {
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(subject) == "" {
		return "", fmt.Errorf("issuer and subject must both be set")
	}
	sum := sha256.Sum256([]byte(issuer + Separator + subject))
	return hex.EncodeToString(sum[:]), nil
}
