// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"maps"
	"sort"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

// TestTestKindClassifiers_PerLanguageDispatch verifies the dispatch hook in
// emitDeclarationChunk routes to the registered classifier for the file's
// language and applies its (IsTest, TestKind) result to every chunk.
//
// Two branches:
//  1. Registry has no entry for LangGo -> all chunks default to IsTest=false,
//     TestKind="" (the pre-Bucket-A behavior).
//  2. Registry has an entry forcing (true, TestKindHelper) -> every emitted
//     chunk reflects that, proving the wiring fires per chunk.
//
// The test mutates the package-level testKindClassifiers map and restores it
// in a defer. Per-language phases below populate the registry via init() in
// chunker_<lang>.go files, so the saved snapshot covers any pre-registered
// entries from sibling phases without clobbering them.
func TestTestKindClassifiers_PerLanguageDispatch(t *testing.T) {
	t.Helper()

	src := []byte("package foo\n\nfunc Bar() {}\n")
	chunker := NewChunker()
	defer chunker.Close()

	// Snapshot the current registry so we restore exactly what was registered
	// before the test (including any per-language phases below).
	saved := make(map[Language]testKindClassifier, len(testKindClassifiers))
	maps.Copy(saved, testKindClassifiers)
	defer func() {
		// Restore: remove anything we added, replace anything we changed,
		// re-add anything we deleted.
		testKindClassifiers = make(map[Language]testKindClassifier, len(saved))
		maps.Copy(testKindClassifiers, saved)
	}()

	// Branch 1: ensure no LangGo entry; verify all chunks default.
	delete(testKindClassifiers, LangGo)

	res, err := chunker.ChunkFile(context.Background(), "/tmp/foo.go", src)
	if err != nil {
		t.Fatalf("ChunkFile (no classifier): %v", err)
	}
	if len(res.Chunks) == 0 {
		t.Fatal("expected at least one chunk; got none")
	}
	for _, ch := range res.Chunks {
		if ch.IsTest {
			t.Errorf("chunk %q (%s) IsTest=true with empty registry; expected false",
				ch.Name, ch.ChunkType)
		}
		if ch.TestKind != TestKindNone {
			t.Errorf("chunk %q (%s) TestKind=%q with empty registry; expected %q",
				ch.Name, ch.ChunkType, ch.TestKind, TestKindNone)
		}
	}

	// Branch 2: register a stub that forces every chunk to (true, helper).
	// Verifies the dispatch fires per chunk and the result lands on the
	// emitted chunk fields.
	testKindClassifiers[LangGo] = func(
		_ *sitter.Node,
		_ []byte,
		_, _ string,
		_ ChunkContext,
		_ string,
	) (bool, TestKind) {
		return true, TestKindHelper
	}

	res, err = chunker.ChunkFile(context.Background(), "/tmp/foo.go", src)
	if err != nil {
		t.Fatalf("ChunkFile (stub classifier): %v", err)
	}
	if len(res.Chunks) == 0 {
		t.Fatal("expected at least one chunk; got none")
	}
	for _, ch := range res.Chunks {
		if !ch.IsTest {
			t.Errorf("chunk %q (%s) IsTest=false with stub classifier; expected true",
				ch.Name, ch.ChunkType)
		}
		if ch.TestKind != TestKindHelper {
			t.Errorf("chunk %q (%s) TestKind=%q with stub classifier; expected %q",
				ch.Name, ch.ChunkType, ch.TestKind, TestKindHelper)
		}
	}
}

// TestTestKindClassifiers_RegistryCoverage asserts the registry contains
// entries for exactly the locked Bucket A languages and nothing else.
// Mirrors framework_test.go:236 TestDetectFrameworksTableCoverage shape:
// declare the expected set, scan the registry, fail on missing or extra.
//
// frameworkExtenders has a parallel coverage check — Go and Rust ONLY.
func TestTestKindClassifiers_RegistryCoverage(t *testing.T) {
	expectedClassifiers := map[Language]bool{
		LangGo:     true,
		LangPython: true,
		LangJava:   true,
		LangKotlin: true,
		LangScala:  true,
		LangCSharp: true,
		LangSwift:  true,
		LangRust:   true,
		LangElixir: true,
		LangPHP:    true,
		LangHCL:    true,
		// Bucket B (Phase 7/10/11/12) added these to the Bucket A registry too:
		// C/C++ filename-driven helper/mock; Lua LuaUnit declaration-style;
		// Bash bats degraded path; Groovy Spock Bucket A scope per Q10.
		LangC:      true,
		LangCPP:    true,
		LangLua:    true,
		LangBash:   true,
		LangGroovy: true,
		// Ruby is a dual-bucket language: Bucket B (testBlockClassifiers)
		// handles RSpec block form; Bucket A (this entry) handles
		// class+method form for Minitest::Test / Test::Unit::TestCase.
		LangRuby: true,
	}
	for lang := range expectedClassifiers {
		if _, ok := testKindClassifiers[lang]; !ok {
			t.Errorf("expected testKindClassifiers entry for %s; missing", lang)
		}
	}
	for lang := range testKindClassifiers {
		if !expectedClassifiers[lang] {
			t.Errorf("unexpected testKindClassifiers entry for %s; not in Bucket A locked set", lang)
		}
	}

	expectedExtenders := map[Language]bool{
		LangGo:   true,
		LangRust: true,
		// HCL has no Imports query — the extender is the only path that
		// produces FrameworkHCLTfTest from the `.tftest.hcl` filename suffix.
		LangHCL: true,
	}
	for lang := range expectedExtenders {
		if _, ok := frameworkExtenders[lang]; !ok {
			t.Errorf("expected frameworkExtenders entry for %s; missing", lang)
		}
	}
	for lang := range frameworkExtenders {
		if !expectedExtenders[lang] {
			t.Errorf("unexpected frameworkExtenders entry for %s; only Go + Rust have extenders", lang)
		}
	}
}

