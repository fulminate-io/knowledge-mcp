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
			(function_signature_item name: (identifier) @name) @decl
		]`,
		// The scoped_identifier arm is what makes `foo::bar(2)` emit a callee at
		// all — it produced no CALLS edge in any earlier state of this file.
		// A path form the index cannot represent, `<Foo as Bar>::baz`, is
		// captured VERBATIM and lands external rather than being normalised
		// into something that looks bound.
		Calls: `(call_expression function: [
			(identifier) @callee
			(field_expression) @callee
			(scoped_identifier) @callee
		])`,
		// The use_as_clause child is named so this query and the arm agree on
		// where `use x::y as z` puts its alias. One capture per statement: the
		// arm walks the declaration's own shape for the list, wildcard and
		// nested-alias forms.
		Imports:  `(use_declaration (use_as_clause)?) @import`,
		TypeRefs: `(type_identifier) @typeref`,
	}
}
