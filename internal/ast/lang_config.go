// SPDX-License-Identifier: Apache-2.0

// lang_config.go — per-language configuration for the v2 engine.
//
// Each language registers a LangConfig describing its tree-sitter grammar,
// the reserved identifier prefix used for placeholder substitution, the
// ordered context wrappers compilePattern tries when re-parsing a fragment,
// and the identifier-validation rule the substituted reserved-prefix names
// must satisfy. Registration mirrors the init-time registry pattern used by
// domains/topology/registry.go.
//
// This file declares the LangConfig + ContextWrapper structs and the
// register / lookup helpers (registerLangConfig, langConfigFor). Per-
// language LangConfig values land in B.6 (Go) and Phase C/D (Python /
// TypeScript / JavaScript / Rust / long-tail).

package ast

import (
	"fmt"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// ContextWrapper is one parse-context candidate used by compilePattern when
// the substituted DSL source can't parse standalone. compilePattern tries
// each wrapper in order; the first one that produces a tree without ERROR
// nodes wins.
//
// Prefix is concatenated before the substituted source; Suffix after. Name
// is informational (used in error messages and debug output).
//
// Example (Go statement wrapper): {Name: "stmt", Prefix: "package _\nfunc _() { ", Suffix: " }"}.
type ContextWrapper struct {
	Name   string
	Prefix string
	Suffix string
}

// LangConfig is the per-language pattern-engine configuration.
//
//   - Lang names the tree-sitter grammar (treesitter.Language).
//   - Reserved is the identifier prefix compilePattern uses when
//     substituting placeholders. The substituted identifier MUST satisfy
//     IdentRule. Pick a prefix unlikely to collide with user code (e.g.
//     "__META_AST_").
//   - Wrappers is the ordered list of parse-context candidates.
//   - IdentRule reports whether a candidate substituted name is a valid
//     identifier in this language. The default rule (asciiGoIdent) accepts
//     ASCII-letter / digit / underscore identifiers; languages with stricter
//     identifier grammars should supply their own predicate.
type LangConfig struct {
	Lang      treesitter.Language
	Reserved  string
	Wrappers  []ContextWrapper
	IdentRule func(string) bool
}

// langRegistry holds the per-language config registered at init time. The
// mutex covers registerLangConfig writes; reads via langConfigFor are
// concurrent-safe under the standard "register once at init, read forever"
// discipline mirrored from domains/topology/registry.go.
var (
	langRegistryMu sync.RWMutex
	langRegistry   = map[treesitter.Language]LangConfig{}
)

// registerLangConfig stores cfg in the registry. Subsequent registrations
// for the same Lang overwrite the previous entry (so tests can reset the
// table between cases). Panics if cfg.Lang is the zero value or cfg.Reserved
// is empty — both are programmer errors that should fail fast at init time.
func registerLangConfig(cfg LangConfig) {
	if cfg.Lang == "" {
		panic("ast/lang_config: registerLangConfig with empty Lang")
	}
	if cfg.Reserved == "" {
		panic("ast/lang_config: registerLangConfig with empty Reserved prefix for " + string(cfg.Lang))
	}
	if cfg.IdentRule == nil {
		cfg.IdentRule = asciiGoIdent
	}
	langRegistryMu.Lock()
	defer langRegistryMu.Unlock()
	langRegistry[cfg.Lang] = cfg
}

// langConfigFor returns the registered LangConfig for the given language.
// Returns ok=false when no config has been registered (e.g., Phase D's
// denied long-tail markup languages).
func langConfigFor(lang treesitter.Language) (LangConfig, bool) {
	langRegistryMu.RLock()
	defer langRegistryMu.RUnlock()
	cfg, ok := langRegistry[lang]
	return cfg, ok
}

// errLanguageNotSupported is returned when Match is called with a language
// for which no LangConfig has been registered. The message is pinned by
// criterion 591ef8906b (Phase D long-tail deny test).
func errLanguageNotSupported(lang treesitter.Language) error {
	if _, denied := deniedLanguages[lang]; denied {
		return fmt.Errorf("ast/match: pattern matching not supported for language %s (config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk)", lang)
	}
	return fmt.Errorf("ast/match: pattern matching not supported for language %s", lang)
}

// deniedLanguages is the explicit deny set (Phase D / Q8) — config and markup
// languages whose tree-sitter grammars lack the structural depth required for
// the parse-substitute-walk loop. Compile() checks this set before registry
// lookup; the error message names the language and explains the rationale.
//
// Rationale: yaml/toml/css/html/sql/dockerfile/cue/svelte/markdown/protobuf/hcl
// all parse, but their AST shapes either bottom out in scalar leaves (yaml/toml/
// markdown/dockerfile) or in non-substitutable token soup (css/html/sql/cue/
// protobuf/hcl/svelte). A placeholder substitution engine has nothing meaningful
// to bind in those grammars; surfacing a hard "not supported" error is more
// honest than registering a default LangConfig that silently never matches.
var deniedLanguages = map[treesitter.Language]struct{}{
	treesitter.LangYaml:       {},
	treesitter.LangToml:       {},
	treesitter.LangCSS:        {},
	treesitter.LangHTML:       {},
	treesitter.LangSQL:        {},
	treesitter.LangDockerfile: {},
	treesitter.LangCue:        {},
	treesitter.LangSvelte:     {},
	treesitter.LangMarkdown:   {},
	treesitter.LangProtobuf:   {},
	treesitter.LangHCL:        {},
}

// isDeniedLanguage reports whether lang is in the explicit deny set.
func isDeniedLanguage(lang treesitter.Language) bool {
	_, ok := deniedLanguages[lang]
	return ok
}

// asciiGoIdent is the default IdentRule. Accepts ASCII Go-style identifiers:
// letter or underscore start, then letters / digits / underscores.
func asciiGoIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if i == 0 {
			if !isLetter && c != '_' {
				return false
			}
			continue
		}
		if !isLetter && !isDigit && c != '_' {
			return false
		}
	}
	return true
}
