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

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

	pmcontext "go.platform-mesh.io/golang-commons/context"
	"go.platform-mesh.io/golang-commons/context/keys"
	pmjwt "go.platform-mesh.io/golang-commons/jwt"
	pmmw "go.platform-mesh.io/golang-commons/middleware"
	appcontext "go.platform-mesh.io/search-service/internal/context"
)

var testJWTSigningKey = []byte("0123456789abcdef0123456789abcdef")

type fakeOrgValidator struct {
	valid bool
	err   error
	org   string
	auth  string
	calls int
}

func (f *fakeOrgValidator) ValidateTokenForOrg(_ context.Context, authHeader, org string) (bool, error) {
	f.calls++
	f.org = org
	f.auth = authHeader
	return f.valid, f.err
}

func newWebToken(t *testing.T, claims map[string]any) pmjwt.WebToken {
	t.Helper()

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: testJWTSigningKey}, nil)
	if err != nil {
		t.Fatalf("create JWT signer: %v", err)
	}
	token, err := josejwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	webToken, err := pmjwt.New(token, []jose.SignatureAlgorithm{jose.HS256})
	if err != nil {
		t.Fatalf("parse JWT: %v", err)
	}
	return webToken
}

func TestSetRequestContextUsesConfiguredUserClaim(t *testing.T) {
	tests := []struct {
		name      string
		userClaim string
		wantUser  string
	}{
		{name: "email", userClaim: "email", wantUser: "user@example.org"},
		{name: "subject", userClaim: "sub", wantUser: "subject-user"},
		{name: "custom claim", userClaim: "preferred_username", wantUser: "roman"},
		{name: "claim with surrounding whitespace", userClaim: "spaced_user", wantUser: "trimmed-user"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validator := &fakeOrgValidator{valid: true}
			mw := NewOrgContextMiddleware(validator, false, "local", tt.userClaim)

			req := httptest.NewRequest(http.MethodGet, "/rest/v1/search?q=test", nil)
			req.Host = "acme.platform-mesh.io:8443"

			ctx := pmcontext.AddAuthHeaderToContext(req.Context(), "Bearer abc")
			ctx = context.WithValue(ctx, keys.WebTokenCtxKey, newWebToken(t, map[string]any{
				"iss":                "https://idp.example.org/auth/realms/acme-tenant",
				"sub":                "subject-user",
				"email":              "user@example.org",
				"preferred_username": "roman",
				"spaced_user":        "  trimmed-user  ",
			}))
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rc, err := appcontext.GetRequestContext(r.Context())
				if err != nil {
					t.Fatalf("request context missing: %v", err)
				}
				if rc.Organization != "acme" {
					t.Fatalf("unexpected org: %s", rc.Organization)
				}
				if rc.User != tt.wantUser {
					t.Fatalf("expected configured claim user %q, got %q", tt.wantUser, rc.User)
				}
				if rc.IDMTenant != "acme-tenant" {
					t.Fatalf("unexpected tenant: %s", rc.IDMTenant)
				}
				w.WriteHeader(http.StatusNoContent)
			})

			mw.SetRequestContext()(next).ServeHTTP(rr, req)

			if rr.Code != http.StatusNoContent {
				t.Fatalf("expected 204, got %d", rr.Code)
			}
			if validator.org != "acme" {
				t.Fatalf("expected org acme in validator, got %s", validator.org)
			}
			if validator.auth != "Bearer abc" {
				t.Fatalf("expected auth header passed to validator")
			}
		})
	}
}

func TestSetRequestContextReturns401ForInvalidConfiguredUserClaim(t *testing.T) {
	tests := []struct {
		name      string
		userClaim string
		claims    map[string]any
	}{
		{
			name:      "missing claim",
			userClaim: "preferred_username",
			claims:    map[string]any{"email": "user@example.org"},
		},
		{
			name:      "non-string claim",
			userClaim: "user_id",
			claims:    map[string]any{"user_id": 42},
		},
		{
			name:      "blank claim",
			userClaim: "preferred_username",
			claims:    map[string]any{"preferred_username": "  "},
		},
		{
			name:      "blank claim name",
			userClaim: "  ",
			claims:    map[string]any{"email": "user@example.org"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validator := &fakeOrgValidator{valid: false}
			mw := NewOrgContextMiddleware(validator, true, "local", tt.userClaim)

			req := httptest.NewRequest(http.MethodGet, "/rest/v1/search?q=test", nil)
			req.Host = "localhost:8443"
			tt.claims["iss"] = "https://idp.example.org/auth/realms/acme-tenant"
			ctx := context.WithValue(req.Context(), keys.WebTokenCtxKey, newWebToken(t, tt.claims))
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			mw.SetRequestContext()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatalf("next handler must not be called")
			})).ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rr.Code)
			}
		})
	}
}

