// SPDX-License-Identifier: Apache-2.0

package treesitter

func javaQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(class_declaration name: (identifier) @name) @decl
			(interface_declaration name: (identifier) @name) @decl
			(method_declaration name: (identifier) @name) @decl
			(constructor_declaration name: (identifier) @name) @decl
			(enum_declaration name: (identifier) @name) @decl
		]`,
		Calls:    `(method_invocation name: (identifier) @callee)`,
		Imports:  `(import_declaration) @import`,
		TypeRefs: `(type_identifier) @typeref`,
	}
}
