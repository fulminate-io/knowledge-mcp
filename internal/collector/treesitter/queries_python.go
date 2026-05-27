// SPDX-License-Identifier: Apache-2.0

package treesitter

func pythonQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_definition name: (identifier) @name) @decl
			(class_definition name: (identifier) @name) @decl
			(decorated_definition) @decl
		]`,
		Calls: `(call function: [
			(identifier) @callee
			(attribute attribute: (identifier) @callee)
		])`,
		Imports: `[
			(import_statement)
			(import_from_statement module_name: (dotted_name) @path)
		] @import`,
		TypeRefs: `(type (identifier) @typeref)`,
	}
}
