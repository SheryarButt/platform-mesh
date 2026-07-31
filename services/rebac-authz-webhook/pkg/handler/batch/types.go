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
	authorizationv1 "k8s.io/api/authorization/v1"
)

// BatchAuthzRequest is the input for the batch authorization endpoint.
type BatchAuthzRequest struct {
	// Items contains the list of authorization checks to perform.
	Items []BatchAuthzItem `json:"items"`
}

// BatchAuthzItem represents a single authorization check in a batch.
type BatchAuthzItem struct {
	// ID is a client-provided correlation ID for matching responses to requests.
	ID string `json:"id"`

	// User is the user making the request.
	User string `json:"user"`

	// ClusterPath is the path to the cluster where the resource is located.
	ClusterPath string `json:"clusterPath"`

	// ResourceAttributes describes the resource being accessed.
	ResourceAttributes *authorizationv1.ResourceAttributes `json:"resourceAttributes"`
}

// BatchAuthzResponse is the output from the batch authorization endpoint.
type BatchAuthzResponse struct {
	// Results contains the authorization result for each item in the request.
	// The order matches the order of items in the request.
	Results []BatchAuthzResult `json:"results"`
}

// BatchAuthzResult represents the result of a single authorization check.
type BatchAuthzResult struct {
	// ID is the correlation ID from the request item (or index if not provided).
	ID string `json:"id"`

	// Allowed indicates whether the request is authorized.
	Allowed bool `json:"allowed"`
}
