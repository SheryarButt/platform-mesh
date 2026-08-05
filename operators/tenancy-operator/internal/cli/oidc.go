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

// Package cli implements the developer CLI: an OIDC login, a kubeconfig, and
// (later) calls against the tenancy virtual workspace.
//
// It holds no tenant-tier credentials and provisions nothing. Both halves matter:
//
//   - No secret. The IdP client is registered `public: true` and the flow is
//     PKCE, so there is no credential here for anyone to steal.
//   - No provisioning. Logging in yields a token, nothing more. A new identity is
//     provisioned by calling `create users` on the virtual workspace — an
//     explicit call the user makes, not a side effect of authenticating.
//
// It is a dev tool. It is not a supported client, and nothing on the server side
// may come to depend on it.
package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/oauth2"
)

// Config is what the CLI needs to talk to the IdP.
//
// The issuer and client ID must match what kcp and the tenancy VW are configured
// with. A token minted for a different audience is rejected by both.
type Config struct {
	IssuerURL string
	ClientID  string

	// CAFile trusts a private-CA issuer, which is the normal case for a dev
	// broker. Without it the discovery call fails on an unknown authority.
	CAFile string

	// Scopes requested. openid is mandatory; the rest shape the id_token so it
	// carries what the VW needs to resolve an identity.
	Scopes []string

	// Resolve overrides name resolution, like `curl --resolve`, for names the
	// system cannot answer. `.localhost` names are handled without it.
	Resolve map[string]string

	// RedirectURL is the loopback callback, and it must EXACTLY match one of the
	// redirectURIs registered for the client — including the port and the path.
	//
	// RFC 8252 says an IdP should accept any port on a loopback redirect, which
	// would let this pick an ephemeral one. Dex does not: its config lists exact
	// URIs, and the platform-mesh client currently registers
	// http://127.0.0.1:8000 with no path. So the port is fixed and configurable
	// rather than chosen, and a mismatch here surfaces as a flat refusal from the
	// IdP with no hint about which side is wrong.
	RedirectURL string
}

// endpoints is the subset of the discovery document we use.
type endpoints struct {
	Issuer        string   `json:"issuer"`
	AuthURL       string   `json:"authorization_endpoint"`
	TokenURL      string   `json:"token_endpoint"`
	JWKSURL       string   `json:"jwks_uri"`
	SupportedPKCE []string `json:"code_challenge_methods_supported"`
}

// httpClient returns a client trusting the configured CA.
func (c Config) httpClient() (*http.Client, error) {
	transport := &http.Transport{
		// Resolves `.localhost` to loopback, which Go's resolver does not do.
		DialContext: dialer(c.Resolve),
	}

	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading the issuer CA from %s: %w", c.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", c.CAFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}

	return &http.Client{Transport: transport, Timeout: 30 * time.Second}, nil
}

// discover reads the issuer's well-known configuration.
func (c Config) discover(ctx context.Context, hc *http.Client) (endpoints, error) {
	var e endpoints

	url := c.IssuerURL + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return e, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return e, fmt.Errorf("reaching the issuer at %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return e, fmt.Errorf("issuer %s returned %s for its discovery document", c.IssuerURL, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return e, fmt.Errorf("parsing the discovery document from %s: %w", url, err)
	}

	// The issuer in the document is what token verification compares against, so
	// a mismatch here means every token we obtain would be rejected downstream.
	if e.Issuer != c.IssuerURL {
		return e, fmt.Errorf("issuer mismatch: configured %q, document says %q", c.IssuerURL, e.Issuer)
	}
	return e, nil
}

