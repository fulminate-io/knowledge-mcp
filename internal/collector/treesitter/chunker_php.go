// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isPHPTestFile recognizes PHPUnit / Pest layout: `tests/` or `Tests/`
// segments, plus `*Test.php` basenames.
func isPHPTestFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "Test.php") {
		return true
	}
	for seg := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if seg == "tests" || seg == "Tests" {
			return true
		}
	}
	return false
}

// extractPHPAttributes returns the simple names of every PHP-8 attribute on
// a declaration. PHP shape:
//
//	method_declaration
//	  attribute_list
//	    attribute_group: "#[Test]"
//	      attribute
//	        name: "Test"
//
// Strips any leading `\\` namespace separator from the attribute name.
func extractPHPAttributes(declNode *sitter.Node, src []byte) []string {
	if declNode == nil {
		return nil
	}
	var out []string
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		if child.Type() != "attribute_list" {
			continue
		}
		out = append(out, walkPHPAttributeList(child, src)...)
	}
	return out
}

func walkPHPAttributeList(list *sitter.Node, src []byte) []string {
	var out []string
	for i := range int(list.NamedChildCount()) {
		group := list.NamedChild(i)
		// Some PHP grammars wrap attributes in `attribute_group`; others put
		// `attribute` directly under `attribute_list`. Handle both.
		if group.Type() == "attribute_group" {
			for j := range int(group.NamedChildCount()) {
				if name := phpAttributeName(group.NamedChild(j), src); name != "" {
					out = append(out, name)
				}
			}
		} else if group.Type() == "attribute" {
			if name := phpAttributeName(group, src); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

func phpAttributeName(anno *sitter.Node, src []byte) string {
	if anno == nil || anno.Type() != "attribute" {
		return ""
	}
	for i := range int(anno.NamedChildCount()) {
		child := anno.NamedChild(i)
		switch child.Type() {
		case "name":
			return strings.TrimPrefix(child.Content(src), "\\")
		case "qualified_name":
			full := strings.TrimPrefix(child.Content(src), "\\")
			if idx := strings.LastIndex(full, "\\"); idx >= 0 {
				return full[idx+1:]
			}
			return full
		}
	}
	return ""
}

// phpHasPHPDocTag returns true when the declNode's PrevNamedSibling is a
// PHPDoc comment containing the literal token. Matches `@test`,
// `@dataProvider`, etc.
//
// PHP tree-sitter places PHPDoc comments as PrevNamedSibling of the method,
// at the same level inside `declaration_list`. Whitespace separates them.
func phpHasPHPDocTag(declNode *sitter.Node, src []byte, tag string) bool {
	if declNode == nil {
		return false
	}
	sib := declNode.PrevNamedSibling()
	for sib != nil && sib.Type() == "comment" {
		text := sib.Content(src)
		if strings.HasPrefix(strings.TrimSpace(text), "/**") && strings.Contains(text, tag) {
			return true
		}
		sib = sib.PrevNamedSibling()
	}
	return false
}

// phpEnclosingClassExtendsTestCase walks the parent chain looking for a
// class_declaration with a `base_clause` containing the name `TestCase`.
// Tree-sitter PHP shape: `class_declaration > base_clause > name`.
func phpEnclosingClassExtendsTestCase(node *sitter.Node, src []byte) bool {
	if node == nil {
		return false
	}
	for p := node.Parent(); p != nil; p = p.Parent() {
		if p.Type() != "class_declaration" {
			continue
		}
		for i := range int(p.NamedChildCount()) {
			child := p.NamedChild(i)
			if child.Type() != "base_clause" {
				continue
			}
			if strings.Contains(child.Content(src), "TestCase") {
				return true
			}
		}
	}
	return false
}

// classifyTestBlockPHP covers Pest's call-style: `test('...', fn () => ...)`,
// `it('...', fn () => ...)`, `describe('...', function () {...})`,
// `beforeEach`, `afterEach`, `beforeAll`, `afterAll`, and `dataset`.
//
// Reuses callExpressionName (chunker_identity.go:121) — Phase 1 extension
// handles PHP's `name` leaf node. PHP's tree-sitter grammar uses
// function_call_expression (not call_expression) but the helper reads
// ChildByFieldName("function") which works for both node types.
func classifyTestBlockPHP(
	declNode *sitter.Node,
	src []byte,
	_ testBlockCaptures,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isPHPTestFile(filePath) {
		return false, TestKindNone
	}
	switch callExpressionName(declNode, src) {
	case "test", "it":
		return true, TestKindTest
	case "describe", "context":
		return true, TestKindTest
	case "beforeEach", "beforeAll":
		return true, TestKindSetup
	case "afterEach", "afterAll":
		return true, TestKindTeardown
	case "dataset":
		return true, TestKindFixture
	}
	return false, TestKindNone
}

func init() {
	testBlockClassifiers[LangPHP] = classifyTestBlockPHP
}

// classifyTestKindPHP recognizes PHPUnit's three detection paths in PHP:
// PHP-8 `#[Test]` attribute, PHPDoc `@test` annotation, and class-extends-
// TestCase plus name-prefix `test`. `@dataProvider` annotations classify
// the method as Fixture.
//
// Pest framework (call-expression `test('name', fn() => ...)`) is Bucket B.
func classifyTestKindPHP(
	declNode *sitter.Node,
	src []byte,
	chunkType, name string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isPHPTestFile(filePath) {
		return false, TestKindNone
	}
	if chunkType != "method_declaration" && chunkType != "function_definition" {
		return true, TestKindHelper
	}
	switch name {
	case "setUp", "setUpBeforeClass":
		return true, TestKindSetup
	case "tearDown", "tearDownAfterClass":
		return true, TestKindTeardown
	}
	annos := extractPHPAttributes(declNode, src)
	if slices.Contains(annos, "Test") {
		return true, TestKindTest
	}
	if phpHasPHPDocTag(declNode, src, "@dataProvider") {
		return true, TestKindFixture
	}
	if phpHasPHPDocTag(declNode, src, "@test") {
		return true, TestKindTest
	}
	if strings.HasPrefix(name, "test") && phpEnclosingClassExtendsTestCase(declNode, src) {
		return true, TestKindTest
	}
	return true, TestKindHelper
}
