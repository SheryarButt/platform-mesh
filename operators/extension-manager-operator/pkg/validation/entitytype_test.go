package validation

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEntityTypeRegistry(t *testing.T) {
	registry := NewEntityTypeRegistry()
	types := registry.KnownTypes()

	assert.True(t, types["global"])
	assert.Len(t, types, 1)
}

func TestCollectDefinedEntityTypes(t *testing.T) {
	tests := []struct {
		name     string
		cc       ContentConfiguration
		expected map[string]bool
	}{
		{
			name: "simple defineEntity under global",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{
								EntityType:   "global",
								DefineEntity: &DefineEntity{Id: "main"},
							},
						},
					},
				},
			},
			expected: map[string]bool{"main": true},
		},
		{
			name: "defineEntity under non-global entityType",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{
								EntityType:   "project",
								DefineEntity: &DefineEntity{Id: "custom"},
							},
						},
					},
				},
			},
			expected: map[string]bool{"project.custom": true},
		},
		{
			name: "nested defineEntity",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{
								EntityType:   "project",
								DefineEntity: &DefineEntity{Id: "component"},
								Children: []Node{
									{
										DefineEntity: &DefineEntity{Id: "overview"},
									},
								},
							},
						},
					},
				},
			},
			expected: map[string]bool{
				"project.component":          true,
				"project.component.overview": true,
			},
		},
		{
			name: "no defineEntity",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{
								EntityType: "global",
							},
						},
					},
				},
			},
			expected: map[string]bool{},
		},
		{
			name: "defineEntity with nodeDefaults entityType",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						NodeDefaults: &NodeDefaults{EntityType: "project"},
						Nodes: []Node{
							{
								DefineEntity: &DefineEntity{Id: "custom"},
							},
						},
					},
				},
			},
			expected: map[string]bool{"project.custom": true},
		},
		{
			name: "defineEntity with empty id is ignored",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{
								EntityType:   "global",
								DefineEntity: &DefineEntity{Id: ""},
							},
						},
					},
				},
			},
			expected: map[string]bool{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := collectDefinedEntityTypes(tt.cc)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCollectReferencedEntityTypes(t *testing.T) {
	tests := []struct {
		name     string
		cc       ContentConfiguration
		expected []string
	}{
		{
			name: "single node",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: "global"},
						},
					},
				},
			},
			expected: []string{"global"},
		},
		{
			name: "nodeDefaults and node",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						NodeDefaults: &NodeDefaults{EntityType: "project"},
						Nodes: []Node{
							{EntityType: "team"},
						},
					},
				},
			},
			expected: []string{"project", "team"},
		},
		{
			name: "nested children",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{
								EntityType: "project",
								Children: []Node{
									{EntityType: "project.component"},
								},
							},
						},
					},
				},
			},
			expected: []string{"project", "project.component"},
		},
		{
			name: "empty entityType is not collected",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: ""},
						},
					},
				},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := collectReferencedEntityTypes(tt.cc)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEntityTypeRegistry_Load(t *testing.T) {
	registry := NewEntityTypeRegistry()

	configs := []ContentConfiguration{
		{
			LuigiConfigFragment: LuigiConfigFragment{
				Data: LuigiConfigData{
					Nodes: []Node{
						{
							EntityType:   "global",
							DefineEntity: &DefineEntity{Id: "project"},
						},
					},
				},
			},
		},
		{
			LuigiConfigFragment: LuigiConfigFragment{
				Data: LuigiConfigData{
					Nodes: []Node{
						{
							EntityType:   "global",
							DefineEntity: &DefineEntity{Id: "team"},
						},
					},
				},
			},
		},
	}

	registry.Bulkload(configs)
	types := registry.KnownTypes()

	assert.True(t, types["global"])
	assert.True(t, types["project"])
	assert.True(t, types["team"])
	assert.Len(t, types, 3)
}

func TestEntityTypeRegistry_Update(t *testing.T) {
	registry := NewEntityTypeRegistry()

	cc := ContentConfiguration{
		LuigiConfigFragment: LuigiConfigFragment{
			Data: LuigiConfigData{
				Nodes: []Node{
					{
						EntityType:   "project",
						DefineEntity: &DefineEntity{Id: "component"},
					},
				},
			},
		},
	}

	registry.LoadForOwner("uid-1", cc)
	types := registry.KnownTypes()

	assert.True(t, types["global"])
	assert.True(t, types["project.component"])
	assert.Len(t, types, 2)
}

