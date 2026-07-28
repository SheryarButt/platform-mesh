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

package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-logr/logr"
	openfgav1 "github.com/openfga/api/proto/openfga/v1"

	"go.platform-mesh.io/rebac-authz-webhook/pkg/clustercache"
	"go.platform-mesh.io/rebac-authz-webhook/pkg/handler/contextual"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)


// BatchHandler handles batch authorization requests.
type BatchHandler struct {
	fga          openfgav1.OpenFGAServiceClient
	clusterCache clustercache.Provider
	log          logr.Logger
}

// New creates a new BatchHandler.
func New(log logr.Logger, fga openfgav1.OpenFGAServiceClient, clusterCache clustercache.Provider) *BatchHandler {
	return &BatchHandler{
		fga:          fga,
		clusterCache: clusterCache,
		log:          log.WithName("batch-authz"),
	}
}

// ServeHTTP implements http.Handler.
func (h *BatchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Body == nil || r.Body == http.NoBody {
		h.writeError(w, http.StatusBadRequest, "request body is empty")
		return
	}
	defer r.Body.Close() //nolint:errcheck

	if contentType := r.Header.Get("Content-Type"); contentType != "application/json" {
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("content-type=%s, expected application/json", contentType))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.log.Error(err, "failed to read request body")
		h.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req BatchAuthzRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.log.Error(err, "failed to unmarshal request")
		h.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if len(req.Items) == 0 {
		h.writeError(w, http.StatusBadRequest, "items array is empty")
		return
	}

	h.log.Info("processing batch authorization request", "itemCount", len(req.Items))

	response := h.processBatch(ctx, req)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.log.Error(err, "failed to encode response")
	}
}

// processBatch processes all items in the batch request.
func (h *BatchHandler) processBatch(ctx context.Context, req BatchAuthzRequest) BatchAuthzResponse {
	results := make(map[string]bool, len(req.Items))
	for _, item := range req.Items {
		results[item.ID] = false
	}

	checks, storeID := h.buildChecks(req.Items)

	if len(checks) == 0 || storeID == "" {
		return h.buildResponse(results)
	}

	batchReq := &openfgav1.BatchCheckRequest{
		StoreId: storeID,
		Checks:  checks,
	}

	h.log.V(5).Info("calling BatchCheck", "storeID", storeID, "checkCount", len(checks))

	batchResp, err := h.fga.BatchCheck(ctx, batchReq)
	if err != nil {
		h.log.Error(err, "BatchCheck failed", "storeID", storeID)
		return h.buildResponse(results)
	}

	for id, singleResult := range batchResp.Result {
		if singleResult.GetError() != nil {
			h.log.Error(nil, "FGA check error", "id", id, "error", singleResult.GetError().GetMessage())
			continue
		}
		results[id] = singleResult.GetAllowed()
	}

	return h.buildResponse(results)
}

// buildChecks builds FGA BatchCheckItems from request items.
func (h *BatchHandler) buildChecks(items []BatchAuthzItem) ([]*openfgav1.BatchCheckItem, string) {
	checks := make([]*openfgav1.BatchCheckItem, 0, len(items))
	var storeID string

	for _, item := range items {
		// Skip items with missing required fields
		if item.ID == "" || item.ResourceAttributes == nil || item.User == "" || item.ClusterName == "" {
			continue
		}

		clusterInfo, ok := h.clusterCache.Get(multicluster.ClusterName(item.ClusterName))
		if !ok {
			continue
		}

		if storeID == "" {
			storeID = clusterInfo.StoreID
		}

		checkInput, err := contextual.BuildCheckInput(item.ResourceAttributes, item.User, item.ClusterName, clusterInfo)
		if err != nil {
			h.log.Error(err, "failed to build check input", "id", item.ID)
			continue
		}

		fgaItem := &openfgav1.BatchCheckItem{
			CorrelationId: item.ID,
			TupleKey: &openfgav1.CheckRequestTupleKey{
				Object:   checkInput.Object,
				Relation: checkInput.Relation,
				User:     checkInput.User,
			},
		}
		if len(checkInput.ContextualTuples) > 0 {
			fgaItem.ContextualTuples = &openfgav1.ContextualTupleKeys{
				TupleKeys: checkInput.ContextualTuples,
			}
		}
		checks = append(checks, fgaItem)
	}

	return checks, storeID
}

// buildResponse converts the results map to BatchAuthzResponse.
func (h *BatchHandler) buildResponse(results map[string]bool) BatchAuthzResponse {
	response := BatchAuthzResponse{
		Results: make([]BatchAuthzResult, 0, len(results)),
	}
	for id, allowed := range results {
		response.Results = append(response.Results, BatchAuthzResult{
			ID:      id,
			Allowed: allowed,
		})
	}
	return response
}

// writeError writes an error response.
func (h *BatchHandler) writeError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
