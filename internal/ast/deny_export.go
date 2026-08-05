// SPDX-License-Identifier: Apache-2.0

// deny_export.go — exported access to the deny-set predicate.
//
// IsDeniedLanguage is a one-line exported wrapper over the unexported
// isDeniedLanguage in lang_config.go. It lives in its own file, rather than
// renaming the predicate in place, so the deny-list surfacing work in the
// tools layer can consume the predicate without editing lang_config.go — the
// deny set and its rationale stay owned by that one file.

package ast

import "github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"

// IsDeniedLanguage reports whether lang is on the ast deny list — a
// config/markup language whose tree-sitter grammar lacks the structural depth
// for the parse-substitute-walk loop, so match/replace refuse it. The tools
// layer uses this to annotate the informational-only ops (list_node_kinds,
// explain) that still answer for a denied language.
func IsDeniedLanguage(lang treesitter.Language) bool {
	return isDeniedLanguage(lang)
}
