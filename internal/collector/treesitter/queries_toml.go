// SPDX-License-Identifier: Apache-2.0

package treesitter

func tomlQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `(table) @decl`,
		Calls:    "",
		Imports:  "",
		TypeRefs: "",
	}
}
