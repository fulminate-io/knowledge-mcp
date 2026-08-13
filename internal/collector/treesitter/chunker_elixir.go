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

// resolveDeclNameElixir names an Elixir definition after the entity it defines
// rather than after the macro that defines it. The TopLevel query binds the
// macro keyword as @kw and no @name, so every admitted definition arrives here
// unnamed and the real name is read out of the call's first argument, in the
// four shapes the grammar produces:
//
//	alias           defmodule MyApp.Worker      -> MyApp.Worker
//	call            def perform(arg)            -> perform
//	identifier      def no_args do              -> no_args
//	binary_operator def with_guard(x) when ...  -> with_guard  (the left of `when`)
//
// Anything else resolves to "" and leaves the chunk unnamed exactly as an
// unnamed chunk behaves today — `defstruct [:a, :b]` takes that path, because
// it defines fields rather than a named entity.
func resolveDeclNameElixir(declNode *sitter.Node, src []byte, chunkType string) string {
	if chunkType != "call" {
		return ""
	}
	args := firstNamedChildOfKind(declNode, "arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return ""
	}
	arg := args.NamedChild(0)
	switch arg.Type() {
	case "alias", "identifier":
		return arg.Content(src)
	case "call":
		return elixirCallTargetName(arg, src)
	case "binary_operator":
		// A guard clause: the head is the operator's left side.
		left := arg.ChildByFieldName("left")
		if left == nil {
			return ""
		}
		switch left.Type() {
		case "call":
			return elixirCallTargetName(left, src)
		case "identifier":
			return left.Content(src)
		}
	}
	return ""
}

// elixirCallTargetName returns a call's target when it is a plain identifier.
// A qualified target is a dot operator rather than an identifier and yields "".
func elixirCallTargetName(call *sitter.Node, src []byte) string {
	return fieldNamed(call, src, "target", "identifier")
}

func init() {
	testBlockClassifiers[LangElixir] = classifyTestBlockElixir
	declNameResolvers[LangElixir] = resolveDeclNameElixir
}
