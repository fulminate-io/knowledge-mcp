// SPDX-License-Identifier: Apache-2.0

package treesitter

func tsQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_declaration name: (identifier) @name) @decl
			(class_declaration name: (type_identifier) @name) @decl
			(interface_declaration name: (type_identifier) @name) @decl
			(type_alias_declaration name: (type_identifier) @name) @decl
			(export_statement declaration: [
				(function_declaration name: (identifier) @name)
				(class_declaration name: (type_identifier) @name)
				(interface_declaration name: (type_identifier) @name)
				(type_alias_declaration name: (type_identifier) @name)
			]) @decl
			(lexical_declaration) @decl
		]`,
		Calls: `(call_expression function: [
			(identifier) @callee
			(member_expression) @callee
		])`,
		Imports:  `(import_statement source: (string) @path)`,
		TypeRefs: `(type_identifier) @typeref`,
		// TestBlocks: shared with JavaScript. See queries_javascript.go for
		// the three-pattern union shape and regex alternant enumeration.
		TestBlocks: jsTestBlocksQuery,
	}
}
