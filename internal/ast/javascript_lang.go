// SPDX-License-Identifier: Apache-2.0

// javascript_lang.go — JavaScript LangConfig + init-time registration. JS
// shares most of its surface with TypeScript but the tree-sitter grammar
// is a distinct registry entry (LangJavaScript), so the two ship as
// separate LangConfigs. The wrapper shapes are identical (JS is a strict
// subset of TS for the constructs the engine wraps) but they are NOT
// dynamically aliased — registering one config under both languages
// would couple unrelated registries and obscure per-language tuning.
//
// Reserved prefix and identifier rule mirror tsLangConfig. See
// typescript_lang.go for the rationale shared across both.

package ast

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// jsLangConfig is the registered LangConfig for JavaScript.
var jsLangConfig = LangConfig{
	Lang:     treesitter.LangJavaScript,
	Reserved: "__META_AST_",
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "function __metaWrapper__() {\n", Suffix: "\n}\n"},
		{Name: "expr", Prefix: "const __metaValue__ = ", Suffix: ";\n"},
	},
	IdentRule: isJSIdent,
}

func init() {
	registerLangConfig(jsLangConfig)
}
