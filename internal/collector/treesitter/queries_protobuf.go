// SPDX-License-Identifier: Apache-2.0

package treesitter

func protobufQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(message) @decl
			(service) @decl
			(enum) @decl
		]`,
		Calls:    "",
		Imports:  `(import) @path`,
		TypeRefs: "",
	}
}