func TestEntityTypeRegistry_Remove(t *testing.T) {
	registry := NewEntityTypeRegistry()

	cc := ContentConfiguration{
		LuigiConfigFragment: LuigiConfigFragment{
			Data: LuigiConfigData{
				Nodes: []Node{
					{
						EntityType:   "global",
						DefineEntity: &DefineEntity{Id: "project"},
					},
				},
			},
		},
	}

	registry.LoadForOwner("uid-1", cc)
	require.True(t, registry.KnownTypes()["project"])

	registry.RemoveOwner("uid-1")
	assert.False(t, registry.KnownTypes()["project"])
	assert.True(t, registry.KnownTypes()["global"])
}

func TestEntityTypeRegistry_Remove_RefCounting(t *testing.T) {
	registry := NewEntityTypeRegistry()

	cc := ContentConfiguration{
		LuigiConfigFragment: LuigiConfigFragment{
			Data: LuigiConfigData{
				Nodes: []Node{
					{EntityType: "global", DefineEntity: &DefineEntity{Id: "project"}},
				},
			},
		},
	}

	registry.LoadForOwner("uid-1", cc)
	registry.LoadForOwner("uid-2", cc)
	require.True(t, registry.KnownTypes()["project"])

	// Remove one — "project" should still be known
	registry.RemoveOwner("uid-1")
	assert.True(t, registry.KnownTypes()["project"])

	// Validate still passes
	errs := registry.Validate(ContentConfiguration{
		LuigiConfigFragment: LuigiConfigFragment{
			Data: LuigiConfigData{
				Nodes: []Node{{EntityType: "project"}},
			},
		},
	})
	assert.Empty(t, errs)

	// Remove the second — now "project" should be gone
	registry.RemoveOwner("uid-2")
	assert.False(t, registry.KnownTypes()["project"])

	// Validate now fails
	errs = registry.Validate(ContentConfiguration{
		LuigiConfigFragment: LuigiConfigFragment{
			Data: LuigiConfigData{
				Nodes: []Node{{EntityType: "project"}},
			},
		},
	})
	assert.Len(t, errs, 1)
}

func TestEntityTypeRegistry_Validate(t *testing.T) {
	registry := NewEntityTypeRegistry()
	registry.Bulkload([]ContentConfiguration{
		{
			LuigiConfigFragment: LuigiConfigFragment{
				Data: LuigiConfigData{
					Nodes: []Node{
						{EntityType: "global", DefineEntity: &DefineEntity{Id: "project"}},
						{EntityType: "global", DefineEntity: &DefineEntity{Id: "team"}},
						{EntityType: "project", DefineEntity: &DefineEntity{Id: "component"}},
						{EntityType: "project", DefineEntity: &DefineEntity{Id: "team"}},
					},
				},
			},
		},
	})

	tests := []struct {
		name        string
		cc          ContentConfiguration
		expectCount int
	}{
		{
			name: "all valid",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: "global"},
							{EntityType: "project"},
							{EntityType: "team"},
							{EntityType: "project.component"},
							{EntityType: "project.team"},
						},
					},
				},
			},
			expectCount: 0,
		},
		{
			name: "unknown entityType",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: "nonexistent"},
						},
					},
				},
			},
			expectCount: 1,
		},
		{
			name: "compound suffix is stripped",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: "project.component::compound"},
						},
					},
				},
			},
			expectCount: 0,
		},
		{
			name: "unknown compound base",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: "nonexistent::compound"},
						},
					},
				},
			},
			expectCount: 1,
		},
		{
			name: "empty entityType is skipped",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: ""},
						},
					},
				},
			},
			expectCount: 0,
		},
		{
			name: "global is always valid",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						NodeDefaults: &NodeDefaults{EntityType: "global"},
						Nodes: []Node{
							{EntityType: "global"},
						},
					},
				},
			},
			expectCount: 0,
		},
		{
			name: "multiple unknown",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: "foo"},
							{EntityType: "bar"},
						},
					},
				},
			},
			expectCount: 2,
		},
		{
			name: "self-referencing CC defines and uses own type",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{
								EntityType:   "global",
								DefineEntity: &DefineEntity{Id: "mytype"},
								Children: []Node{
									{EntityType: "mytype"},
								},
							},
						},
					},
				},
			},
			expectCount: 0,
		},
		{
			name: "self-referencing CC with nested defineEntity",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{
								EntityType:   "project",
								DefineEntity: &DefineEntity{Id: "sub"},
								Children: []Node{
									{EntityType: "project.sub"},
								},
							},
						},
					},
				},
			},
			expectCount: 0,
		},
		{
			name: "self-referencing but also uses truly unknown type",
			cc: ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{
								EntityType:   "global",
								DefineEntity: &DefineEntity{Id: "mytype"},
								Children: []Node{
									{EntityType: "mytype"},
									{EntityType: "doesnotexist"},
								},
							},
						},
					},
				},
			},
			expectCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := registry.Validate(tt.cc)
			assert.Len(t, errs, tt.expectCount)
		})
	}
}

