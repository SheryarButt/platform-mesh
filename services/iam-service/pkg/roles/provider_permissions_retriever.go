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

package roles

import (
	"fmt"
	"strings"

	"go.platform-mesh.io/iam-service/pkg/graph"
)

type ProviderPermissionsRetriever struct {
	cache *ProviderPermissionsCache
}

func NewProviderPermissionsRetriever(cache *ProviderPermissionsCache) *ProviderPermissionsRetriever {
	return &ProviderPermissionsRetriever{
		cache: cache,
	}
}

func (r *ProviderPermissionsRetriever) GetRoleDefinitions(rctx graph.ResourceContext) ([]RoleDefinition, error) {
	groupKindKey := buildGroupKindKey(rctx.Group, rctx.Kind)
	roles := r.cache.GetRoles(groupKindKey)
	if roles == nil {
		return []RoleDefinition{}, nil
	}
	return roles, nil
}

// buildGroupKindKey constructs a lookup key from group and kind.
// Format: "{kind-lowercase}.{group}" or just "{kind-lowercase}" if group is empty.
func buildGroupKindKey(group, kind string) string {
	// Normalize kind to lowercase for consistent lookup
	kind = strings.ToLower(kind)
	if group == "" {
		return kind
	}
	return fmt.Sprintf("%s.%s", kind, group)
}
