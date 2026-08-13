// SPDX-License-Identifier: Apache-2.0

package treesitter

func elmQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(value_declaration) @decl
			(type_declaration) @decl
			(type_alias_declaration) @decl
		]`,
		Calls:    "",
		Imports:  `(import_clause) @path`,
		TypeRefs: "",
		// TestBlocks: elm-test `Test.test "name" (\_ -> ...)`,
		// `Test.fuzz fuzzer "name" (\val -> ...)`, `Test.describe "name" [...]`.
		//
		// Empirical shape (verified via tree-sitter parse):
		// `(function_call_expr target: (value_expr name: (value_qid)) arg: ...)`.
		// The plan spec said `function_call_target / qualified_name` but the
		// actual upstream grammar uses `target: (value_expr name: (value_qid))`.
		// Both query and chunker_elm.go updated in lockstep (round-4 fix).
		TestBlocks: `([
			(function_call_expr
				target: (value_expr name: (value_qid) @fn)
				arg: (string_constant_expr) @name
			) @decl
			(function_call_expr
				target: (value_expr name: (value_qid) @fn)
				arg: (value_expr)
				arg: (string_constant_expr) @name
			) @decl
		] (#match? @fn "^(Test\\.test|Test\\.fuzz|Test\\.fuzz2|Test\\.fuzz3|Test\\.describe|Test\\.skip|Test\\.only|test|fuzz|describe)$"))`,
	}
}