func TestNormalizeEntityType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"global", "global"},
		{"project", "project"},
		{"project.component", "project.component"},
		{"project.overview::compound", "project.overview"},
		{"team.overview::compound", "team.overview"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizeEntityType(tt.input))
		})
	}
}

func TestBuildEntityTypeName(t *testing.T) {
	tests := []struct {
		name           string
		parentType     string
		defineEntityId string
		expected       string
	}{
		{"global parent", "global", "main", "main"},
		{"empty parent", "", "main", "main"},
		{"non-global parent", "project", "component", "project.component"},
		{"nested parent", "project.component", "overview", "project.component.overview"},
		{"global compound parent", "global::compound", "test", "test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, buildEntityTypeName(tt.parentType, tt.defineEntityId))
		})
	}
}

func TestEntityTypeRegistry_ConcurrentAccess(t *testing.T) {
	registry := NewEntityTypeRegistry()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			cc := ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: "global", DefineEntity: &DefineEntity{Id: "project"}},
						},
					},
				},
			}
			registry.LoadForOwner(fmt.Sprintf("uid-%d", i), cc)
		}()
		go func() {
			defer wg.Done()
			cc := ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: "project"},
						},
					},
				},
			}
			registry.Validate(cc)
		}()
	}
	wg.Wait()
}

func TestValidateEntityTypes_Integration(t *testing.T) {
	cC := NewContentConfiguration()
	registry := NewEntityTypeRegistry()
	registry.Bulkload([]ContentConfiguration{
		{
			LuigiConfigFragment: LuigiConfigFragment{
				Data: LuigiConfigData{
					Nodes: []Node{
						{EntityType: "global", DefineEntity: &DefineEntity{Id: "project"}},
						{EntityType: "global", DefineEntity: &DefineEntity{Id: "team"}},
					},
				},
			},
		},
	})

	input := []byte(`{
		"name": "test",
		"luigiConfigFragment": {
			"data": {
				"nodes": [{
					"entityType": "project",
					"pathSegment": "test"
				}]
			}
		}
	}`)

	merr := cC.ValidateEntityTypes(input, "json", registry)
	assert.Nil(t, merr)

	invalidInput := []byte(`{
		"name": "test",
		"luigiConfigFragment": {
			"data": {
				"nodes": [{
					"entityType": "nonexistent",
					"pathSegment": "test"
				}]
			}
		}
	}`)

	merr = cC.ValidateEntityTypes(invalidInput, "json", registry)
	require.NotNil(t, merr)
	assert.GreaterOrEqual(t, merr.Len(), 1)
	assert.Contains(t, merr.Error(), "nonexistent")
}

func TestEntityTypeRegistry_LoadForOwner_Idempotent(t *testing.T) {
	registry := NewEntityTypeRegistry()

	cc := ContentConfiguration{
		LuigiConfigFragment: LuigiConfigFragment{
			Data: LuigiConfigData{
				Nodes: []Node{
					{EntityType: "global", DefineEntity: &DefineEntity{Id: "project"}},
				},
			},
		},
	}

	registry.LoadForOwner("uid-1", cc)
	registry.LoadForOwner("uid-1", cc)
	registry.LoadForOwner("uid-1", cc)

	types := registry.KnownTypes()
	assert.True(t, types["project"])
	assert.True(t, types["global"])
	assert.Len(t, types, 2)

	// After removing once, project should be gone
	registry.RemoveOwner("uid-1")
	types = registry.KnownTypes()
	assert.False(t, types["project"])
	assert.True(t, types["global"])
}

