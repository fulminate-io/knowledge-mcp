// SPDX-License-Identifier: Apache-2.0

package treesitter

func groovyQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(class_definition) @decl
			(function_definition) @decl
		]`,
		Calls:    `(juxt_function_call) @callee`,
		Imports:  `(groovy_import) @import`,
		TypeRefs: "",
	}
}
