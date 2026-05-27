// SPDX-License-Identifier: Apache-2.0

package treesitter

func rustQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_item name: (identifier) @name) @decl
			(struct_item name: (type_identifier) @name) @decl
			(enum_item name: (type_identifier) @name) @decl
			(impl_item type: (type_identifier) @name) @decl
			(trait_item name: (type_identifier) @name) @decl
			(mod_item name: (identifier) @name) @decl
			(type_item name: (type_identifier) @name) @decl
		]`,
		Calls: `(call_expression function: [
			(identifier) @callee
			(field_expression field: (field_identifier) @callee)
		])`,
		Imports:  `(use_declaration) @import`,
		TypeRefs: `(type_identifier) @typeref`,
	}
}
