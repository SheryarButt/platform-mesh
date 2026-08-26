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
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go.platform-mesh.io/golang-commons/logger"

	"k8s.io/client-go/rest"
)

const (
	// maxSnippetBytes defines how much of an unexpected response body to read for logging.
	maxSnippetBytes = 1024
)

type OrgAccessValidator struct {
	http    *http.Client
	baseURL *url.URL
	log     *logger.Logger
}

func NewOrgAccessValidator(restCfg *rest.Config, log *logger.Logger) (*OrgAccessValidator, error) {
	cfg := rest.CopyConfig(restCfg)
	cfg.KeyData = nil
	cfg.CertData = nil
	cfg.KeyFile = ""
	cfg.CertFile = ""

	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("create kcp HTTP client: %w", err)
	}

	baseURL, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("parse kcp host URL: %w", err)
	}
	baseURL.Path = ""

	return &OrgAccessValidator{http: httpClient, baseURL: baseURL, log: log}, nil
}

// ValidateTokenForOrg returns true when kcp authenticated the JWT for the organization.
// A forbidden response is valid here because it proves authentication succeeded; OpenFGA
// remains responsible for authorizing individual search results.
//
// Only a kcp 401 means the JWT is invalid. Any other unexpected status is reported
// as an error.
func (v *OrgAccessValidator) ValidateTokenForOrg(ctx context.Context, authHeader, org string) (bool, error) {
	clusterPath := fmt.Sprintf("root:orgs:%s", org)
	requestURL := fmt.Sprintf("%s://%s/clusters/%s/version", v.baseURL.Scheme, v.baseURL.Host, clusterPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, http.NoBody)
	if err != nil {
		return false, fmt.Errorf("create kcp auth request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)

	resp, err := v.http.Do(req)
	if err != nil {
		v.log.Error().
			Err(err).
			Str("organization", org).
			Str("clusterPath", clusterPath).
			Msg("kcp org token validation request failed")
		return false, fmt.Errorf("execute kcp auth request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusForbidden:
		return true, nil
	case http.StatusUnauthorized:
		v.log.Warn().
			Str("organization", org).
			Str("clusterPath", clusterPath).
			Int("statusCode", resp.StatusCode).
			Msg("kcp rejected the JWT")
		return false, nil
	default:
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxSnippetBytes))
		bodySnippet := strings.TrimSpace(string(bodyBytes))
		logEvt := v.log.Warn().
			Str("organization", org).
			Str("clusterPath", clusterPath).
			Int("statusCode", resp.StatusCode)
		if bodySnippet != "" {
			logEvt = logEvt.Str("responseBody", bodySnippet)
		}
		logEvt.Msg("kcp org token validation returned unexpected status")

		return false, fmt.Errorf("kcp auth check returned unexpected status %d", resp.StatusCode)
	}
}
