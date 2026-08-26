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

package kcp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.platform-mesh.io/golang-commons/logger/testlogger"

	"k8s.io/client-go/rest"
)

func TestOrgAccessValidatorValidateTokenForOrg(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		valid      bool
		wantErr    bool
	}{
		{name: "authenticated", statusCode: http.StatusOK, valid: true},
		{name: "authenticated with created response", statusCode: http.StatusCreated, valid: true},
		{name: "authenticated but forbidden", statusCode: http.StatusForbidden, valid: true},
		{name: "invalid JWT", statusCode: http.StatusUnauthorized, valid: false},
		{name: "unexpected client response", statusCode: http.StatusBadRequest, wantErr: true},
		{name: "unknown organization", statusCode: http.StatusNotFound, wantErr: true},
		{name: "throttled by kcp", statusCode: http.StatusTooManyRequests, wantErr: true},
		{name: "server failure", statusCode: http.StatusInternalServerError, wantErr: true},
		{name: "kcp unavailable", statusCode: http.StatusServiceUnavailable, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/clusters/root:orgs:acme/version" {
					t.Errorf("unexpected request path: %s", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer token" {
					t.Errorf("unexpected authorization header: %q", got)
				}
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(`{"gitVersion":"v1.30.0"}`))
			}))
			defer server.Close()

			log := testlogger.New().HideLogOutput().Logger
			validator, err := NewOrgAccessValidator(&rest.Config{Host: server.URL}, log)
			if err != nil {
				t.Fatalf("create validator: %v", err)
			}

			valid, err := validator.ValidateTokenForOrg(t.Context(), "Bearer token", "acme")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("validate token: %v", err)
			}

			if valid != tc.valid {
				t.Fatalf("expected valid=%t, got %t", tc.valid, valid)
			}
		})
	}
}

// Validation runs on every API request, so the response body must be drained to keep the
// connection poolable instead of forcing a fresh TLS handshake per request.
func TestOrgAccessValidatorReusesConnections(t *testing.T) {
	for _, statusCode := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusBadRequest} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			conns := map[string]struct{}{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conns[r.RemoteAddr] = struct{}{}
				w.WriteHeader(statusCode)
				_, _ = w.Write([]byte(`{"gitVersion":"v1.30.0"}`))
			}))
			defer server.Close()

			log := testlogger.New().HideLogOutput().Logger
			validator, err := NewOrgAccessValidator(&rest.Config{Host: server.URL}, log)
			if err != nil {
				t.Fatalf("create validator: %v", err)
			}

			for range 3 {
				_, _ = validator.ValidateTokenForOrg(t.Context(), "Bearer token", "acme")
			}

			if len(conns) != 1 {
				t.Fatalf("expected all requests on one connection, got %d", len(conns))
			}
		})
	}
}
