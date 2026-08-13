// SPDX-License-Identifier: Apache-2.0

package treesitter

func elixirQueries() *QuerySet {
	return &QuerySet{
		// TopLevel: Elixir has no declaration node kind — a definition is a
		// `call` whose target is a macro keyword, and so is every other
		// expression in the language. Binding that keyword as @name is what
		// made every definition a chunk named after its macro, so it is bound
		// as @kw instead: the chunk builder reads only @decl and @name, while
		// the predicate below still sees @kw and drops anything that is not a
		// definition macro. `assert 1 + 1 == 2` and `use ExUnit.Case` stop
		// being declarations; the real name comes from the arguments, via the
		// per-language declNameResolver in chunker_elixir.go.
		//
		// The `target: (identifier)` field is deliberately KEPT. A bare
		// `(call) @decl` is NOT equivalent — a qualified call's target is a
		// dot operator rather than an identifier, so dropping the field adds a
		// chunk for every Enum.map / String.upcase in the file.
		//
		// The allowlist is the language's own definition macros. A
		// project-defined macro (Ecto's `schema`, Phoenix's `plug`) is
		// deliberately absent: those are configuration DSL calls, not
		// declarations. The omission is a boundary, not an oversight.
		// THE OUTER PARENTHESES AROUND PATTERN-PLUS-PREDICATE ARE LOAD-BEARING.
		// A predicate written as a sibling S-expression after the pattern
		// compiles into a SEPARATE pattern that carries no captures, leaving
		// the capture-bearing pattern unfiltered: measured at PatternCount 2
		// with the predicate on pattern 1, and the with-captures count then
		// identical to the same query with no predicate at all. Grouped this
		// way it compiles to one pattern holding both, and the with-captures
		// count drops to the definition macros alone. Compare WITH-CAPTURES
		// counts when checking this, never raw cursor matches — the raw count
		// RISES sharply under a predicate because rejected alternatives surface
		// as zero-capture matches.
		TopLevel: `((call target: (identifier) @kw) @decl
		(#match? @kw "^(defmodule|defprotocol|defimpl|def|defp|defmacro|defmacrop|defdelegate|defguard|defguardp|defstruct|defexception)$"))`,
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
		TestBlocks: `([
			(call
				target: (identifier) @fn
				(arguments (string) @name)
				(do_block) @params
			) @decl
			(call
				target: (identifier) @fn
				(do_block) @params
			) @decl
		] (#match? @fn "^(test|describe|setup|setup_all|setup_with_mocks|on_exit|property)$"))`,
	}
}
