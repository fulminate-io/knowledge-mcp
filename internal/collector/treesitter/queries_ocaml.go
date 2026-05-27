// SPDX-License-Identifier: Apache-2.0

package treesitter

func ocamlQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(value_definition) @decl
			(type_definition) @decl
			(module_definition) @decl
		]`,
		Calls:    "",
		Imports:  `(open_module) @path`,
		TypeRefs: "",
		// TestBlocks: Alcotest's `Alcotest.test_case "name" `Quick fn` parses
		// as `(application_expression function: (value_path) argument: (string))`.
		// ppx_inline_test's `let%test "name" = expr` parses as
		// `(value_definition (attribute_id "test") (let_binding pattern: (string)))`
		// — verified empirically (locked Q6 outcome 1: clean parsing). Both
		// paths shipped.
		TestBlocks: `[
			(application_expression
				function: (value_path) @fn
				argument: (string) @name
			) @decl
			(value_definition
				(attribute_id) @ext
				(let_binding pattern: (string) @name)
			) @decl
		] (#match? @fn "^(Alcotest\\.test_case|Alcotest\\.test|Alcotest_lwt\\.test_case|Alcotest_lwt\\.test|test_case|test)$")
		(#match? @ext "^test$")`,
	}
}
