// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isLuaTestFile recognizes busted (`*_spec.lua`) and LuaUnit
// (`test_*.lua` / `*_test.lua`) filename conventions, plus a `spec/`
// path segment combined with `_spec.lua` filename.
func isLuaTestFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "_spec.lua") {
		return true
	}
	if strings.HasSuffix(base, "_test.lua") {
		return true
	}
	if strings.HasPrefix(base, "test_") && filepath.Ext(base) == ".lua" {
		return true
	}
	return false
}

// luaCallPrefix extracts the callee identifier from a Lua tree-sitter
// `function_call` node. Lua's grammar names the callee leaf via the
// `prefix` field — genuine divergence from `function` / `method` /
// `target` field-name conventions in other grammars.
//
// REUSE NOTE (T2-A): mirrors the SHAPE of callExpressionName at
// chunker_identity.go:121, but reads field `prefix` because Lua's
// upstream grammar uses that name.
func luaCallPrefix(declNode *sitter.Node, src []byte) string {
	if declNode == nil {
		return ""
	}
	prefix := declNode.ChildByFieldName("prefix")
	if prefix != nil && prefix.Type() == "identifier" {
		return prefix.Content(src)
	}
	return ""
}

// classifyTestKindLua covers LuaUnit's declaration-style:
// `function TestSuite:testFoo()`. The function_statement node's `name`
// field is a `function_name` whose children include the table identifier
// (TestSuite), a `:` separator, and the method identifier (testFoo). We
// inspect the function_name to recover the table/method pair.
func classifyTestKindLua(
	declNode *sitter.Node,
	src []byte,
	chunkType, _ string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isLuaTestFile(filePath) {
		return false, TestKindNone
	}
	if chunkType != "function_statement" {
		return true, TestKindHelper
	}
	tableName, methodName := luaFunctionStatementParts(declNode, src)
	// Top-level functions in test files (no receiver) → helper.
	if tableName == "" {
		return true, TestKindHelper
	}
	// Functions on non-Test* tables → helper inside test file.
	if !strings.HasPrefix(tableName, "Test") {
		return true, TestKindHelper
	}
	switch methodName {
	case "setUp", "setUpClass":
		return true, TestKindSetup
	case "tearDown", "tearDownClass":
		return true, TestKindTeardown
	}
	if strings.HasPrefix(methodName, "test") || strings.HasPrefix(methodName, "Test") {
		return true, TestKindTest
	}
	return true, TestKindHelper
}

// luaFunctionStatementParts returns the (table, method) pair for a Lua
// function_statement whose name uses `Table:method()` syntax. For plain
// `function foo()` returns ("", "foo"). For `function Table.method()`
// returns ("Table", "method").
func luaFunctionStatementParts(declNode *sitter.Node, src []byte) (string, string) {
	if declNode == nil {
		return "", ""
	}
	nameNode := declNode.ChildByFieldName("name")
	if nameNode == nil {
		return "", ""
	}
	if nameNode.Type() == "identifier" {
		return "", nameNode.Content(src)
	}
	// function_name has multiple children: identifier, separator, identifier.
	var ids []string
	for i := 0; i < int(nameNode.NamedChildCount()); i++ {
		child := nameNode.NamedChild(i)
		if child.Type() == "identifier" {
			ids = append(ids, child.Content(src))
		}
	}
	if len(ids) >= 2 {
		return ids[0], ids[len(ids)-1]
	}
	if len(ids) == 1 {
		return "", ids[0]
	}
	return "", ""
}

// classifyTestBlockLua covers busted's call-style DSL:
// `it("...", function() ... end)`, `describe("...", function() ... end)`,
// `before_each(function() ... end)`, etc.
func classifyTestBlockLua(
	declNode *sitter.Node,
	src []byte,
	_ testBlockCaptures,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isLuaTestFile(filePath) {
		return false, TestKindNone
	}
	switch luaCallPrefix(declNode, src) {
	case "it", "test", "spec":
		return true, TestKindTest
	case "describe", "context", "feature", "scenario":
		return true, TestKindTest
	case "pending":
		return true, TestKindHelper
	case "before_each", "before", "setup", "lazy_setup", "strict_setup":
		return true, TestKindSetup
	case "after_each", "after", "teardown", "lazy_teardown", "strict_teardown", "finally":
		return true, TestKindTeardown
	case "insulate", "expose", "randomize":
		return true, TestKindTest
	}
	return false, TestKindNone
}

func init() {
	testKindClassifiers[LangLua] = classifyTestKindLua
	testBlockClassifiers[LangLua] = classifyTestBlockLua
}