// TestTestKindClassifiers_NonBucketALanguagesUnchanged exercises the
// pre-Bucket-A behavior for declaration chunks emitted from non-Bucket-A
// languages: every NON-test_block chunk emitted from a synthetic test-shaped
// fixture must have IsTest=false TestKindNone. Confirms the testKindClassifiers
// registry's negative coverage — no overzealous catch-all classification.
//
// test_block chunks (Bucket B output via testBlockClassifiers) are EXCLUDED
// from this assertion because Bucket B has its own per-language predicates
// covering JS/TS/Ruby/Lua/etc. Bucket A and Bucket B are independent
// dispatch paths over disjoint chunk shapes (declarations vs call/macro
// blocks); coupling their assertions in one test would conflate scopes.
func TestTestKindClassifiers_NonBucketALanguagesUnchanged(t *testing.T) {
	// Each entry: a (path, src) pair the chunker can parse for a non-Bucket-A
	// language. We assert every chunk produced has IsTest=false.
	cases := []struct {
		lang string
		path string
		src  string
	}{
		// Bucket B languages — declaration chunks emit IsTest=false; only
		// test_block chunks classify (filtered out by the loop below).
		{"typescript", "App.spec.ts", `import { describe, it } from 'jest';
describe('foo', () => { it('works', () => {}); });
`},
		{"javascript", "App.spec.js", `const { describe, it } = require('jest');
describe('foo', () => { it('works', () => {}); });
`},
		{"ruby", "spec/foo_spec.rb", `RSpec.describe 'foo' do
  it 'works' do
  end
end
`},
		{"elm", "tests/Foo.elm", `module Foo exposing (..)
foo : Int
foo = 1
`},
		{"ocaml", "tests/foo.ml", `let foo () = ()
`},
		// Bucket A coverage languages with non-test paths so the classifier's
		// strict-positive gate returns (false, TestKindNone). Verifies the
		// negative branch (non-test files don't classify).
		{"bash", "test_foo.sh", `#!/usr/bin/env bash
function test_works() {
  echo "ok"
}
`},
		{"lua", "lib/utils.lua", `function helper()
end
`},
		{"groovy", "lib/Helper.groovy", `class Helper {
    def doStuff() { }
}
`},
		{"c", "production.c", `#include <stdio.h>
int main(void) { return 0; }
`},
		{"cpp", "production.cpp", `#include <iostream>
int main() { return 0; }
`},
	}
	chunker := NewChunker()
	defer chunker.Close()
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile(%s): %v", tc.lang, err)
			}
			for _, ch := range res.Chunks {
				// test_block chunks follow the Bucket B dispatch — out of
				// scope for this Bucket A negative-coverage test.
				if ch.ChunkType == "test_block" {
					continue
				}
				if ch.IsTest {
					t.Errorf("[%s] chunk %q (%s) IsTest=true; want false (non-Bucket-A)",
						tc.lang, ch.Name, ch.ChunkType)
				}
				if ch.TestKind != TestKindNone {
					t.Errorf("[%s] chunk %q (%s) TestKind=%q; want %q",
						tc.lang, ch.Name, ch.ChunkType, ch.TestKind, TestKindNone)
				}
			}
		})
	}

	// Sanity: list the names of registered classifiers for diagnostic clarity.
	var names []string
	for lang := range testKindClassifiers {
		names = append(names, string(lang))
	}
	sort.Strings(names)
	t.Logf("registered testKindClassifiers: %v", names)
}

// TestTestBlockClassifiers_PerLanguageDispatch verifies the Bucket B dispatch
// hook in emitTestBlockChunk routes to the registered classifier for the file's
// language and applies its (IsTest, TestKind) result to every emitted test_block
// chunk. Three branches:
//
//  1. No classifier registered for the language — chunks emitted with
//     IsTest=false TestKind=TestKindNone (pre-Bucket-B fallthrough).
//  2. Classifier registered, returns (true, TestKindTest) — chunk emitted with
//     those values; CONTAINS edge produced.
//  3. Classifier registered, returns (false, TestKindNone) — strict-positive
//     gate (locked Q9) DROPS chunk AND skips CONTAINS edge.
//
// Mirrors snapshot/restore discipline of TestTestKindClassifiers_PerLanguageDispatch.