// Login performs the PKCE authorization-code flow and returns the tokens.
//
// A public client with no secret: the code is bound to a verifier this process
// generated and never transmitted, so an intercepted code is useless. There is no
// credential here for an attacker to steal, because we hold none.
func Login(ctx context.Context, cfg Config, openBrowser bool) (*oauth2.Token, error) {
	hc, err := cfg.httpClient()
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)

	eps, err := cfg.discover(ctx, hc)
	if err != nil {
		return nil, err
	}

	redirectURL := cfg.RedirectURL
	if redirectURL == "" {
		return nil, fmt.Errorf("a redirect URL is required and must match one registered with the IdP")
	}
	u, err := neturl.Parse(redirectURL)
	if err != nil {
		return nil, fmt.Errorf("redirect URL %q: %w", redirectURL, err)
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "80")
	}
	callbackPath := u.Path
	if callbackPath == "" {
		callbackPath = "/"
	}

	listener, err := net.Listen("tcp", host)
	if err != nil {
		return nil, fmt.Errorf("listening on %s for the callback (is another login running?): %w", host, err)
	}
	defer func() { _ = listener.Close() }()

	oauthCfg := &oauth2.Config{
		ClientID:    cfg.ClientID,
		Endpoint:    oauth2.Endpoint{AuthURL: eps.AuthURL, TokenURL: eps.TokenURL},
		RedirectURL: redirectURL,
		Scopes:      cfg.Scopes,
	}

	verifier := oauth2.GenerateVerifier()
	state := oauth2.GenerateVerifier() // reused as a CSRF nonce; any high-entropy string works
	authURL := oauthCfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline, // ask for a refresh token, so `get-token` can renew without a browser
		oauth2.S256ChallengeOption(verifier),
	)

	result := make(chan callbackResult, 1)
	srv := &http.Server{
		Handler:           callbackHandler(callbackPath, state, result),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	fmt.Fprintf(os.Stderr, "Opening browser to sign in.\nIf it does not open, visit:\n\n  %s\n\n", authURL)
	if openBrowser {
		_ = browse(authURL)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-result:
		if r.err != nil {
			return nil, r.err
		}
		token, err := oauthCfg.Exchange(ctx, r.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, fmt.Errorf("exchanging the authorization code: %w", err)
		}
		if token.Extra("id_token") == nil {
			// kcp and the VW authenticate the id_token, not the access token. A
			// provider that returns only an access token cannot be used here, and
			// saying so now beats a confusing 401 later.
			return nil, fmt.Errorf("the issuer returned no id_token: request the `openid` scope and check the client is configured to issue one")
		}
		return token, nil
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("timed out waiting for the browser callback")
	}
}

type callbackResult struct {
	code string
	err  error
}

func callbackHandler(path, wantState string, out chan<- callbackResult) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		if e := q.Get("error"); e != "" {
			msg := e
			if d := q.Get("error_description"); d != "" {
				msg += ": " + d
			}
			http.Error(w, msg, http.StatusBadRequest)
			out <- callbackResult{err: fmt.Errorf("the issuer refused the login: %s", msg)}
			return
		}

		// A mismatched state means this callback is not the one we started, so the
		// code in it is not ours to exchange.
		if q.Get("state") != wantState {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			out <- callbackResult{err: fmt.Errorf("callback state did not match the request; discarding")}
			return
		}

		code := q.Get("code")
		if code == "" {
			http.Error(w, "no code", http.StatusBadRequest)
			out <- callbackResult{err: fmt.Errorf("callback carried no authorization code")}
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><h3>Signed in.</h3><p>You can close this tab and return to the terminal.</p></body></html>"))
		out <- callbackResult{code: code}
	})
	return mux
}

// Refresh renews a token without a browser. Returns the same token when it is
// still valid, so callers can call it unconditionally.
func Refresh(ctx context.Context, cfg Config, token *oauth2.Token) (*oauth2.Token, error) {
	if token == nil {
		return nil, fmt.Errorf("no token to refresh")
	}
	if token.Valid() {
		return token, nil
	}
	if token.RefreshToken == "" {
		return nil, fmt.Errorf("token expired and no refresh token is stored; run `login` again")
	}

	hc, err := cfg.httpClient()
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)

	eps, err := cfg.discover(ctx, hc)
	if err != nil {
		return nil, err
	}

	oauthCfg := &oauth2.Config{
		ClientID: cfg.ClientID,
		Endpoint: oauth2.Endpoint{AuthURL: eps.AuthURL, TokenURL: eps.TokenURL},
		Scopes:   cfg.Scopes,
	}
	// TokenSource refreshes on demand; no client secret is involved.
	refreshed, err := oauthCfg.TokenSource(ctx, token).Token()
	if err != nil {
		return nil, fmt.Errorf("refreshing the token: %w", err)
	}
	return refreshed, nil
}

// IDToken pulls the id_token out of a token response. This — not the access
// token — is what kcp and the tenancy VW authenticate.
func IDToken(t *oauth2.Token) (string, error) {
	raw, ok := t.Extra("id_token").(string)
	if !ok || raw == "" {
		return "", fmt.Errorf("token response carries no id_token")
	}
	return raw, nil
}

// browse opens a URL in the user's browser, best effort.
func browse(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