func TestEntityTypeRegistry_LoadForOwner_UpdateTypes(t *testing.T) {
	registry := NewEntityTypeRegistry()

	cc1 := ContentConfiguration{
		LuigiConfigFragment: LuigiConfigFragment{
			Data: LuigiConfigData{
				Nodes: []Node{
					{EntityType: "global", DefineEntity: &DefineEntity{Id: "project"}},
				},
			},
		},
	}

	cc2 := ContentConfiguration{
		LuigiConfigFragment: LuigiConfigFragment{
			Data: LuigiConfigData{
				Nodes: []Node{
					{EntityType: "global", DefineEntity: &DefineEntity{Id: "team"}},
				},
			},
		},
	}

	registry.LoadForOwner("uid-1", cc1)
	assert.True(t, registry.KnownTypes()["project"])

	// Update the same owner with different types
	registry.LoadForOwner("uid-1", cc2)
	types := registry.KnownTypes()
	assert.False(t, types["project"], "old type should be removed")
	assert.True(t, types["team"], "new type should be added")
}

func TestEntityTypeRegistry_LoadForOwner_MultipleOwners(t *testing.T) {
	registry := NewEntityTypeRegistry()

	cc := ContentConfiguration{
		LuigiConfigFragment: LuigiConfigFragment{
			Data: LuigiConfigData{
				Nodes: []Node{
					{EntityType: "global", DefineEntity: &DefineEntity{Id: "project"}},
				},
			},
		},
	}

	registry.LoadForOwner("uid-1", cc)
	registry.LoadForOwner("uid-2", cc)

	// Both define "project", removing one should keep it
	registry.RemoveOwner("uid-1")
	assert.True(t, registry.KnownTypes()["project"])

	// Removing the second should remove it
	registry.RemoveOwner("uid-2")
	assert.False(t, registry.KnownTypes()["project"])
}

func TestEntityTypeRegistry_RemoveOwner_NonExistent(t *testing.T) {
	registry := NewEntityTypeRegistry()

	// Should not panic
	registry.RemoveOwner("nonexistent-uid")
	assert.True(t, registry.KnownTypes()["global"])
}

func TestEntityTypeRegistry_BulkloadWithOwners(t *testing.T) {
	registry := NewEntityTypeRegistry()

	configs := map[string]ContentConfiguration{
		"uid-1": {
			LuigiConfigFragment: LuigiConfigFragment{
				Data: LuigiConfigData{
					Nodes: []Node{
						{EntityType: "global", DefineEntity: &DefineEntity{Id: "project"}},
					},
				},
			},
		},
		"uid-2": {
			LuigiConfigFragment: LuigiConfigFragment{
				Data: LuigiConfigData{
					Nodes: []Node{
						{EntityType: "global", DefineEntity: &DefineEntity{Id: "team"}},
					},
				},
			},
		},
	}

	registry.BulkloadWithOwners(configs)
	types := registry.KnownTypes()
	assert.True(t, types["global"])
	assert.True(t, types["project"])
	assert.True(t, types["team"])
	assert.Len(t, types, 3)

	// Subsequent LoadForOwner for same uid should be idempotent
	registry.LoadForOwner("uid-1", configs["uid-1"])
	types = registry.KnownTypes()
	assert.Len(t, types, 3)

	// Removing uid-1 should remove project
	registry.RemoveOwner("uid-1")
	types = registry.KnownTypes()
	assert.False(t, types["project"])
	assert.True(t, types["team"])
}

func TestEntityTypeRegistry_Bulkload_ClearsOwners(t *testing.T) {
	registry := NewEntityTypeRegistry()

	registry.LoadForOwner("uid-1", ContentConfiguration{
		LuigiConfigFragment: LuigiConfigFragment{
			Data: LuigiConfigData{
				Nodes: []Node{
					{EntityType: "global", DefineEntity: &DefineEntity{Id: "project"}},
				},
			},
		},
	})

	// Bulkload resets everything including owners
	registry.Bulkload([]ContentConfiguration{})
	types := registry.KnownTypes()
	assert.Len(t, types, 1)
	assert.True(t, types["global"])

	// RemoveOwner for the old uid should be a no-op
	registry.RemoveOwner("uid-1")
	assert.True(t, registry.KnownTypes()["global"])
}

func TestEntityTypeRegistry_ConcurrentAccess_WithOwners(t *testing.T) {
	registry := NewEntityTypeRegistry()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			cc := ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: "global", DefineEntity: &DefineEntity{Id: "project"}},
						},
					},
				},
			}
			registry.LoadForOwner(fmt.Sprintf("uid-%d", i), cc)
		}()
		go func() {
			defer wg.Done()
			registry.Validate(ContentConfiguration{
				LuigiConfigFragment: LuigiConfigFragment{
					Data: LuigiConfigData{
						Nodes: []Node{
							{EntityType: "project"},
						},
					},
				},
			})
		}()
	}
	wg.Wait()
}
