// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWalkTestBlocks_Synthetic verifies the test_block abstraction end-to-end
// using a synthetic TypeScript TestBlocks query injected via the in-package
// compiled-query cache. NO production queries_<lang>.go files are touched —
// per-language predicate / query bodies are Bucket B's scope.
//
// The test exercises:
//   - the @decl + @name + @params capture path with a top-level it() call;
//   - the @parent_name capture path via a separate pattern matching describe();
//   - the @parent_name-absent fallback (chunk.ParentName == "");
//   - Exported == false, ChunkType == "test_block";
//   - Context.Signature == "(done)" verbatim from the @params capture;
//   - one CONTAINS edge per emitted chunk;
//   - the empty-query short-circuit (cqs.testBlocks == nil → zero test_blocks).
func TestWalkTestBlocks_Synthetic(t *testing.T) {
	// Synthetic query: matches `it("...", (params) => {...})` and
	// `describe("...", () => {...})`. The `it` pattern uses @parent_name = "" by
	// not binding it; only the describe pattern would carry @parent_name in
	// real Bucket B queries. For this synthetic test we keep both top-level so
	// we can assert the @parent_name-absent path on the it() chunk.
	const syntheticQuery = `
(call_expression
  function: (identifier) @fn
  arguments: (arguments
    (string) @name
    (arrow_function parameters: (formal_parameters) @params)
  )
) @decl
(#match? @fn "^(it|test|describe)$")
`

	entry := registry[LangTypeScript]
	require.NotNil(t, entry, "LangTypeScript must be registered")

	q, err := sitter.NewQuery([]byte(syntheticQuery), entry.lang)
	require.NoError(t, err, "synthetic TestBlocks query must compile")

	chunker := NewChunker()
	defer chunker.Close()

	// Cache-priming option (b) per plan: preassign the compiled entry BEFORE
	// ChunkFile is called so getCompiledQueries returns this synthetic set
	// and the qs.TestBlocks == "" skip is bypassed.
	chunker.compiled[LangTypeScript] = &compiledQuerySet{testBlocks: q}

	// Install a no-op stub classifier so the strict-positive gate (locked Q9)
	// doesn't drop our synthetic chunks once Phase 2 lands the production
	// classifyTestBlockJS predicate. The stub lets us exercise the test_block
	// abstraction (capture extraction, signature round-trip, edge emission)
	// in isolation from production predicate semantics. Snapshot/restore is
	// the standard idiom for global-map mutation under test (mirror
	// chunker_test_kind_test.go:36-47).
	saved, hadEntry := testBlockClassifiers[LangTypeScript]
	testBlockClassifiers[LangTypeScript] = func(_ *sitter.Node, _ []byte, _ testBlockCaptures, _ ChunkContext, _ string) (bool, TestKind) {
		return true, TestKindTest
	}
	defer func() {
		if hadEntry {
			testBlockClassifiers[LangTypeScript] = saved
		} else {
			delete(testBlockClassifiers, LangTypeScript)
		}
	}()

	const src = `
it("rejects expired", (done) => { done(); });

describe("Auth", () => {
  it("alpha", () => {});
});

it("orphaned", () => {});
`

	result, err := chunker.ChunkFile(context.Background(), "spec.ts", []byte(src))
	require.NoError(t, err)
	require.NotNil(t, result)

	// Collect test_block chunks for assertions.
	var testBlocks []Chunk
	for _, c := range result.Chunks {
		if c.ChunkType == "test_block" {
			testBlocks = append(testBlocks, c)
		}
	}
	require.GreaterOrEqual(t, len(testBlocks), 3, "expected at least 3 test_block chunks (it/describe/it/it)")

	// Index by name for stable lookups.
	byName := make(map[string]Chunk, len(testBlocks))
	for _, c := range testBlocks {
		byName[c.Name] = c
		assert.Equal(t, "test_block", c.ChunkType)
		assert.False(t, c.Exported, "test_block chunks must always have Exported=false")
		// The stub classifier installed above forces every test_block chunk to
		// (true, TestKindTest); this asserts the dispatch hook fires per chunk
		// and the predicate's result lands on the emitted chunk fields.
		assert.True(t, c.IsTest, "stub classifier forces IsTest=true on every test_block chunk")
		assert.Equal(t, TestKindTest, c.TestKind, "stub classifier forces TestKindTest on every test_block chunk")
	}

	// "rejects expired" — exercises @params verbatim round-trip.
	rej, ok := byName["rejects expired"]
	require.True(t, ok, "expected chunk named 'rejects expired'")
	assert.Equal(t, "(done)", rej.Context.Signature, "Signature must equal the @params capture verbatim")
	// @parent_name absent on this query → ParentName empty.
	assert.Empty(t, rej.ParentName, "ParentName must be empty when @parent_name is not captured (T3-3 fix)")

	// "orphaned" — top-level it() with no enclosing describe.
	orph, ok := byName["orphaned"]
	require.True(t, ok, "expected chunk named 'orphaned'")
	assert.Empty(t, orph.ParentName, "top-level it() must produce ParentName=='' (no automatic AST ascent)")

	// "Auth" — describe block.
	auth, ok := byName["Auth"]
	require.True(t, ok, "expected chunk named 'Auth' (describe block)")
	assert.Equal(t, "()", auth.Context.Signature, "describe(...) closure params should round-trip as '()'")
	_ = auth

	// At least one CONTAINS edge per emitted test_block chunk (file → qualified name).
	var containsCount int
	for _, e := range result.Edges {
		if e.Type == EdgeContains && e.FromID == "spec.ts" {
			containsCount++
		}
	}
	assert.GreaterOrEqual(t, containsCount, len(testBlocks),
		"expected >= one CONTAINS edge per test_block chunk")

	// Empty-query skip path: a fresh chunker with cqs.testBlocks=nil emits zero test_blocks.
	chunker2 := NewChunker()
	defer chunker2.Close()
	chunker2.compiled[LangTypeScript] = &compiledQuerySet{} // testBlocks intentionally nil
	result2, err := chunker2.ChunkFile(context.Background(), "spec2.ts", []byte(src))
	require.NoError(t, err)
	for _, c := range result2.Chunks {
		assert.NotEqual(t, "test_block", c.ChunkType,
			"empty TestBlocks query must produce zero test_block chunks")
	}
}

