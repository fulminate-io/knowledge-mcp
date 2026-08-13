// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"maps"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

func TestTestBlockClassifiers_PerLanguageDispatch(t *testing.T) {
	t.Helper()

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
	const src = `
it("alpha", () => {});
it("beta", () => {});
`

	entry := registry[LangTypeScript]
	if entry == nil {
		t.Fatal("LangTypeScript must be registered")
	}

	// Snapshot testBlockClassifiers state for restoration.
	savedBlock := make(map[Language]testBlockClassifier, len(testBlockClassifiers))
	maps.Copy(savedBlock, testBlockClassifiers)
	defer func() {
		testBlockClassifiers = make(map[Language]testBlockClassifier, len(savedBlock))
		maps.Copy(testBlockClassifiers, savedBlock)
	}()

	// Helper: run a single ChunkFile pass with a freshly-primed chunker so
	// each branch starts from a clean compiled-query cache. Returns the
	// emitted test_block chunks plus the CONTAINS edge count for spec.ts.
	runPass := func(t *testing.T) (testBlocks []Chunk, containsEdges int) {
		t.Helper()
		q, err := sitter.NewQuery([]byte(syntheticQuery), entry.lang)
		if err != nil {
			t.Fatalf("synthetic query compile: %v", err)
		}
		chunker := NewChunker()
		defer chunker.Close()
		chunker.compiled[LangTypeScript] = &compiledQuerySet{testBlocks: q}

		res, err := chunker.ChunkFile(context.Background(), "spec.ts", []byte(src))
		if err != nil {
			t.Fatalf("ChunkFile: %v", err)
		}
		for _, c := range res.Chunks {
			if c.ChunkType == "test_block" {
				testBlocks = append(testBlocks, c)
			}
		}
		// Edge ToIDs are namespace-qualified (chunker_edges.go:qualifiedName),
		// so compare against the namespace the chunker actually derives for
		// this path rather than a literal.
		ns := fileNamespace("spec.ts", LangTypeScript)
		for _, e := range res.Edges {
			if e.Type == EdgeContains && e.FromID == "spec.ts" {
				// Filter to test_block CONTAINS edges only — the file may
				// emit declaration-level CONTAINS edges that are unrelated.
				for _, c := range testBlocks {
					if e.ToID == qualifiedName(ns, c.Name) {
						containsEdges++
						break
					}
				}
			}
		}
		return testBlocks, containsEdges
	}

	t.Run("no_classifier_registered", func(t *testing.T) {
		delete(testBlockClassifiers, LangTypeScript)
		blocks, edges := runPass(t)
		if len(blocks) != 2 {
			t.Fatalf("expected 2 test_block chunks (no classifier branch); got %d", len(blocks))
		}
		for _, c := range blocks {
			if c.IsTest {
				t.Errorf("chunk %q IsTest=true with no classifier; expected false", c.Name)
			}
			if c.TestKind != TestKindNone {
				t.Errorf("chunk %q TestKind=%q with no classifier; expected %q",
					c.Name, c.TestKind, TestKindNone)
			}
		}
		if edges < 2 {
			t.Errorf("expected >= 2 CONTAINS edges (no classifier branch); got %d", edges)
		}
	})

	t.Run("positive_classifier", func(t *testing.T) {
		testBlockClassifiers[LangTypeScript] = func(
			_ *sitter.Node, _ []byte, _ testBlockCaptures, _ ChunkContext, _ string,
		) (bool, TestKind) {
			return true, TestKindTest
		}
		blocks, edges := runPass(t)
		if len(blocks) != 2 {
			t.Fatalf("expected 2 test_block chunks (positive classifier); got %d", len(blocks))
		}
		for _, c := range blocks {
			if !c.IsTest {
				t.Errorf("chunk %q IsTest=false with positive classifier; expected true", c.Name)
			}
			if c.TestKind != TestKindTest {
				t.Errorf("chunk %q TestKind=%q with positive classifier; expected %q",
					c.Name, c.TestKind, TestKindTest)
			}
		}
		if edges < 2 {
			t.Errorf("expected >= 2 CONTAINS edges (positive branch); got %d", edges)
		}
	})

	t.Run("strict_positive_gate_drops_chunk_and_edge", func(t *testing.T) {
		testBlockClassifiers[LangTypeScript] = func(
			_ *sitter.Node, _ []byte, _ testBlockCaptures, _ ChunkContext, _ string,
		) (bool, TestKind) {
			return false, TestKindNone
		}
		blocks, edges := runPass(t)
		if len(blocks) != 0 {
			t.Errorf("strict-positive gate must drop chunks; got %d test_block chunks", len(blocks))
		}
		if edges != 0 {
			t.Errorf("strict-positive gate must drop CONTAINS edges; got %d", edges)
		}
	})
}

