// SPDX-License-Identifier: Apache-2.0

package treesitter

func sqlQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `(statement) @decl`,
		Calls:    "",
		Imports:  "",
		TypeRefs: "",
	}
}
