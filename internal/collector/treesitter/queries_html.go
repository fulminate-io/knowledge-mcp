// SPDX-License-Identifier: Apache-2.0

package treesitter

func htmlQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
    (script_element) @decl
    (style_element) @decl
]`,
		Calls:    "",
		Imports:  "",
		TypeRefs: "",
	}
}
