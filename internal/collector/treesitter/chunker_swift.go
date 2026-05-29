// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isSwiftTestFile recognizes *Tests.swift basenames or a `Tests/` segment.
func isSwiftTestFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "Tests.swift") || strings.HasSuffix(base, "Test.swift") {
		return true
	}
	return slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "Tests")
}

// swiftEnclosingXCTestCase walks the parent chain looking for a
// class_declaration. Once found, inspects its `inheritance_specifier`
// NamedChildren for the simple type name `XCTestCase`. Tree-sitter Swift
// shape:
//
//	class_declaration
//	  type_identifier              ← class name
//	  inheritance_specifier
//	    user_type
//	      type_identifier          ← superclass simple name
//	  class_body
//	    function_declaration ...
//
// Returns true when any enclosing class extends XCTestCase. Subclasses of
// subclasses (e.g. `class MyBase: XCTestCase` then `class FooTests:
// MyBase`) are NOT detected — only direct superclass matches. This is the
// 80% case; deeper transitive detection would require resolving every base
// class in the project, which is out of scope.
func swiftEnclosingXCTestCase(node *sitter.Node, src []byte) bool {
	if node == nil {
		return false
	}
	for p := node.Parent(); p != nil; p = p.Parent() {
		if p.Type() != "class_declaration" {
			continue
		}
		// Walk class_declaration's NamedChildren for inheritance_specifier(s).
		for i := range int(p.NamedChildCount()) {
			child := p.NamedChild(i)
			if child.Type() != "inheritance_specifier" {
				continue
			}
			// Check every type_identifier descendant for "XCTestCase".
			if findInheritanceMatch(child, src, "XCTestCase") {
				return true
			}
		}
	}
	return false
}

// findInheritanceMatch searches an inheritance_specifier subtree for a
// type_identifier whose content matches `want`.
func findInheritanceMatch(node *sitter.Node, src []byte, want string) bool {
	if node == nil {
		return false
	}
	if node.Type() == "type_identifier" && node.Content(src) == want {
		return true
	}
	for i := range int(node.NamedChildCount()) {
		if findInheritanceMatch(node.NamedChild(i), src, want) {
			return true
		}
	}
	return false
}

// classifyTestKindSwift covers XCTestCase-based tests AND Swift Testing's
// @Test macro on function_declaration. `measure { }` block detection lives
// in classifyTestBlockSwift (Bucket B leaf chunk per locked Q3).
//
// Test signals:
//
//	@Test macro on function_declaration     -> Test
//	setUp / setUpWithError                  -> Setup
//	tearDown / tearDownWithError            -> Teardown
//	`test` prefix in XCTestCase subclass    -> Test
//	everything else in test file            -> Helper
func classifyTestKindSwift(
	declNode *sitter.Node,
	src []byte,
	chunkType, name string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isSwiftTestFile(filePath) {
		return false, TestKindNone
	}
	if chunkType != "function_declaration" {
		return true, TestKindHelper
	}
	// Swift Testing's @Test macro — joins Bucket A because it decorates a
	// function_declaration (Bucket A territory).
	if swiftHasTestMacro(declNode, src) {
		return true, TestKindTest
	}
	switch name {
	case "setUp", "setUpWithError":
		return true, TestKindSetup
	case "tearDown", "tearDownWithError":
		return true, TestKindTeardown
	}
	if strings.HasPrefix(name, "test") && swiftEnclosingXCTestCase(declNode, src) {
		return true, TestKindTest
	}
	return true, TestKindHelper
}

// swiftHasTestMacro returns true when declNode (a function_declaration) is
// decorated with the @Test macro attribute. Tree-sitter Swift parses
// `@Test func foo()` as `function_declaration > modifiers > attribute >
// user_type > type_identifier "Test"`. The Contains check is the
// conservative fallback per Bucket A grammar-drift policy.
func swiftHasTestMacro(declNode *sitter.Node, src []byte) bool {
	if declNode == nil {
		return false
	}
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		t := child.Type()
		if t != "modifiers" && t != "attribute" {
			continue
		}
		// Attribute textual form on Swift: "@Test", "@Test(.tags(.foo))".
		// Contains "@Test" is sufficient because attribute names are
		// case-sensitive in Swift and "@Testable" / "@TestRule" don't
		// appear in the wild. Keeps the helper minimal and resilient
		// to grammar drift on the inner attribute node names.
		if strings.Contains(child.Content(src), "@Test") {
			return true
		}
	}
	return false
}

// classifyTestBlockSwift classifies XCTest's measure { ... } and
// measureMetrics { ... } as TestKindBenchmark (locked Q3).
//
// Reuses callExpressionName (chunker_identity.go:121) — Phase 1 extension
// handles Swift's simple_identifier and the field-less fallback to first
// named child for grammars without a `function:` field.
func classifyTestBlockSwift(
	declNode *sitter.Node,
	src []byte,
	_ testBlockCaptures,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isSwiftTestFile(filePath) {
		return false, TestKindNone
	}
	// Strict-positive: only fire on measure / measureMetrics inside an
	// XCTestCase subclass. Outside XCTestCase the call is not a benchmark.
	if !swiftEnclosingXCTestCase(declNode, src) {
		return false, TestKindNone
	}
	switch callExpressionName(declNode, src) {
	case "measure", "measureMetrics":
		return true, TestKindBenchmark
	}
	return false, TestKindNone
}

func init() {
	testBlockClassifiers[LangSwift] = classifyTestBlockSwift
}
