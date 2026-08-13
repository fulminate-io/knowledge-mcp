// SPDX-License-Identifier: Apache-2.0

package treesitter

func luaQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_statement) @decl
			(variable_declaration) @decl
		]`,
		Calls:    `(function_call) @callee`,
		Imports:  "",
		TypeRefs: "",
		// TestBlocks: busted DSL.
		// Tree-sitter Lua's function_call uses field `prefix` for the callee
		// and `args` for the function_arguments. The function lambda is
		// passed as a regular argument inside function_arguments.
		//
		// Documented gap: nested @parent_name binding not captured (mirrors
		// JS/Kotlin/Scala/Elixir/PHP gap). The inner it() inside describe()
		// emits with ParentName="" — astPathHash uniquely identifies each.
		TestBlocks: `([
			(function_call
				prefix: (identifier) @fn
				args: (function_arguments
					(string) @name
					(function) @params
				)
			) @decl
			(function_call
				prefix: (identifier) @fn
				args: (function_arguments
					(function) @params
				)
			) @decl
		] (#match? @fn "^(it|test|spec|pending|describe|context|feature|scenario|insulate|expose|randomize|before_each|after_each|setup|teardown|before|after|lazy_setup|lazy_teardown|strict_setup|strict_teardown|finally)$"))`,
	}
}
