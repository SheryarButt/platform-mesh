package validation

import (
	"fmt"
	"strings"
	"sync"
)

type EntityTypeRegistry struct {
	mu     sync.RWMutex
	types  map[string]int            // entity type → reference count
	owners map[string]map[string]bool // owner key → set of entity types it contributes
}

func NewEntityTypeRegistry() *EntityTypeRegistry {
	return &EntityTypeRegistry{
		types:  map[string]int{"global": 1},
		owners: make(map[string]map[string]bool),
	}
}

func (r *EntityTypeRegistry) Bulkload(configs []ContentConfiguration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.types = map[string]int{"global": 1}
	r.owners = make(map[string]map[string]bool)
	for _, cc := range configs {
		for et := range collectDefinedEntityTypes(cc) {
			r.types[et]++
		}
	}
}

func (r *EntityTypeRegistry) BulkloadWithOwners(configs map[string]ContentConfiguration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.types = map[string]int{"global": 1}
	r.owners = make(map[string]map[string]bool)
	for owner, cc := range configs {
		defined := collectDefinedEntityTypes(cc)
		r.owners[owner] = defined
		for et := range defined {
			r.types[et]++
		}
	}
}

func (r *EntityTypeRegistry) LoadForOwner(owner string, cc ContentConfiguration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	newTypes := collectDefinedEntityTypes(cc)

	if oldTypes, exists := r.owners[owner]; exists {
		for et := range oldTypes {
			if !newTypes[et] {
				if r.types[et] <= 1 {
					delete(r.types, et)
				} else {
					r.types[et]--
				}
			}
		}
	}

	for et := range newTypes {
		if r.owners[owner] == nil || !r.owners[owner][et] {
			r.types[et]++
		}
	}

	r.owners[owner] = newTypes
}

func (r *EntityTypeRegistry) RemoveOwner(owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	oldTypes, exists := r.owners[owner]
	if !exists {
		return
	}

	for et := range oldTypes {
		if r.types[et] <= 1 {
			delete(r.types, et)
		} else {
			r.types[et]--
		}
	}
	delete(r.owners, owner)
}

// Validate checks all entityType references in a ContentConfiguration against
// the registry. Returns errors for unknown entity types.
func (r *EntityTypeRegistry) Validate(cc ContentConfiguration) []error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	selfDefined := collectDefinedEntityTypes(cc)
	refs := collectReferencedEntityTypes(cc)
	var errs []error
	for _, ref := range refs {
		normalized := normalizeEntityType(ref)
		if normalized == "" {
			continue
		}
		if r.types[normalized] == 0 && !selfDefined[normalized] {
			errs = append(errs, fmt.Errorf("unknown entityType %q", ref))
		}
	}
	return errs
}

// KnownTypes returns a set of known entity types (for testing).
func (r *EntityTypeRegistry) KnownTypes() map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]bool, len(r.types))
	for k := range r.types {
		result[k] = true
	}
	return result
}

// collectDefinedEntityTypes walks all nodes in a ContentConfiguration and
// collects the entity types defined via defineEntity segments.
func collectDefinedEntityTypes(cc ContentConfiguration) map[string]bool {
	types := make(map[string]bool)
	defaultEntityType := ""
	if cc.LuigiConfigFragment.Data.NodeDefaults != nil {
		defaultEntityType = cc.LuigiConfigFragment.Data.NodeDefaults.EntityType
	}
	for _, node := range cc.LuigiConfigFragment.Data.Nodes {
		collectDefinedEntityTypesFromNode(node, defaultEntityType, types)
	}
	return types
}

func collectDefinedEntityTypesFromNode(node Node, defaultEntityType string, types map[string]bool) {
	entityType := node.EntityType
	if entityType == "" {
		entityType = defaultEntityType
	}

	if node.DefineEntity != nil && node.DefineEntity.Id != "" {
		fullType := buildEntityTypeName(entityType, node.DefineEntity.Id)
		types[fullType] = true
	}

	// Determine the entity type context for children
	childEntityType := entityType
	if node.DefineEntity != nil && node.DefineEntity.Id != "" {
		childEntityType = buildEntityTypeName(entityType, node.DefineEntity.Id)
	}

	for _, child := range node.Children {
		collectDefinedEntityTypesFromNode(child, childEntityType, types)
	}
}

// buildEntityTypeName builds the full entity type name from a parent entity type
// and a defineEntity id. Per the docs, "global" is excluded from the chain.
func buildEntityTypeName(parentEntityType, defineEntityId string) string {
	normalized := normalizeEntityType(parentEntityType)
	if normalized == "" || normalized == "global" {
		return defineEntityId
	}
	return normalized + "." + defineEntityId
}

// collectReferencedEntityTypes collects all entityType values from
// nodeDefaults and nodes (recursively).
func collectReferencedEntityTypes(cc ContentConfiguration) []string {
	var refs []string
	if cc.LuigiConfigFragment.Data.NodeDefaults != nil && cc.LuigiConfigFragment.Data.NodeDefaults.EntityType != "" {
		refs = append(refs, cc.LuigiConfigFragment.Data.NodeDefaults.EntityType)
	}
	for _, node := range cc.LuigiConfigFragment.Data.Nodes {
		collectReferencedEntityTypesFromNode(node, &refs)
	}
	return refs
}

func collectReferencedEntityTypesFromNode(node Node, refs *[]string) {
	if node.EntityType != "" {
		*refs = append(*refs, node.EntityType)
	}
	for _, child := range node.Children {
		collectReferencedEntityTypesFromNode(child, refs)
	}
}

// normalizeEntityType strips any "::" suffix (e.g. "project.overview::compound"
// becomes "project.overview").
func normalizeEntityType(entityType string) string {
	if base, _, found := strings.Cut(entityType, "::"); found {
		return base
	}
	return entityType
}
