// SPDX-License-Identifier: Apache-2.0

package treesitter

func svelteQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(element) @decl
			(script_element) @decl
			(style_element) @decl
		]`,
		Calls:    "",
		Imports:  "",
		TypeRefs: "",
	}
}
