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

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.platform-mesh.io/extension-manager-operator/pkg/validation"
	"go.platform-mesh.io/golang-commons/logger"
)

func initLog() *logger.Logger {
	logConfig := logger.DefaultConfig()
	logConfig.Name = "router_test"
	logConfig.Level = "DEBUG"
	logConfig.NoJSON = false
	log, _ := logger.New(logConfig)
	return log
}

func TestCreateRouter(t *testing.T) {
	tests := []struct {
		name       string
		isLocal    bool
		method     string
		path       string
		expectCode int
		expectCORS bool
		reqBody    string
	}{
		{
			name:       "validate endpoint exists",
			isLocal:    false,
			method:     http.MethodPost,
			path:       "/validate",
			expectCode: http.StatusOK,
			reqBody: `{
				"contentType": "json",
				"contentConfiguration":"{\"luigiConfigFragment\": {\"data\": {\"nodeDefaults\": {\"entityType\": \"global\",\"isolateView\": true},\"nodes\": [{\"entityType\": \"global\",\"icon\": \"home\",\"label\": \"Overview\",\"pathSegment\": \"home\"}],\"texts\": [{\"locale\": \"de\",\"textDictionary\": {\"hello\": \"Hallo\"}}]}},\"name\": \"overview\"}"}"
			}`,
		},
		{
			name:       "wrong method not allowed",
			isLocal:    false,
			method:     http.MethodGet,
			path:       "/validate",
			expectCode: http.StatusMethodNotAllowed,
			reqBody: `{
				"contentType": "json",
				"contentConfiguration":"{\"luigiConfigFragment\": {\"data\": {\"nodeDefaults\": {\"entityType\": \"global\",\"isolateView\": true},\"nodes\": [{\"entityType\": \"global\",\"icon\": \"home\",\"label\": \"Overview\",\"pathSegment\": \"home\"}],\"texts\": [{\"locale\": \"de\",\"textDictionary\": {\"hello\": \"Hallo\"}}]}},\"name\": \"overview\"}"}"
			}`,
		},
		{
			name:       "local setup OK",
			isLocal:    true,
			method:     http.MethodPost,
			path:       "/validate",
			expectCode: http.StatusOK,
			reqBody: `{
				"contentType": "json",
				"contentConfiguration":"{\"luigiConfigFragment\": {\"data\": {\"nodeDefaults\": {\"entityType\": \"global\",\"isolateView\": true},\"nodes\": [{\"entityType\": \"global\",\"icon\": \"home\",\"label\": \"Overview\",\"pathSegment\": \"home\"}],\"texts\": [{\"locale\": \"de\",\"textDictionary\": {\"hello\": \"Hallo\"}}]}},\"name\": \"overview\"}"}"
			}`,
		},
		{
			name:       "invalid request body",
			isLocal:    true,
			method:     http.MethodPost,
			path:       "/validate",
			expectCode: http.StatusInternalServerError,
			reqBody: `{
				"contentType": "json",
				"contentConfiguration":"{\"luigiConfigFragment\": {\"dat
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := initLog()

			validator := validation.NewContentConfiguration()

			router := CreateRouter(tt.isLocal, log, validator, nil)
			assert.NotNil(t, router)

			req := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.reqBody))
			rr := httptest.NewRecorder()

			router.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectCode, rr.Code)
		})
	}
}

func TestCreateRouter_SelfReferencingCC(t *testing.T) {
	registry := validation.NewEntityTypeRegistry()

	log := initLog()
	validator := validation.NewContentConfiguration()
	router := CreateRouter(false, log, validator, registry)

	// CC defines "project" via defineEntity and references it on a child node.
	// The registry only knows "global", so without the self-referencing fix this would fail.
	reqBody := `{
		"contentType": "json",
		"contentConfiguration": "{\"name\": \"self-ref\", \"luigiConfigFragment\": {\"data\": {\"nodes\": [{\"entityType\": \"global\", \"pathSegment\": \"root\", \"defineEntity\": {\"id\": \"project\"}, \"children\": [{\"entityType\": \"project\", \"pathSegment\": \"overview\"}]}]}}}"
	}`

	req := httptest.NewRequest(http.MethodPost, "/validate", bytes.NewBufferString(reqBody))
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp Response
	err := json.NewDecoder(rr.Body).Decode(&resp)
	assert.NoError(t, err)
	assert.Empty(t, resp.ValidationErrors, "self-referencing CC should pass entity type validation")
	assert.NotEmpty(t, resp.ParsedConfiguration)
}

func TestCreateRouter_WithEntityTypeRegistry(t *testing.T) {
	registry := validation.NewEntityTypeRegistry()
	registry.Bulkload([]validation.ContentConfiguration{
		{
			LuigiConfigFragment: validation.LuigiConfigFragment{
				Data: validation.LuigiConfigData{
					Nodes: []validation.Node{
						{EntityType: "global", DefineEntity: &validation.DefineEntity{Id: "project"}},
					},
				},
			},
		},
	})

	tests := []struct {
		name             string
		reqBody          string
		expectCode       int
		expectValidation bool
	}{
		{
			name: "valid entityType with registry",
			reqBody: `{
				"contentType": "json",
				"contentConfiguration": "{\"name\": \"test\", \"luigiConfigFragment\": {\"data\": {\"nodes\": [{\"entityType\": \"global\", \"pathSegment\": \"home\"}]}}}"
			}`,
			expectCode: http.StatusOK,
		},
		{
			name: "unknown entityType with registry",
			reqBody: `{
				"contentType": "json",
				"contentConfiguration": "{\"name\": \"test\", \"luigiConfigFragment\": {\"data\": {\"nodes\": [{\"entityType\": \"nonexistent\", \"pathSegment\": \"home\"}]}}}"
			}`,
			expectCode:       http.StatusUnprocessableEntity,
			expectValidation: true,
		},
		{
			name: "known entityType project with registry",
			reqBody: `{
				"contentType": "json",
				"contentConfiguration": "{\"name\": \"test\", \"luigiConfigFragment\": {\"data\": {\"nodes\": [{\"entityType\": \"project\", \"pathSegment\": \"home\"}]}}}"
			}`,
			expectCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := initLog()
			validator := validation.NewContentConfiguration()

			router := CreateRouter(false, log, validator, registry)
			assert.NotNil(t, router)

			req := httptest.NewRequest(http.MethodPost, "/validate", bytes.NewBufferString(tt.reqBody))
			rr := httptest.NewRecorder()

			router.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectCode, rr.Code)

			if tt.expectValidation {
				var resp Response
				err := json.NewDecoder(rr.Body).Decode(&resp)
				assert.NoError(t, err)
				assert.NotEmpty(t, resp.ValidationErrors)
			}
		})
	}
}
