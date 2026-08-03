// SPDX-License-Identifier: Apache-2.0

package tools

// search_schema_mode_test.go pins the search tool's DECLARED mode contract
// against the RUNTIME values SearchToolDef returns — never against file text.
//
// That distinction is the point. The tool Description is assembled by `+`
// concatenation across several source lines, so any grep-based anchor on it
// breaks the moment the literal is re-wrapped, while saying nothing about what
// callers actually receive. Asserting the composed value is immune to wrapping
// and is what a caller reads.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSearchToolDef_ModeContractDocumented asserts the declaration matches what
// the arms now do: text is BM25-only AND runs no rerank, the trio belongs to the
// knowledge/custom-graph arms rather than being qualified as code-only, and
// temporal is still named as a recency alias.
func TestSearchToolDef_ModeContractDocumented(t *testing.T) {
	def := SearchToolDef()

	t.Run("tool description names the text contract", func(t *testing.T) {
		assert.Contains(t, def.Description, "BM25 only",
			"the tool description must say what text retrieves")
		assert.Contains(t, def.Description, "no rerank runs",
			"suppressing the rerank is half the text contract and must be declared")
	})

	modeProp, ok := def.InputSchema.Properties["mode"]
	require.True(t, ok, "the search tool declares a mode param")

	t.Run("mode property names the honoring arms", func(t *testing.T) {
		assert.Contains(t, modeProp.Description, "knowledge",
			"the trio is honored on the knowledge arm, which the declaration must name")
		assert.NotContains(t, modeProp.Description, "'vector' (code)",
			"qualifying the trio as code-only is the false claim this replaces")
	})

	t.Run("temporal stays a declared recency alias", func(t *testing.T) {
		assert.Contains(t, modeProp.Description, "temporal",
			"temporal is honored as a recency boost, so it stays declared")
	})
}
