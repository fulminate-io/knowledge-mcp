// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// operationGrammar is the server's shape validator, restated here so a term that
// would be REJECTED at runtime is caught at build time instead. Keep it in sync
// with the server-side check; the two are deliberately independent copies (the
// client and server are separate modules and share only generated protobuf), so
// this test is what keeps them from drifting apart silently.
var operationGrammar = regexp.MustCompile(`^[a-z][a-z0-9_.]{2,47}$`)

// TestOperationVocabulary is the ONLY thing bounding the cardinality of the
// operation metrics dimension — the server validates shape but keeps no closed
// list, by design. It asserts grammar-conformance, uniqueness, and closure.
func TestOperationVocabulary(t *testing.T) {
	t.Run("every declared term matches the server grammar", func(t *testing.T) {
		for _, op := range AllOperations {
			assert.Regexp(t, operationGrammar, string(op),
				"operation %q would be rejected by the server's shape check", op)
		}
	})

	t.Run("terms are unique", func(t *testing.T) {
		seen := make(map[Operation]int, len(AllOperations))
		for _, op := range AllOperations {
			seen[op]++
		}
		for op, n := range seen {
			assert.Equal(t, 1, n, "operation %q is declared %d times in AllOperations", op, n)
		}
	})

	t.Run("the reserved unstamped term is present", func(t *testing.T) {
		assert.Contains(t, AllOperations, OpUnstamped,
			"client.unstamped is the reserved default-deny term and must be enumerated")
		assert.Equal(t, OpUnstamped, Operation("client.unstamped"),
			"the reserved term's VALUE is what the per-tag tooling buckets on; renaming it breaks that alarm")
	})

	t.Run("every tool-map term is a declared term", func(t *testing.T) {
		declared := make(map[Operation]struct{}, len(AllOperations))
		for _, op := range AllOperations {
			declared[op] = struct{}{}
		}
		for tool, op := range toolOperations {
			_, ok := declared[op]
			assert.True(t, ok, "tool %q maps to %q, which is not enumerated in AllOperations", tool, op)
		}
	})

	t.Run("an unknown tool degrades to a declared bounded term", func(t *testing.T) {
		// The property that matters is BOUNDEDNESS: an unrecognized tool must
		// never pass its raw name through as a label.
		got := OperationForTool("not_a_real_tool_name")
		assert.Equal(t, OpToolUnknown, got)
		assert.Regexp(t, operationGrammar, string(got))
	})

	t.Run("known tools resolve to their own term", func(t *testing.T) {
		require.Equal(t, OpSearch, OperationForTool("search"))
		require.Equal(t, OpFileSymbols, OperationForTool("file_symbols"))
	})
}

// TestOperationVocabularyCoversToolCatalog asserts the declared tool map and the
// advertised MCP catalog agree. It lives here rather than in the tools package
// because the vocabulary is what must not drift; a tool added to the catalog
// without a term would otherwise reach production as tool.unknown, which is
// bounded but useless. The catalog names are restated rather than imported to
// avoid an import cycle (tools already depends on the client wiring).
func TestOperationVocabularyCoversToolCatalog(t *testing.T) {
	// Mirrors tools.AllToolSchemas() — 22 advertised tools.
	catalog := []string{
		"query", "traverse", "mutate", "delete", "manage",
		"sync",
		"thoughts", "search", "file_symbols", "collect",
		"custom_collector", "ast", "help", "record_decision",
		"analyze_usage", "manage_checks", "create_plan", "create_ticket", "create_project",
		"create_research", "create_test_plan", "assemble",
	}
	require.Len(t, catalog, 22, "the advertised catalog is a closed 22-entry set")

	for _, name := range catalog {
		assert.NotEqual(t, OpToolUnknown, OperationForTool(name),
			"catalog tool %q has no declared operation — add one to operation_vocab.go", name)
	}
	assert.Len(t, toolOperations, len(catalog),
		"toolOperations and the advertised catalog must have the same size; a stale entry here is as wrong as a missing one")
}
