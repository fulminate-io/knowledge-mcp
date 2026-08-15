// SPDX-License-Identifier: Apache-2.0

package treesitter

func luaQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_statement) @decl
			(variable_declaration) @decl
		]`,
		// Capturing the function_call WHOLE put the arguments inside the callee
		// text. These three arms capture the callee alone.
		//
		// THE MIDDLE ARM CLOSES A SILENT EDGE LOSS rather than a stripped
		// qualifier. For the chained form `a.b(1).c(2)` this grammar nests the
		// inner call as the OUTER call's first prefix child, so neither of the
		// other two arms matches the outer call at all — both require the first
		// named child to be an identifier — and the language would emit ONE
		// callee where the whole-node capture emitted two. With this arm the
		// same source yields `a.b` from the inner call and `c` from the outer.
		//
		// THE BARE ARM GOES LAST so it cannot pre-empt either qualified shape.
		Calls: `[
			(function_call . prefix: (identifier) @callee (identifier) @callee . (function_call_paren))
			(function_call . (function_call) (identifier) @callee . (function_call_paren))
			(function_call . (identifier) @callee . (function_call_paren))
		]`,
		// Lua has no import statement and no type syntax: its module system is
		// the runtime `require` call, so there is nothing static to capture and
		// no BindsResolver arm to register.
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
