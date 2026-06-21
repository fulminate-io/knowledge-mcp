// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func recipeNode(id, name, body string, updatedAt int64) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: id, Type: "recipe", SymbolName: name, Content: body, UpdatedAt: updatedAt,
		Metadata: map[string]string{},
	}
}

func TestLoadRecipeByName_HitAndMiss(t *testing.T) {
	f := &fakeGraphCaller{
		nodes: []*knowledgev1.Node{
			recipeNode("r1", "eip-patterns", "select section", 1),
			recipeNode("r2", "go101-patterns", "select section", 1),
		},
	}
	got, err := loadRecipeByName(context.Background(), f, "go101-patterns")
	require.NoError(t, err)
	assert.Equal(t, "r2", got.Id)

	_, err = loadRecipeByName(context.Background(), f, "missing")
	require.Error(t, err)
	// The not-found error lists the available names.
	assert.Contains(t, err.Error(), "eip-patterns")
	assert.Contains(t, err.Error(), "go101-patterns")
}

func TestParseWithCache_CachesByIDAndUpdatedAt(t *testing.T) {
	// Clear any cache entry from a prior test run for determinism.
	astCache.Delete(astCacheKey{id: "rc1", updatedAt: 7})

	node := recipeNode("rc1", "x", "select section\nemit pattern {\n name := section.symbol_name\n}", 7)
	a, err := parseWithCache(node)
	require.NoError(t, err)
	require.NotNil(t, a)

	// Second call with the same (id, updatedAt) returns the SAME cached pointer.
	b, err := parseWithCache(node)
	require.NoError(t, err)
	assert.Same(t, a, b, "parseWithCache must return the cached AST without re-parsing")

	// Bumping UpdatedAt forces a fresh parse → a different pointer.
	node2 := recipeNode("rc1", "x", "select section\nemit pattern {\n name := section.symbol_name\n}", 8)
	c, err := parseWithCache(node2)
	require.NoError(t, err)
	assert.NotSame(t, a, c, "an UpdatedAt bump must invalidate the cached AST")
}