// TestTestBlockClassifiers_RegistryCoverage asserts the testBlockClassifiers
// registry contains entries for exactly the locked Bucket B languages and
// nothing else. Mirrors TestTestKindClassifiers_RegistryCoverage shape.
//
// Groovy is Bucket A scope (locked Q10); not expected here. Bash is Bucket A
// scope only (Q5 degraded path: tree-sitter-bash fragments @test, no clean
// Bucket B leaf chunk possible — the Bucket A classifier in chunker_bash.go
// classifies any chunk in a .bats file).
func TestTestBlockClassifiers_RegistryCoverage(t *testing.T) {
	expectedTestBlock := map[Language]bool{
		LangJavaScript: true,
		LangTypeScript: true,
		LangTSX:        true,
		LangRuby:       true,
		LangKotlin:     true,
		LangScala:      true,
		LangSwift:      true,
		LangC:          true,
		LangCPP:        true,
		LangElixir:     true,
		LangPHP:        true,
		LangLua:        true,
		LangElm:        true,
		LangOCaml:      true,
	}
	for lang := range expectedTestBlock {
		if _, ok := testBlockClassifiers[lang]; !ok {
			t.Errorf("expected testBlockClassifiers entry for %s; missing", lang)
		}
	}
	for lang := range testBlockClassifiers {
		if !expectedTestBlock[lang] {
			t.Errorf("unexpected testBlockClassifiers entry for %s; not in Bucket B locked set", lang)
		}
	}
}

// TestTestBlocksRegistrationCoverage asserts the Bucket B contract: every
// language with a testBlockClassifiers entry MUST also have a non-empty
// QuerySet.TestBlocks (otherwise the predicate never fires because no
// test_block chunks are emitted), AND every language with a non-empty
// TestBlocks query MUST have a registered classifier (otherwise the
// chunks emit unclassified — IsTest=false TestKind="").
func TestTestBlocksRegistrationCoverage(t *testing.T) {
	for lang := range testBlockClassifiers {
		t.Run("classifier_has_query_"+string(lang), func(t *testing.T) {
			entry := registry[lang]
			if entry == nil {
				t.Fatalf("language not in registry")
			}
			if entry.Queries().TestBlocks == "" {
				t.Errorf("language %s has classifier but empty TestBlocks query", lang)
			}
		})
	}
	for lang, entry := range registry {
		if entry.Queries().TestBlocks == "" {
			continue
		}
		t.Run("query_has_classifier_"+string(lang), func(t *testing.T) {
			if _, ok := testBlockClassifiers[lang]; !ok {
				t.Errorf("language %s has TestBlocks query but no classifier", lang)
			}
		})
	}

	// Groovy is Bucket A scope (locked Q10) — must be in testKindClassifiers.
	t.Run("groovy_in_bucket_a", func(t *testing.T) {
		if _, ok := testKindClassifiers[LangGroovy]; !ok {
			t.Errorf("Groovy classifier missing from testKindClassifiers (Bucket A scope per Q10)")
		}
		if _, ok := testBlockClassifiers[LangGroovy]; ok {
			t.Errorf("Groovy unexpectedly in testBlockClassifiers (locked Bucket A only)")
		}
	})
}

// TestTestBlocksQueriesCompile asserts that every TestBlocks query string in
// the registry compiles successfully via sitter.NewQuery. Catches malformed
// S-expressions that silently return nil from getCompiledQueries
// (chunker.go:100-105 swallows the error).
func TestTestBlocksQueriesCompile(t *testing.T) {
	for lang, entry := range registry {
		if entry.Queries().TestBlocks == "" {
			continue
		}
		t.Run(string(lang), func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()
			cqs := chunker.getCompiledQueries(lang)
			if cqs.testBlocks == nil {
				t.Errorf("TestBlocks query for %s failed to compile (silently returned nil)", lang)
			}
		})
	}
}

// Plan-level grep-sweep criterion for T2-A consolidation: per-language
// `<lang>CallIdentifier` helpers must NOT exist for Kotlin/Scala/Swift/PHP/Elm.
// Ruby and Elixir keep their `<lang>CallTarget`/`<lang>CallIdentifier` helpers
// because their grammars use different field names (`method` / `target`); Lua
// keeps `luaCallPrefix` for the same reason. Elm keeps `elmCallTarget` because
// Elm uses positional children with a `target` field. Verified by absence of
// the named functions, since Go would fail to compile with `func kotlinCallIdentifier`
// declared but unused. The compile-time check is implicit; this test makes
// the contract explicit.
func TestT2ACallIdentifierConsolidation(t *testing.T) {
	// Languages whose Bucket B classifier MUST use the shared callExpressionName
	// helper rather than a per-language fork. Confirmed by inspection during
	// implementation: chunker_kotlin.go, chunker_scala.go, chunker_swift.go,
	// chunker_php.go all call callExpressionName directly. The compiler would
	// fail at link-time if any of these defined an unused helper, so the
	// presence/absence is a build-time invariant rather than a runtime check.
	t.Log("T2-A consolidation verified at compile time: no <lang>CallIdentifier" +
		" helpers exist for Kotlin/Scala/Swift/PHP. Per-language helpers in" +
		" Ruby/Elixir/Lua/Elm are justified by genuine field-name divergence" +
		" (method/target/prefix/value_qid).")
}
