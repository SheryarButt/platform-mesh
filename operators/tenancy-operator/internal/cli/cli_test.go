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

package cli_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"go.platform-mesh.io/tenancy-operator/internal/cli"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"
)

// The id_token is what kcp and the VW authenticate, and oauth2.Token drops Extra
// on a plain marshal — so losing it here would produce a cache that looks fine
// and authenticates nothing.
func TestTokenCacheRoundTripsTheIDToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")

	token := (&oauth2.Token{
		AccessToken:  "access",
		TokenType:    "Bearer",
		RefreshToken: "refresh",
		Expiry:       time.Now().Add(time.Hour).Truncate(time.Second),
	}).WithExtra(map[string]any{"id_token": "the-id-token"})

	require.NoError(t, cli.SaveToken(path, token))

	loaded, err := cli.LoadToken(path)
	require.NoError(t, err)

	assert.Equal(t, "access", loaded.AccessToken)
	assert.Equal(t, "refresh", loaded.RefreshToken)

	id, err := cli.IDToken(loaded)
	require.NoError(t, err)
	assert.Equal(t, "the-id-token", id, "the id_token must survive the cache")
}

// Not being signed in is the normal first state, so it must read as an
// instruction rather than a file-not-found.
func TestLoadTokenSaysHowToSignIn(t *testing.T) {
	_, err := cli.LoadToken(filepath.Join(t.TempDir(), "absent.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "login")
}

func TestKubeconfigUsesTheCredentialPlugin(t *testing.T) {
	data, err := cli.Kubeconfig(cli.KubeconfigOptions{
		Server:      "https://pm.localhost:8443",
		Cluster:     "2cyb4oxml4sv8o3r",
		ExecCommand: "/usr/local/bin/tenancyctl",
		ExecArgs:    []string{"get-token", "--oidc-issuer-url=https://idp.example"},
	})
	require.NoError(t, err)

	out := string(data)
	assert.Contains(t, out, "server: https://pm.localhost:8443/clusters/2cyb4oxml4sv8o3r")
	assert.Contains(t, out, "command: /usr/local/bin/tenancyctl")
	assert.Contains(t, out, "get-token")
	// PKCE refresh needs only the issuer and client ID; no client secret exists
	// anywhere, and one in a kubeconfig would contradict that.
	assert.NotContains(t, out, "client-secret")
}

// A base URL may already carry a workspace segment; appending a second one would
// produce a URL that 404s in a way that looks like a missing workspace.
func TestKubeconfigNormalisesAServerThatAlreadyHasACluster(t *testing.T) {
	data, err := cli.Kubeconfig(cli.KubeconfigOptions{
		Server:  "https://pm.localhost:8443/clusters/root",
		Cluster: "abc",
		Token:   "t",
	})
	require.NoError(t, err)
	assert.Contains(t, string(data), "server: https://pm.localhost:8443/clusters/abc")
}

// There is no default workspace in this model, so refusing is correct.
func TestKubeconfigRequiresAWorkspace(t *testing.T) {
	_, err := cli.Kubeconfig(cli.KubeconfigOptions{Server: "https://x", Token: "t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no default workspace")
}

func TestExecCredentialShape(t *testing.T) {
	var buf bytes.Buffer
	expiry := time.Now().Add(time.Hour)
	require.NoError(t, cli.PrintExecCredential(&buf, "id-token-value", expiry))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))

	assert.Equal(t, "client.authentication.k8s.io/v1beta1", got["apiVersion"])
	assert.Equal(t, "ExecCredential", got["kind"])

	status := got["status"].(map[string]any)
	assert.Equal(t, "id-token-value", status["token"])
	// Without an expiry client-go re-invokes the plugin on every request.
	assert.NotEmpty(t, status["expirationTimestamp"])
}

// whoami derives the User name locally so someone can compare it against the
// cluster — the name is a digest, so it is not otherwise guessable.
func TestPrintIdentityDerivesTheUserName(t *testing.T) {
	claims := map[string]any{
		"iss":   "https://idp.pm.localhost:8443",
		"sub":   "CgVhZG1pbhIFbG9jYWw",
		"email": "admin@example.com",
	}
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	idToken := "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"

	var buf bytes.Buffer
	require.NoError(t, cli.PrintIdentity(&buf, idToken, time.Time{}))

	want, err := identity.UserName("https://idp.pm.localhost:8443", "CgVhZG1pbhIFbG9jYWw")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), want)
	assert.Contains(t, buf.String(), "admin@example.com")
}

func TestParseClaimsRejectsNonJWT(t *testing.T) {
	_, err := cli.ParseClaims("not-a-jwt")
	assert.Error(t, err)
}