func TestSetRequestContextReturns401WhenJWTValidationFails(t *testing.T) {
	validator := &fakeOrgValidator{valid: false}
	mw := NewOrgContextMiddleware(validator, false, "local", "email")

	req := httptest.NewRequest(http.MethodGet, "/rest/v1/search?q=test", nil)
	req.Host = "acme.platform-mesh.io"
	ctx := pmcontext.AddAuthHeaderToContext(req.Context(), "Bearer abc")
	ctx = context.WithValue(ctx, keys.WebTokenCtxKey, pmjwt.WebToken{
		IssuerAttributes: pmjwt.IssuerAttributes{
			Issuer:  "https://idp.example.org/auth/realms/acme-tenant",
			Subject: "user",
		},
	})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	mw.SetRequestContext()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("next handler must not be called")
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if validator.calls != 1 {
		t.Fatalf("expected validator to be called once, got %d", validator.calls)
	}
}

func TestSetRequestContextReturns500OnValidatorError(t *testing.T) {
	validator := &fakeOrgValidator{err: errors.New("boom")}
	mw := NewOrgContextMiddleware(validator, false, "local", "email")

	req := httptest.NewRequest(http.MethodGet, "/rest/v1/search?q=test", nil)
	req.Host = "acme.platform-mesh.io"
	ctx := pmcontext.AddAuthHeaderToContext(req.Context(), "Bearer abc")
	ctx = context.WithValue(ctx, keys.WebTokenCtxKey, pmjwt.WebToken{
		IssuerAttributes: pmjwt.IssuerAttributes{
			Issuer:  "https://idp.example.org/auth/realms/acme-tenant",
			Subject: "user",
		},
	})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	mw.SetRequestContext()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("next handler must not be called")
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

func TestSetRequestContextReturns401ForInvalidTokenContext(t *testing.T) {
	validator := &fakeOrgValidator{valid: true}
	mw := NewOrgContextMiddleware(validator, false, "local", "email")

	req := httptest.NewRequest(http.MethodGet, "/rest/v1/search?q=test", nil)
	req.Host = "acme.platform-mesh.io"
	req = req.WithContext(pmcontext.AddAuthHeaderToContext(req.Context(), "Bearer abc"))

	rr := httptest.NewRecorder()
	mw.SetRequestContext()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("next handler must not be called")
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if validator.calls != 0 {
		t.Fatalf("validator must not be called without a parsed token")
	}
}

func TestSetRequestContextReturns401ForMissingOrUnparseableJWT(t *testing.T) {
	tests := []struct {
		name       string
		authHeader string
	}{
		{name: "missing token"},
		{name: "unparseable token", authHeader: "Bearer not-a-jwt"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			validator := &fakeOrgValidator{valid: true}
			mw := NewOrgContextMiddleware(validator, false, "local", "email")
			handler := pmmw.StoreWebToken()(pmmw.StoreAuthHeader()(mw.SetRequestContext()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatalf("next handler must not be called")
			}))))

			req := httptest.NewRequest(http.MethodGet, "/rest/v1/search?q=test", nil)
			req.Host = "acme.platform-mesh.io"
			if tc.authHeader != "" {
				req.Header.Set(pmmw.AuthorizationHeader, tc.authHeader)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rr.Code)
			}
			if validator.calls != 0 {
				t.Fatalf("validator must not be called without a parsed token")
			}
		})
	}
}

