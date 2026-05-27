// SPDX-License-Identifier: Apache-2.0

package treesitter

func hclQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `(block) @decl`,
		Calls:    "",
		Imports:  "",
		TypeRefs: "",
	}
}
