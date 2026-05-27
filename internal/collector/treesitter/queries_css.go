// SPDX-License-Identifier: Apache-2.0

package treesitter

func cssQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(rule_set) @decl
			(media_statement) @decl
		]`,
		Calls:    "",
		Imports:  "",
		TypeRefs: "",
	}
}
