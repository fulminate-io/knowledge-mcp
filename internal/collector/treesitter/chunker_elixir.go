// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isElixirTestFile recognizes ExUnit's discovery rules: `_test.exs`/`_test.ex`
// basenames or a `test/` path segment.
func isElixirTestFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "_test.exs") || strings.HasSuffix(base, "_test.ex") {
		return true
	}
	return slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "test")
}

// classifyTestKindElixir classifies Elixir top-level call expressions in
// ExUnit test modules. Per locked scope, `test "name" do ... end` blocks
// are Bucket B's territory — Bucket A returns Helper for those (a
// test_block chunk is emitted in parallel by walkTestBlocks).
//
// Recognized:
//
//	setup       -> Setup
//	setup_all   -> Setup (ExUnit's once-per-module setup; no teardown_all
//	               equivalent — cleanup uses on_exit/1).
//	default     -> Helper (defmodule, def, test, describe, etc.)
func classifyTestKindElixir(
	_ *sitter.Node,
	_ []byte,
	_, name string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isElixirTestFile(filePath) {
		return false, TestKindNone
	}
	switch name {
	case "setup", "setup_all":
		return true, TestKindSetup
	}
	return true, TestKindHelper
}

// classifyTestBlockElixir covers ExUnit's block-form macros:
// `test "..." do ... end`, `describe "..." do ... end`, `setup do ... end`,
// `setup_all do ... end`, `property "..." do ... end` (StreamData), and
// `on_exit do ... end` cleanup hooks.
func classifyTestBlockElixir(
	declNode *sitter.Node,
	src []byte,
	_ testBlockCaptures,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isElixirTestFile(filePath) {
		return false, TestKindNone
	}
	switch elixirCallTarget(declNode, src) {
	case "test":
		return true, TestKindTest
	case "describe":
		return true, TestKindTest
	case "property":
		return true, TestKindTest
	case "setup", "setup_all", "setup_with_mocks":
		return true, TestKindSetup
	case "on_exit":
		return true, TestKindTeardown
	}
	return false, TestKindNone
}

// elixirCallTarget extracts the callee identifier from an Elixir tree-sitter
// `call` node.
//
// REUSE NOTE (T2-A): mirrors the SHAPE of callExpressionName at
// chunker_identity.go:121, but reads field `target` because Elixir's
// tree-sitter grammar names the callee leaf via `target` rather than
// `function`. Genuine field-name divergence in the upstream grammar — not
// a copy-paste fork.
func elixirCallTarget(declNode *sitter.Node, src []byte) string {
	if declNode == nil {
		return ""
	}
	target := declNode.ChildByFieldName("target")
	if target != nil && target.Type() == "identifier" {
		return target.Content(src)
	}
	return ""
}

func init() {
	testBlockClassifiers[LangElixir] = classifyTestBlockElixir
}
