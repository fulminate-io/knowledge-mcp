// SPDX-License-Identifier: Apache-2.0

package treesitter

func elixirQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `(call target: (identifier) @name) @decl`,
		Calls:    `(call target: (identifier) @callee)`,
		Imports:  "",
		TypeRefs: "",
		// TestBlocks: ExUnit block-form `test "name" do ... end`,
		// `describe "name" do ... end`, `setup do ... end`,
		// `setup_all do ... end`. Tree-sitter Elixir uses field `target`
		// for the callee leaf (not `function`).
		//
		// Documented gap: nested @parent_name binding not captured.
		// Same tree-sitter limitation as JS/Kotlin/Scala — adding a
		// nested-pattern that ALSO matches the inner test would
		// double-emit (the inner call also matches the bare pattern).
		// Chunk.ParentName == "" for nested test() inside describe().
		TestBlocks: `[
			(call
				target: (identifier) @fn
				(arguments (string) @name)
				(do_block) @params
			) @decl
			(call
				target: (identifier) @fn
				(do_block) @params
			) @decl
		] (#match? @fn "^(test|describe|setup|setup_all|setup_with_mocks|on_exit|property)$")`,
	}
}
