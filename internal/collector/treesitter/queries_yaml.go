// SPDX-License-Identifier: Apache-2.0

package treesitter

func yamlQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `(block_mapping_pair) @decl`,
		Calls:    "",
		Imports:  "",
		TypeRefs: "",
	}
}
