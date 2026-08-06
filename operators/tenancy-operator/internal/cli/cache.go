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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
)

// DefaultCacheDir is where the CLI keeps its token.
const DefaultCacheDir = ".tenancy"

// CachePath returns the token cache path, honouring an explicit override.
func CachePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory for the token cache: %w", err)
	}
	return filepath.Join(home, DefaultCacheDir, "token.json"), nil
}

// SaveToken writes the token to disk, readable only by the current user.
//
// This file is the whole credential: an id_token that kcp accepts, plus a refresh
// token that mints more. 0600 on the file and 0700 on the directory is the
// minimum, and it is why the CLI is a dev tool — a real client should use the
// platform keyring.
func SaveToken(path string, token *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating the token cache directory: %w", err)
	}

	// The id_token lives in Extra and is dropped by a plain marshal of
	// oauth2.Token, so persist it explicitly — it is the only part kcp actually
	// authenticates.
	idToken, _ := token.Extra("id_token").(string)
	stored := storedToken{
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		RefreshToken: token.RefreshToken,
		Expiry:       token.Expiry,
		IDToken:      idToken,
	}

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing the token cache: %w", err)
	}
	return nil
}

// LoadToken reads a previously saved token. A missing file is reported plainly so
// the caller can tell the user to log in rather than surfacing an ENOENT.
func LoadToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("not signed in (no token at %s): run `tenancyctl login`", path)
		}
		return nil, err
	}

	var stored storedToken
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("parsing the token cache at %s: %w", path, err)
	}

	token := &oauth2.Token{
		AccessToken:  stored.AccessToken,
		TokenType:    stored.TokenType,
		RefreshToken: stored.RefreshToken,
		Expiry:       stored.Expiry,
	}
	if stored.IDToken != "" {
		token = token.WithExtra(map[string]any{"id_token": stored.IDToken})
	}
	return token, nil
}

type storedToken struct {
	AccessToken  string    `json:"access_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
}

// Context is what a successful login learned about the environment, cached so
// later commands do not have to be told again.
//
// Only addressing, never credentials: the token is a separate file with its own
// lifetime, and mixing the two would mean re-typing the environment every time a
// token is refreshed or discarded.
type Context struct {
	// Server is the tenancy virtual workspace URL.
	Server string `json:"server,omitempty"`

	// CAFile verifies its serving certificate.
	CAFile string `json:"caFile,omitempty"`

	// Cluster is the /clusters/{x} segment to address.
	Cluster string `json:"cluster,omitempty"`

	// KCPServer is the kcp front-proxy base URL, and KCPCAFile verifies it.
	//
	// A DIFFERENT endpoint from Server above: the virtual workspace answers
	// "which tenants and projects do I have", kcp is where the work then
	// happens. Both are remembered because a kubeconfig needs the second and every
	// listing needs the first, and being asked for either one repeatedly is how a
	// tool acquires a shell alias.
	KCPServer string `json:"kcpServer,omitempty"`
	KCPCAFile string `json:"kcpCAFile,omitempty"`

	// The OIDC settings this identity signed in with.
	//
	// Remembered because a kubeconfig embeds them in its exec plugin, and a
	// kubeconfig written without them is not merely inconvenient — it is broken in
	// a way that only shows up later, when kubectl runs the plugin and it exits
	// complaining about a flag the user never saw.
	OIDCIssuerURL string `json:"oidcIssuerURL,omitempty"`
	OIDCClientID  string `json:"oidcClientID,omitempty"`
	OIDCCAFile    string `json:"oidcCAFile,omitempty"`
}

// Merge fills empty fields of c from other, and reports whether anything came
// from there. Flags win; what was remembered fills the gaps.
func (c Context) Merge(other Context) (Context, bool) {
	changed := false
	fill := func(dst *string, src string) {
		if *dst == "" && src != "" {
			*dst = src
			changed = true
		}
	}
	fill(&c.Server, other.Server)
	fill(&c.CAFile, other.CAFile)
	fill(&c.Cluster, other.Cluster)
	fill(&c.KCPServer, other.KCPServer)
	fill(&c.KCPCAFile, other.KCPCAFile)
	fill(&c.OIDCIssuerURL, other.OIDCIssuerURL)
	fill(&c.OIDCClientID, other.OIDCClientID)
	fill(&c.OIDCCAFile, other.OIDCCAFile)
	return c, changed
}

// ContextPath is where Context is cached, beside the token.
func ContextPath(tokenPath string) string {
	return filepath.Join(filepath.Dir(tokenPath), "context.json")
}

// SaveContext records the environment a login was performed against.
//
// Best-effort by design: failing to cache addressing must never fail a login that
// actually succeeded, since the token — the part that is hard to obtain — is
// already safe on disk.
func SaveContext(path string, c Context) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600 for consistency with the token, though this holds no secret.
	return os.WriteFile(path, data, 0o600)
}

// LoadContext reads the cached environment. A missing file is not an error: it
// simply means nothing has been remembered yet.
func LoadContext(path string) Context {
	data, err := os.ReadFile(path)
	if err != nil {
		return Context{}
	}
	var c Context
	if err := json.Unmarshal(data, &c); err != nil {
		return Context{}
	}
	return c
}
