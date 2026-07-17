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
	"slices"
	"sync"

	pmprovidersv1alpha1 "go.platform-mesh.io/apis/providers/v1alpha1"
	"go.platform-mesh.io/golang-commons/logger"

	toolscache "k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/multicluster-provider/pkg/provider"
)

type ProviderPermissionsCache struct {
	mu sync.RWMutex
	// roles maps GroupResource (e.g. "httpbin.orchestrate.platform-mesh.io") to role definitions
	roles map[string][]RoleDefinition
}

func NewProviderPermissionsCache(log *logger.Logger) *ProviderPermissionsCache {
	return &ProviderPermissionsCache{
		roles: make(map[string][]RoleDefinition),
	}
}

func (c *ProviderPermissionsCache) SetupWithProvider(p *provider.Provider) error {
	informer, err := p.GetAggregateInformer(&pmprovidersv1alpha1.ProviderPermissions{})
	if err != nil {
		return err
	}

	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if pp, ok := obj.(*pmprovidersv1alpha1.ProviderPermissions); ok {
				c.handleAdd(pp)
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			oldPP, ok1 := oldObj.(*pmprovidersv1alpha1.ProviderPermissions)
			newPP, ok2 := newObj.(*pmprovidersv1alpha1.ProviderPermissions)
			if ok1 && ok2 {
				c.handleUpdate(oldPP, newPP)
			}
		},
		DeleteFunc: func(obj any) {
			if pp, ok := obj.(*pmprovidersv1alpha1.ProviderPermissions); ok {
				c.handleDelete(pp)
				return
			}
		},
	})
	return err
}

func (c *ProviderPermissionsCache) GetRoles(groupResource string) []RoleDefinition {
	c.mu.RLock()
	defer c.mu.RUnlock()

	roles, ok := c.roles[groupResource]
	if !ok {
		return nil
	}
	return roles
}

func (c *ProviderPermissionsCache) handleAdd(pp *pmprovidersv1alpha1.ProviderPermissions) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.addRoles(pp.Spec.Roles)
}

func (c *ProviderPermissionsCache) handleUpdate(oldPP, newPP *pmprovidersv1alpha1.ProviderPermissions) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, rr := range oldPP.Spec.Roles {
		c.removeRoles(rr.GroupResource, rr.Roles)
	}
	c.addRoles(newPP.Spec.Roles)
}

func (c *ProviderPermissionsCache) addRoles(resourceRoles []pmprovidersv1alpha1.ResourceRoles) {
	for _, rr := range resourceRoles {
		for _, r := range rr.Roles {
			c.roles[rr.GroupResource] = append(c.roles[rr.GroupResource], RoleDefinition{
				ID:          r.ID,
				DisplayName: r.DisplayName,
				Description: r.Description,
			})
		}
	}
}

func (c *ProviderPermissionsCache) handleDelete(pp *pmprovidersv1alpha1.ProviderPermissions) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, rr := range pp.Spec.Roles {
		c.removeRoles(rr.GroupResource, rr.Roles)
	}
}

func (c *ProviderPermissionsCache) removeRoles(groupResource string, toRemove []pmprovidersv1alpha1.RoleDefinition) {
	existing := c.roles[groupResource]
	if len(existing) == 0 {
		return
	}

	remaining := slices.DeleteFunc(existing, func(r RoleDefinition) bool {
		return slices.ContainsFunc(toRemove, func(rm pmprovidersv1alpha1.RoleDefinition) bool {
			return rm.ID == r.ID
		})
	})

	if len(remaining) == 0 {
		delete(c.roles, groupResource)
	} else {
		c.roles[groupResource] = remaining
	}
}
