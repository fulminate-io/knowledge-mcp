// SPDX-License-Identifier: Apache-2.0

package treesitter

func goQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_declaration name: (identifier) @name) @decl
			(method_declaration
				receiver: (parameter_list) @receiver
				name: (field_identifier) @name) @decl
			(type_declaration (type_spec name: (type_identifier) @name)) @decl
		]`,
		Calls: `(call_expression function: [
			(identifier) @callee
			(selector_expression) @callee
		])`,
		Imports:  `(import_spec path: (interpreted_string_literal) @path)`,
		TypeRefs: `(type_identifier) @typeref`,
	}
}
