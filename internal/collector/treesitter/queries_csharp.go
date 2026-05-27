// SPDX-License-Identifier: Apache-2.0

package treesitter

func csharpQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(class_declaration name: (identifier) @name) @decl
			(interface_declaration name: (identifier) @name) @decl
			(struct_declaration name: (identifier) @name) @decl
			(namespace_declaration name: [(identifier) (qualified_name)] @name) @decl
			(method_declaration name: (identifier) @name) @decl
			(enum_declaration name: (identifier) @name) @decl
		]`,
		Calls: `(invocation_expression function: [
			(identifier) @callee
			(member_access_expression name: (identifier) @callee)
		])`,
		Imports:  `(using_directive) @import`,
		TypeRefs: `(identifier) @typeref`,
	}
}
