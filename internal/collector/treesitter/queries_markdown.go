// SPDX-License-Identifier: Apache-2.0

package treesitter

func markdownQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(section) @decl
			(atx_heading) @decl
		]`,
		Calls:    "",
		Imports:  "",
		TypeRefs: "",
	}
}
