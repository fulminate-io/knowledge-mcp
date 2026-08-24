// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isOCamlTestFile recognizes Dune convention: filename suffix `_test.ml`,
// `test_*.ml`, and path segments `test/` / `tests/`.
func isOCamlTestFile(filePath string) bool {
	base := filepath.Base(filePath)
	if strings.HasSuffix(base, "_test.ml") || strings.HasSuffix(base, "_test.mli") {
		return true
	}
	if strings.HasPrefix(base, "test_") {
		ext := filepath.Ext(base)
		if ext == ".ml" || ext == ".mli" {
			return true
		}
	}
	for seg := range strings.SplitSeq(filepath.ToSlash(filePath), "/") {
		if seg == "test" || seg == "tests" {
			return true
		}
	}
	return false
}

// hasInlineTestExtension returns true when declNode is a value_definition
// with an `attribute_id "test"` child — i.e. `let%test "name" = expr`.
//
// Per locked Q6 outcome 1 (verified empirically): tree-sitter-ocaml emits
// `(value_definition (attribute_id "test") (let_binding pattern: (string)))`
// for the ppx_inline_test let%test form. T3-D fix: restrict matching to
// value_definitions with the explicit "test" attribute_id — plain
// `let foo = 42` (no attribute) returns false.
func hasInlineTestExtension(declNode *sitter.Node, src []byte) bool {
	if declNode == nil {
		return false
	}
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		if child.Type() != "attribute_id" {
			continue
		}
		if strings.TrimSpace(child.Content(src)) == "test" {
			return true
		}
	}
	return false
}

// classifyTestBlockOCaml covers Alcotest (`Alcotest.test_case "name" ...`,
// always shipped — application_expression parses cleanly) and
// ppx_inline_test (`let%test "name" = expr`, gated on locked Q6 verification —
// outcome 1 confirmed clean parsing).
func classifyTestBlockOCaml(
	declNode *sitter.Node,
	src []byte,
	_ testBlockCaptures,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isOCamlTestFile(filePath) {
		return false, TestKindNone
	}
	switch declNode.Type() {
	case "application_expression":
		// The TestBlocks query's #match? regex already gated to Alcotest
		// callees — any application_expression matched here is a test.
		return true, TestKindTest
	case "value_definition":
		// T3-D: only classify value_definitions with the `let%test` extension.
		if hasInlineTestExtension(declNode, src) {
			return true, TestKindTest
		}
	}
	return false, TestKindNone
}

// resolveDeclNameOCaml names OCaml's three chunked declaration kinds.
//
// The name always lives one level down: module_definition, value_definition and
// type_definition expose NO fields of their own — the binding child does. That
// is the same grammar fact that makes module_binding, not module_definition,
// the container kind in classLikeByLang's OCaml row.
//
// The kind checks are what keep the two negatives unnamed while KEEPING their
// chunks: `let () = ...` binds a unit pattern rather than a value_name, and
// `let%test "x" = ...` binds a string. Both resolve to "" and are still chunked,
// which is exactly what a tightened query would not have done.
func resolveDeclNameOCaml(declNode *sitter.Node, src []byte, chunkType string) string {
	switch chunkType {
	case "value_definition":
		if lb := firstNamedChildOfKind(declNode, "let_binding"); lb != nil {
			return fieldNamed(lb, src, "pattern", "value_name")
		}
	case "type_definition":
		if tb := firstNamedChildOfKind(declNode, "type_binding"); tb != nil {
			return fieldNamed(tb, src, "name", "type_constructor")
		}
	case "module_definition":
		if mb := firstNamedChildOfKind(declNode, "module_binding"); mb != nil {
			return fieldNamed(mb, src, "name", "module_name")
		}
	}
	return ""
}

func init() {
	testBlockClassifiers[LangOCaml] = classifyTestBlockOCaml
	declNameResolvers[LangOCaml] = resolveDeclNameOCaml
}
