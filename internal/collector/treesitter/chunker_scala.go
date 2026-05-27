// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// classifyTestKindScala covers JUnit-style annotations on Scala defs and
// adds ScalaTest / Specs2 / MUnit class-membership signals via the file's
// detected frameworks (chunk.Context.Frameworks). ScalaTest's actual test
// cases are call-expression / DSL shapes (FunSuite, FlatSpec, etc.) that
// Bucket B handles.
//
// For Bucket A, Scala is essentially "JVM annotation dispatch + classes in
// test files = helper unless annotated otherwise."
func classifyTestKindScala(
	declNode *sitter.Node,
	src []byte,
	chunkType, _ string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isScalaTestFile(filePath) {
		return false, TestKindNone
	}
	if chunkType == "class_definition" || chunkType == "trait_definition" ||
		chunkType == "object_definition" || chunkType == "val_definition" {
		return true, TestKindHelper
	}
	annos := extractScalaAnnotations(declNode, src)
	if kind, ok := jvmAnnotationKind(annos); ok {
		return true, kind
	}
	return true, TestKindHelper
}

// classifyTestBlockScala covers ScalaTest (FunSuite/DescribeSpec/FunSpec/
// FlatSpec/FreeSpec/WordSpec), MUnit, and Specs2.
//
// Two call shapes:
//
//   - Direct call form: `test("works") { ... }`. callExpressionName returns
//     "" for the OUTER call (whose function is itself a call_expression);
//     the classifier-local outer-call unwrap descends one level to the
//     inner call's identifier (mirrors JS Pattern C round-5 fix).
//
//   - Infix form: `"x" should "y" in { ... }`. The classifier extracts the
//     operator identifier from the infix_expression's `operator` field —
//     the @fn capture in the query already binds to that operator, but
//     declNode here is the whole infix_expression, so we read the operator
//     directly. The identifier values that map to TestKindTest are the
//     same set (`should`/`must`/`in`).
func classifyTestBlockScala(
	declNode *sitter.Node,
	src []byte,
	_ testBlockCaptures,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isScalaTestFile(filePath) {
		return false, TestKindNone
	}
	fn := scalaCallOrInfixIdentifier(declNode, src)
	switch fn {
	case "test", "it", "in", "should", "must":
		return true, TestKindTest
	case "describe", "context", "feature", "scenario":
		return true, TestKindTest
	case "beforeAll", "beforeEach", "before":
		return true, TestKindSetup
	case "afterAll", "afterEach", "after":
		return true, TestKindTeardown
	}
	return false, TestKindNone
}

// scalaCallOrInfixIdentifier extracts the call name. Tries the shared
// callExpressionName first; for the trailing-block call shape (outer call
// whose function is itself a call_expression) it unwraps one level. For
// infix_expression nodes it reads the `operator` field directly. This
// keeps the shared helper's contract unchanged — Scala's
// infix_expression is genuinely not a call_expression.
func scalaCallOrInfixIdentifier(declNode *sitter.Node, src []byte) string {
	if declNode == nil {
		return ""
	}
	switch declNode.Type() {
	case "call_expression":
		if name := callExpressionName(declNode, src); name != "" {
			return name
		}
		// Outer-call unwrap (mirrors JS Pattern C): when @decl is the outer
		// call of `test("name") { ... }`, the function field is itself a
		// call_expression rather than identifier. Descend one level.
		if outerFn := declNode.ChildByFieldName("function"); outerFn != nil && outerFn.Type() == "call_expression" {
			return callExpressionName(outerFn, src)
		}
	case "infix_expression":
		op := declNode.ChildByFieldName("operator")
		if op != nil && op.Type() == "identifier" {
			return op.Content(src)
		}
	}
	return ""
}

func init() {
	testBlockClassifiers[LangScala] = classifyTestBlockScala
}