func TestSetRequestContextReturns401ForInvalidIssuer(t *testing.T) {
	validator := &fakeOrgValidator{valid: true}
	mw := NewOrgContextMiddleware(validator, false, "local", "email")

	req := httptest.NewRequest(http.MethodGet, "/rest/v1/search?q=test", nil)
	req.Host = "acme.platform-mesh.io"
	ctx := pmcontext.AddAuthHeaderToContext(req.Context(), "Bearer abc")
	ctx = context.WithValue(ctx, keys.WebTokenCtxKey, newWebToken(t, map[string]any{
		"iss":   "https://idp.example.org/no-realms-segment",
		"email": "user@example.org",
	}))
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	mw.SetRequestContext()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("next handler must not be called")
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestSetRequestContextLocalhostUsesHostOrgAndValidatesJWT(t *testing.T) {
	validator := &fakeOrgValidator{valid: true}
	mw := NewOrgContextMiddleware(validator, false, "local-org-test", "email")

	req := httptest.NewRequest(http.MethodGet, "/rest/v1/search?q=test", nil)
	req.Host = "localhost:8443"

	ctx := pmcontext.AddAuthHeaderToContext(req.Context(), "bearer\tabc")
	ctx = context.WithValue(ctx, keys.WebTokenCtxKey, newWebToken(t, map[string]any{
		"iss":   "https://idp.example.org/auth/realms/acme-tenant",
		"email": "user@example.org",
	}))
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc, err := appcontext.GetRequestContext(r.Context())
		if err != nil {
			t.Fatalf("request context missing: %v", err)
		}
		if rc.Organization != "localhost" {
			t.Fatalf("unexpected org: %s", rc.Organization)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mw.SetRequestContext()(next).ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if validator.calls != 1 || validator.org != "localhost" || validator.auth != "Bearer abc" {
		t.Fatalf("unexpected validator call: calls=%d org=%q auth=%q", validator.calls, validator.org, validator.auth)
	}
}

func TestSetRequestContextBypassesJWTValidationAndUsesConfiguredOrgInLocalDevelopmentMode(t *testing.T) {
	validator := &fakeOrgValidator{valid: false}
	mw := NewOrgContextMiddleware(validator, true, "local-org-test", "email")

	req := httptest.NewRequest(http.MethodGet, "/rest/v1/search?q=test", nil)
	req.Host = "search.portal.localhost"

	ctx := pmcontext.AddAuthHeaderToContext(req.Context(), "Bearer abc")
	ctx = context.WithValue(ctx, keys.WebTokenCtxKey, newWebToken(t, map[string]any{
		"iss":   "https://idp.example.org/auth/realms/acme-tenant",
		"email": "user@example.org",
	}))
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc, err := appcontext.GetRequestContext(r.Context())
		if err != nil {
			t.Fatalf("request context missing: %v", err)
		}
		if rc.Organization != "local-org-test" {
			t.Fatalf("unexpected org: %s", rc.Organization)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mw.SetRequestContext()(next).ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if validator.calls != 0 {
		t.Fatalf("validator must not be called in local development mode")
	}
}

func TestSetRequestContextReturns401ForMalformedAuthorizationHeader(t *testing.T) {
	validator := &fakeOrgValidator{valid: true}
	mw := NewOrgContextMiddleware(validator, false, "local", "email")

	req := httptest.NewRequest(http.MethodGet, "/rest/v1/search?q=test", nil)
	req.Host = "acme.platform-mesh.io"

	ctx := pmcontext.AddAuthHeaderToContext(req.Context(), "abc")
	ctx = context.WithValue(ctx, keys.WebTokenCtxKey, pmjwt.WebToken{
		IssuerAttributes: pmjwt.IssuerAttributes{
			Issuer:  "https://idp.example.org/auth/realms/acme-tenant",
			Subject: "subject-user",
		},
	})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	mw.SetRequestContext()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("next handler must not be called")
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if validator.org != "" || validator.auth != "" {
		t.Fatalf("validator must not be called on malformed auth header")
	}
}

func TestNewOrgContextMiddlewareFallsBackToDefaultLocalOrg(t *testing.T) {
	validator := &fakeOrgValidator{valid: false}
	mw := NewOrgContextMiddleware(validator, true, "", "email")

	req := httptest.NewRequest(http.MethodGet, "/rest/v1/search?q=test", nil)
	req.Host = "localhost:8443"

	ctx := pmcontext.AddAuthHeaderToContext(req.Context(), "Bearer abc")
	ctx = context.WithValue(ctx, keys.WebTokenCtxKey, newWebToken(t, map[string]any{
		"iss":   "https://idp.example.org/auth/realms/acme-tenant",
		"email": "user@example.org",
	}))
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc, err := appcontext.GetRequestContext(r.Context())
		if err != nil {
			t.Fatalf("request context missing: %v", err)
		}
		if rc.Organization != defaultLocalDevelopmentOrg {
			t.Fatalf("unexpected org: %s", rc.Organization)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mw.SetRequestContext()(next).ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if validator.calls != 0 {
		t.Fatalf("validator must not be called in local development mode")
	}
}