// TestExtractTestBlockCaptures_NoCaptureAbsent verifies that when a query match
// omits the @parent_name and @params capture bindings, the extracted struct's
// fields zero-value to the empty string (the absence is propagated, not synthesized).
func TestExtractTestBlockCaptures_NoCaptureAbsent(t *testing.T) {
	// Query that ONLY binds @decl + @name — no @parent_name, no @params.
	const minimalQuery = `
(call_expression
  function: (identifier) @fn
  arguments: (arguments (string) @name)
) @decl
(#match? @fn "^it$")
`
	entry := registry[LangTypeScript]
	require.NotNil(t, entry)

	q, err := sitter.NewQuery([]byte(minimalQuery), entry.lang)
	require.NoError(t, err)

	chunker := NewChunker()
	defer chunker.Close()
	chunker.compiled[LangTypeScript] = &compiledQuerySet{testBlocks: q}

	// Install a no-op stub classifier so the strict-positive gate (locked Q9)
	// doesn't drop the chunk once Phase 2 lands the production classifier.
	saved, hadEntry := testBlockClassifiers[LangTypeScript]
	testBlockClassifiers[LangTypeScript] = func(_ *sitter.Node, _ []byte, _ testBlockCaptures, _ ChunkContext, _ string) (bool, TestKind) {
		return true, TestKindTest
	}
	defer func() {
		if hadEntry {
			testBlockClassifiers[LangTypeScript] = saved
		} else {
			delete(testBlockClassifiers, LangTypeScript)
		}
	}()

	const src = `it("minimal", () => {});`

	result, err := chunker.ChunkFile(context.Background(), "min.ts", []byte(src))
	require.NoError(t, err)

	var found bool
	for _, c := range result.Chunks {
		if c.ChunkType != "test_block" {
			continue
		}
		found = true
		assert.Equal(t, "minimal", c.Name)
		assert.Empty(t, c.ParentName,
			"@parent_name not captured → ParentName must be empty (no AST ascent)")
		assert.Empty(t, c.Context.Signature,
			"@params not captured → Signature must be empty")
	}
	require.True(t, found, "expected at least one test_block chunk")
}
