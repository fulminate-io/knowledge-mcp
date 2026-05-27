// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// testKindClassifier is the per-language predicate signature for Bucket A.
// Receives the tree-sitter declaration node, source bytes, the resolved
// chunk type (e.g. "function_declaration", "method_declaration",
// "class_declaration"), the symbol name, the file's context (including
// Frameworks set populated by DetectFrameworks plus any extensions), and
// the file path. Returns (IsTest, TestKind). When the predicate cannot
// classify a chunk, it returns (false, TestKindNone) — never (true, "")
// or (false, non-empty), which would violate the Chunk invariant.
//
// Predicates run inside emitDeclarationChunk (chunker.go:307) on every
// declaration emitted by walkTopLevel. Languages without an entry in
// testKindClassifiers leave IsTest=false TestKind="" untouched.
type testKindClassifier func(
	declNode *sitter.Node,
	src []byte,
	chunkType, name string,
	fileCtx ChunkContext,
	filePath string,
) (bool, TestKind)

// testKindClassifiers registers per-language predicates for declaration-
// based test classification (Bucket A). Mirror frameworkTables shape at
// framework_tables.go:24 — map-of-language-to-helper, no entry means no
// classification.
//
// Empty in this step; populated by per-language phases below. Each
// predicate lives in chunker_<lang>.go alongside language-specific
// helpers like extractGoSignature (chunker_go.go:84).
var testKindClassifiers = map[Language]testKindClassifier{}

// frameworkExtender is the per-language hook that augments the file's
// Frameworks set with AST/filename-derived constants that DetectFrameworks
// (framework.go:140) cannot produce — currently FrameworkRustTest (set
// when #[test] attribute is present anywhere) and FrameworkGoTesting (set
// when filePath ends in _test.go). Returns the augmented set; called
// once per file in ChunkFile after DetectFrameworks and before walkTopLevel.
//
// CONTRACT: extenders MUST preserve the input slice and only APPEND.
// Returning a nil/empty slice when the input was non-nil silently drops
// the framework signal that DetectFrameworks already produced from imports.
// Implementations should always start from `detected` and append additional
// constants based on AST/filename signals; never replace, filter, or reorder.
type frameworkExtender func(
	root *sitter.Node,
	src []byte,
	filePath string,
	detected []Framework,
) []Framework

// frameworkExtenders is the per-language registry parallel to
// testKindClassifiers. Empty in this step.
var frameworkExtenders = map[Language]frameworkExtender{}

// testBlockClassifier is the per-language predicate signature for Bucket B.
// Receives the tree-sitter @decl node (the call/macro invocation), source
// bytes, the test_block captures pulled from the TestBlocks query match
// (Name = string-literal label, ParentName = outer describe scope, Params =
// closure parameter list), the file's context (Frameworks set populated by
// DetectFrameworks plus extenders), and the file path.
//
// Returns (IsTest, TestKind). When the predicate returns (false, TestKindNone),
// the dispatch hook in emitTestBlockChunk SKIPS emission entirely (strict-positive).
type testBlockClassifier func(
	declNode *sitter.Node,
	src []byte,
	captures testBlockCaptures,
	fileCtx ChunkContext,
	filePath string,
) (bool, TestKind)

// testBlockClassifiers registers per-language predicates for call-expression /
// macro / block-style test classification (Bucket B). Mirror testKindClassifiers
// shape at chunker_test_kind.go:37 — map-of-language-to-helper, no entry means
// the per-language TestBlocks query is empty (no test_block chunks emitted in
// the first place) OR a registered query exists without a predicate (transient
// state during phased rollout — chunks emit unclassified).
//
// Empty in this step; populated by per-language phases below.
var testBlockClassifiers = map[Language]testBlockClassifier{}

func init() {
	// Go: filename-driven test classification + FrameworkGoTesting marker.
	testKindClassifiers[LangGo] = classifyTestKindGo
	frameworkExtenders[LangGo] = extendFrameworksGo

	// Python: pytest + unittest dispatch (decorator + name-prefix).
	testKindClassifiers[LangPython] = classifyTestKindPython

	// Java: JUnit / TestNG / JMH annotation dispatch.
	testKindClassifiers[LangJava] = classifyTestKindJava

	// Kotlin: shares the JVM annotation table.
	testKindClassifiers[LangKotlin] = classifyTestKindKotlin

	// Scala: shares the JVM annotation table with sibling-walk fallback.
	testKindClassifiers[LangScala] = classifyTestKindScala

	// C#: short-form attribute_list dispatch.
	testKindClassifiers[LangCSharp] = classifyTestKindCSharp

	// Swift: XCTestCase superclass + setUp/tearDown name dispatch.
	testKindClassifiers[LangSwift] = classifyTestKindSwift

	// Rust: sibling attribute_item walk with closed-set allowlist + framework
	// extender for FrameworkRustTest.
	testKindClassifiers[LangRust] = classifyTestKindRust
	frameworkExtenders[LangRust] = extendFrameworksRust

	// Elixir: filename + setup/setup_all macro names (module-level only —
	// `test`/`describe` blocks are Bucket B).
	testKindClassifiers[LangElixir] = classifyTestKindElixir

	// PHP: PHP-8 #[Test] + PHPDoc @test + class-extends-TestCase.
	testKindClassifiers[LangPHP] = classifyTestKindPHP

	// HCL: filename-only (`.tftest.hcl` -> all blocks are tests). Extender
	// adds FrameworkHCLTfTest for `.tftest.hcl` files since HCL has no
	// Imports query.
	testKindClassifiers[LangHCL] = classifyTestKindHCL
	frameworkExtenders[LangHCL] = extendFrameworksHCL

	// Ruby: Bucket A class+method form for Minitest::Test / Test::Unit::TestCase.
	// Bucket B (block form) registered separately in chunker_ruby.go.
	testKindClassifiers[LangRuby] = classifyTestKindRuby
}
