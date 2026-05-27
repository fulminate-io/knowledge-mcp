// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// classifyTestKindKotlin uses extractJVMAnnotations + jvmAnnotationKind,
// adding Kotest's `Test` / `StringSpec` / `DescribeSpec` recognition only
// at the class level — most Kotest tests are call-expression / lambda body
// shapes that are Bucket B's territory. Bucket A reports class-level
// chunks as helpers.
//
// Kotest has no class-level @Test annotation; KotlinTest classes are
// detected via class-name-prefix heuristics (out of scope for now). For
// vanilla JUnit-on-Kotlin (kotest-junit5 included), the JVM annotation
// dispatch table covers @Test / @BeforeEach / @AfterEach.
func classifyTestKindKotlin(
	declNode *sitter.Node,
	src []byte,
	chunkType, _ string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isKotlinTestFile(filePath) {
		return false, TestKindNone
	}
	if chunkType == "class_declaration" || chunkType == "object_declaration" {
		return true, TestKindHelper
	}
	annos := extractJVMAnnotations(declNode, src)
	if kind, ok := jvmAnnotationKind(annos); ok {
		return true, kind
	}
	return true, TestKindHelper
}

// classifyTestBlockKotlin covers Kotest (FunSpec/DescribeSpec/BehaviorSpec/
// StringSpec) and Spek call-style shapes. Reuses callExpressionName
// (chunker_identity.go:121) — post Phase 1 the helper handles
// simple_identifier and falls back to first-named-child when the call_expression
// has no `function:` field (Kotlin's grammar uses positional children).
func classifyTestBlockKotlin(
	declNode *sitter.Node,
	src []byte,
	_ testBlockCaptures,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isKotlinTestFile(filePath) {
		return false, TestKindNone
	}
	fn := callExpressionName(declNode, src)
	switch fn {
	case "test", "it", "context", "describe", "by", "should", "expect":
		return true, TestKindTest
	case "given", "when", "then", "Given", "When", "Then":
		return true, TestKindTest
	case "feature", "scenario":
		return true, TestKindTest
	case "FunSpec", "DescribeSpec", "BehaviorSpec", "StringSpec", "FreeSpec", "WordSpec":
		// Spec-class instantiation — the Kotest constructor takes a lambda
		// of test definitions; treat the spec block itself as helper-shape
		// (the inner test/it/describe calls are the real test_block chunks).
		return true, TestKindHelper
	case "beforeTest", "beforeEach", "beforeSpec", "beforeEachTest", "beforeAll":
		return true, TestKindSetup
	case "afterTest", "afterEach", "afterSpec", "afterEachTest", "afterAll":
		return true, TestKindTeardown
	}
	return false, TestKindNone
}

func init() {
	testBlockClassifiers[LangKotlin] = classifyTestBlockKotlin
}
