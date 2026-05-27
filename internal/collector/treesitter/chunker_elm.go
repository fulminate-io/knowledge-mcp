// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isElmTestFile recognizes elm-test convention: filename suffix `.elm`
// inside a `tests/` segment. elm-test puts spec files under tests/.
func isElmTestFile(filePath string) bool {
	if filepath.Ext(filePath) != ".elm" {
		return false
	}
	return slices.Contains(strings.Split(filepath.ToSlash(filePath), "/"), "tests")
}

// elmCallTarget extracts the qualified callee name from an Elm
// `function_call_expr` node. Tree-sitter Elm shape:
//
//	(function_call_expr
//	  target: (value_expr name: (value_qid (...)))
//	  arg: ...)
//
// Cannot reuse callExpressionName (chunker_identity.go:121) because Elm
// has no `function` field — `target` is the field name and the
// callee leaf is nested two levels deep (value_expr → value_qid). Same
// precedent pattern as rubyCallIdentifier and elixirCallTarget.
func elmCallTarget(callNode *sitter.Node, src []byte) string {
	if callNode == nil {
		return ""
	}
	target := callNode.ChildByFieldName("target")
	if target == nil {
		return ""
	}
	nameNode := target.ChildByFieldName("name")
	if nameNode == nil {
		return ""
	}
	return nameNode.Content(src)
}

// classifyTestBlockElm covers elm-test:
//
//	Test.test "name" (\_ -> ...)             → test
//	Test.fuzz / Test.fuzz2 / Test.fuzz3      → fuzz
//	Test.describe "name" [...]               → test
//	Test.skip / Test.only                    → test
func classifyTestBlockElm(
	declNode *sitter.Node,
	src []byte,
	_ testBlockCaptures,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isElmTestFile(filePath) {
		return false, TestKindNone
	}
	switch elmCallTarget(declNode, src) {
	case "Test.test", "test":
		return true, TestKindTest
	case "Test.fuzz", "Test.fuzz2", "Test.fuzz3", "fuzz":
		return true, TestKindFuzz
	case "Test.describe", "describe":
		return true, TestKindTest
	case "Test.skip", "Test.only":
		return true, TestKindTest
	}
	return false, TestKindNone
}

func init() {
	testBlockClassifiers[LangElm] = classifyTestBlockElm
}
