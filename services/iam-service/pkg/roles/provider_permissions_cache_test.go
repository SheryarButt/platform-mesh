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
	"testing"

	"github.com/stretchr/testify/assert"

	pmprovidersv1alpha1 "go.platform-mesh.io/apis/providers/v1alpha1"
	"go.platform-mesh.io/golang-commons/logger"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestProviderPermissionsCache_HandleAdd(t *testing.T) {
	log, _ := logger.New(logger.DefaultConfig())
	cache := NewProviderPermissionsCache(log)

	pp := &pmprovidersv1alpha1.ProviderPermissions{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pp",
			UID:  types.UID("test-uid-1"),
		},
		Spec: pmprovidersv1alpha1.ProviderPermissionsSpec{
			Roles: []pmprovidersv1alpha1.ResourceRoles{
				{
					GroupResource: "orchestrate.platform-mesh.io.httpbin",
					Roles: []pmprovidersv1alpha1.RoleDefinition{
						{
							ID:          "admin",
							DisplayName: "Administrator",
							Description: "Full access to httpbin",
						},
						{
							ID:          "viewer",
							DisplayName: "Viewer",
							Description: "Read-only access",
						},
					},
				},
			},
		},
	}

	cache.handleAdd(pp)

	roles := cache.GetRoles("orchestrate.platform-mesh.io.httpbin")
	assert.Len(t, roles, 2)
	assert.Equal(t, "admin", roles[0].ID)
	assert.Equal(t, "Administrator", roles[0].DisplayName)
	assert.Equal(t, "viewer", roles[1].ID)
}

func TestProviderPermissionsCache_HandleUpdate(t *testing.T) {
	log, _ := logger.New(logger.DefaultConfig())
	cache := NewProviderPermissionsCache(log)

	oldPP := &pmprovidersv1alpha1.ProviderPermissions{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pp",
			UID:  types.UID("test-uid-1"),
		},
		Spec: pmprovidersv1alpha1.ProviderPermissionsSpec{
			Roles: []pmprovidersv1alpha1.ResourceRoles{
				{
					GroupResource: "orchestrate.platform-mesh.io.httpbin",
					Roles: []pmprovidersv1alpha1.RoleDefinition{
						{ID: "admin", DisplayName: "Administrator", Description: "Full access"},
						{ID: "viewer", DisplayName: "Viewer", Description: "Read-only"},
					},
				},
			},
		},
	}

	cache.handleAdd(oldPP)
	assert.Len(t, cache.GetRoles("orchestrate.platform-mesh.io.httpbin"), 2)

	newPP := &pmprovidersv1alpha1.ProviderPermissions{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pp",
			UID:  types.UID("test-uid-1"),
		},
		Spec: pmprovidersv1alpha1.ProviderPermissionsSpec{
			Roles: []pmprovidersv1alpha1.ResourceRoles{
				{
					GroupResource: "orchestrate.platform-mesh.io.httpbin",
					Roles: []pmprovidersv1alpha1.RoleDefinition{
						{ID: "admin", DisplayName: "Administrator", Description: "Full access"},
						{ID: "editor", DisplayName: "Editor", Description: "Can edit"},
						{ID: "viewer", DisplayName: "Viewer", Description: "Read-only"},
					},
				},
			},
		},
	}

	cache.handleUpdate(oldPP, newPP)

	roles := cache.GetRoles("orchestrate.platform-mesh.io.httpbin")
	assert.Len(t, roles, 3)

	roleIDs := make([]string, len(roles))
	for i, r := range roles {
		roleIDs[i] = r.ID
	}
	assert.ElementsMatch(t, []string{"admin", "editor", "viewer"}, roleIDs)
}

func TestProviderPermissionsCache_HandleDelete(t *testing.T) {
	log, _ := logger.New(logger.DefaultConfig())
	cache := NewProviderPermissionsCache(log)

	pp := &pmprovidersv1alpha1.ProviderPermissions{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pp",
			UID:  types.UID("test-uid-1"),
		},
		Spec: pmprovidersv1alpha1.ProviderPermissionsSpec{
			Roles: []pmprovidersv1alpha1.ResourceRoles{
				{
					GroupResource: "orchestrate.platform-mesh.io.httpbin",
					Roles: []pmprovidersv1alpha1.RoleDefinition{
						{ID: "admin", DisplayName: "Administrator", Description: "Full access"},
					},
				},
			},
		},
	}

	cache.handleAdd(pp)
	assert.Len(t, cache.GetRoles("orchestrate.platform-mesh.io.httpbin"), 1)

	cache.handleDelete(pp)

	roles := cache.GetRoles("orchestrate.platform-mesh.io.httpbin")
	assert.Nil(t, roles)
}

func TestProviderPermissionsCache_MultipleProviderPermissions(t *testing.T) {
	log, _ := logger.New(logger.DefaultConfig())
	cache := NewProviderPermissionsCache(log)

	pp1 := &pmprovidersv1alpha1.ProviderPermissions{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pp-1",
			UID:  types.UID("uid-1"),
		},
		Spec: pmprovidersv1alpha1.ProviderPermissionsSpec{
			Roles: []pmprovidersv1alpha1.ResourceRoles{
				{
					GroupResource: "orchestrate.platform-mesh.io.httpbin",
					Roles: []pmprovidersv1alpha1.RoleDefinition{
						{ID: "admin", DisplayName: "Admin", Description: "Full access"},
					},
				},
			},
		},
	}

	pp2 := &pmprovidersv1alpha1.ProviderPermissions{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pp-2",
			UID:  types.UID("uid-2"),
		},
		Spec: pmprovidersv1alpha1.ProviderPermissionsSpec{
			Roles: []pmprovidersv1alpha1.ResourceRoles{
				{
					GroupResource: "orchestrate.platform-mesh.io.httpbin",
					Roles: []pmprovidersv1alpha1.RoleDefinition{
						{ID: "viewer", DisplayName: "Viewer", Description: "Read-only"},
					},
				},
			},
		},
	}

	cache.handleAdd(pp1)
	cache.handleAdd(pp2)

	roles := cache.GetRoles("orchestrate.platform-mesh.io.httpbin")
	assert.Len(t, roles, 2)

	roleIDs := make([]string, len(roles))
	for i, r := range roles {
		roleIDs[i] = r.ID
	}
	assert.ElementsMatch(t, []string{"admin", "viewer"}, roleIDs)

	// Delete pp1, should only leave pp2's roles
	cache.handleDelete(pp1)

	roles = cache.GetRoles("orchestrate.platform-mesh.io.httpbin")
	assert.Len(t, roles, 1)
	assert.Equal(t, "viewer", roles[0].ID)
}

func TestProviderPermissionsCache_GetRoles_NotFound(t *testing.T) {
	log, _ := logger.New(logger.DefaultConfig())
	cache := NewProviderPermissionsCache(log)

	roles := cache.GetRoles("non.existent.resource")
	assert.Nil(t, roles)
}
