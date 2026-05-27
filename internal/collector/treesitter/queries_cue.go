// SPDX-License-Identifier: Apache-2.0

package treesitter

func cueQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `(field) @decl`,
		Calls:    "",
		Imports:  "",
		TypeRefs: "",
	}
}
