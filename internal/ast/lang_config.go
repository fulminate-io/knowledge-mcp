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
	"sort"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// ContextWrapper is one parse-context candidate used by compilePattern when
// the substituted DSL source can't parse standalone. compilePattern tries
// EVERY wrapper and keeps each one that both parses without ERROR nodes and
// HOSTS the fragment; the distinct survivors are compiled as a union and all
// of them are matched. Registration order therefore decides candidate order —
// and so which stamp survives a dedupe — but never which single wrapper is
// used, because there is no single wrapper.
//
// Prefix is concatenated before the substituted source; Suffix after. Name
// is informational (used in error messages and debug output).
//
// Context is the CALLER-FACING classification of the construct this wrapper
// makes expressible — exactly one of contextDecl / contextStmt /
// contextExpr / contextMember. It is distinct from Name on purpose: Name is
// the internal wrapper identity that appears in compile-failure messages and
// is historically inaccurate in places (Java's and C#'s wrapper named "decl"
// is a class body, i.e. the member context), while Context is the vocabulary
// callers see and pin against. registerLangConfig rejects a wrapper whose
// Context is empty or outside the four constants.
//
// Example (Go statement wrapper): {Name: "stmt", Context: contextStmt, Prefix: "package _\nfunc _() { ", Suffix: " }"}.
type ContextWrapper struct {
	Name    string
	Context string
	Prefix  string
	Suffix  string
}

// The context vocabulary. Every registered wrapper carries exactly one of
// these, and they are the only values a caller may pin. Declared as
// constants rather than spelled as literals at the registration sites so the
// registration vocabulary and the caller-facing one cannot drift apart.
//
//   - contextDecl   — top-level declarations (functions, types, imports).
//   - contextStmt   — statements inside a function or method body.
//   - contextExpr   — bare expressions with no terminator.
//   - contextMember — members inside a class / struct / interface body.
const (
	contextDecl   = "decl"
	contextStmt   = "stmt"
	contextExpr   = "expr"
	contextMember = "member"
)

// isValidContext reports whether s is one of the four context constants.
func isValidContext(s string) bool {
	switch s {
	case contextDecl, contextStmt, contextExpr, contextMember:
		return true
	default:
		return false
	}
}

// ValidContexts returns the four caller-facing context values, in the order
// they are documented. It is the tool layer's single source for validating a
// `context` pin and for naming the alternatives in the rejection message, so
// the vocabulary a caller is offered cannot drift from the one registration
// enforces.
func ValidContexts() []string {
	return []string{contextDecl, contextStmt, contextExpr, contextMember}
}

// RegisteredContexts returns the distinct contexts lang actually registers a
// wrapper for, in wrapper order, and whether lang has a config at all. Read
// from the live registry rather than a table: a language that gains or loses a
// wrapper changes what a caller can pin, and a hardcoded answer would keep
// offering a context nothing compiles under.
func RegisteredContexts(lang treesitter.Language) ([]string, bool) {
	cfg, ok := langConfigFor(lang)
	if !ok {
		return nil, false
	}
	var (
		out  []string
		seen = map[string]struct{}{}
	)
	for _, w := range cfg.Wrappers {
		if _, dup := seen[w.Context]; dup {
			continue
		}
		seen[w.Context] = struct{}{}
		out = append(out, w.Context)
	}
	return out, true
}

// HasTestFilePredicate reports whether lang carries a test-file convention the
// walk can act on. Read from the live registry for the same reason
// RegisteredContexts is: a language that gains a predicate must start accepting
// include_tests in the same commit, without a second table to update.
//
// False for an unregistered or denied language too — neither has a convention,
// and both reach a hard error later on the same call.
func HasTestFilePredicate(lang treesitter.Language) bool {
	cfg, ok := langConfigFor(lang)
	return ok && cfg.IsTestFile != nil
}

// TestFilePredicateLanguages names every registered language that carries a
// test-file convention, sorted. It exists so the refusal message for a language
// without one can say which languages DO support the flag, rather than leaving
// the caller to probe them one at a time.
func TestFilePredicateLanguages() []string {
	langRegistryMu.RLock()
	out := make([]string, 0, len(langRegistry))
	for lang, cfg := range langRegistry {
		if cfg.IsTestFile != nil {
			out = append(out, string(lang))
		}
	}
	langRegistryMu.RUnlock()
	sort.Strings(out)
	return out
}

// hasPathSegment reports whether rel contains seg as a COMPLETE slash-separated
// path segment — "test" matches test/x.rb and a/test/x.rb, never a/testdata/x.rb
// or a/mytest/x.rb. Paths reaching the predicates are repo-relative and
// slash-separated, the same shape RawMatch.FilePath carries.
func hasPathSegment(rel, seg string) bool {
	for rest := rel; rest != ""; {
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			return false // the last element is the file name, never a directory
		}
		if rest[:i] == seg {
			return true
		}
		rest = rest[i+1:]
	}
	return false
}

// pathBase is the file-name element of a slash-separated repo-relative path.
func pathBase(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	return rel
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
//   - IsTestFile reports whether a repo-relative path is TEST source under this
//     language's own convention. It is what the walk's include_tests filter
//     consults, so the filter means the same thing for every language instead of
//     meaning Go. NIL is a first-class, DOCUMENTED disposition: it says this
//     language has no unambiguous FILENAME convention (Rust marks tests with an
//     in-file `mod tests`; C has none at all), and the tool layer refuses an
//     explicit include_tests for such a language rather than accepting a flag it
//     would silently ignore. Every registered language's disposition — predicate
//     or documented nil — is asserted against the live registry by
//     TestLangConfig_EveryLanguageHasTestFileDisposition, so a newly registered
//     language with no decision fails rather than defaulting.
//   - LayoutTokens holds the exact source texts of this grammar's anonymous
//     PURE-LAYOUT tokens: tokens the parse surfaces as children but which
//     carry no meaning a caller could have intended, because the same
//     construct written differently parses to the same tree without them. The
//     matcher's child alignment skips them on BOTH the pattern and the target
//     side, so a one-line pattern and a multi-line body reach the same list.
//     The zero value — no layout tokens — is correct for every grammar the
//     census in testdata/layout_token_census.txt marks layout=no, which today
//     is all of them but Go. A token that is whitespace-like but MEANINGFUL
//     does NOT belong here: C's `;` and Python's offside newline are meaning,
//     and classifying either as layout would make patterns stop discriminating.
//   - CommentKinds holds the exact NODE KINDS this grammar emits for a comment
//     — `comment` for most, but split forms like Rust's line_comment /
//     block_comment and Kotlin's multiline_comment where the grammar draws the
//     line differently. These are MEASURED by testdata/comment_kind_census.txt
//     and never hand-guessed; a run that disagrees with the census fails. The
//     matcher's ORDINARY child alignment skips them on BOTH the pattern and the
//     target side so a comment-carrying body still aligns against a
//     comment-free pattern, while SEQUENCE ($$$) placeholders still consume them
//     VERBATIM — that split is what keeps a $$$ body capture re-interpolating as
//     valid source rather than silently dropping the comments it spanned. A kind
//     that is IsExtra but MEANINGFUL — Ruby's heredoc_body, C#'s preproc regions,
//     OCaml's attribute — does NOT belong here: skipping it would corrupt a
//     match, and the exhaustiveness of the comment/meaningful split is enforced
//     by TestExtraKindPartition_Corpus, whose meaningful-extras table is
//     test-resident because no matcher path reads it. Every registered grammar
//     emits at least one comment kind, so unlike IsTestFile there is no
//     documented-nil disposition — the list is non-empty for all 21.
type LangConfig struct {
	Lang         treesitter.Language
	Reserved     string
	Wrappers     []ContextWrapper
	IdentRule    func(string) bool
	LayoutTokens []string
	CommentKinds []string
	IsTestFile   func(string) bool
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
// table between cases). Panics if cfg.Lang is the zero value, cfg.Reserved
// is empty, or any wrapper carries a Context outside the four constants —
// all programmer errors that should fail fast at init time rather than
// reaching a caller as a wrapper that cannot be named or pinned.
func registerLangConfig(cfg LangConfig) {
	if cfg.Lang == "" {
		panic("ast/lang_config: registerLangConfig with empty Lang")
	}
	if cfg.Reserved == "" {
		panic("ast/lang_config: registerLangConfig with empty Reserved prefix for " + string(cfg.Lang))
	}
	for _, w := range cfg.Wrappers {
		if !isValidContext(w.Context) {
			panic(fmt.Sprintf(
				"ast/lang_config: registerLangConfig for %s: wrapper %q has Context %q, want one of %q/%q/%q/%q",
				cfg.Lang, w.Name, w.Context, contextDecl, contextStmt, contextExpr, contextMember))
		}
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
// the long-tail deny test.
func errLanguageNotSupported(lang treesitter.Language) error {
	if reason, denied := deniedLanguages[lang]; denied {
		return fmt.Errorf("ast/match: pattern matching not supported for language %s (%s)", lang, reason)
	}
	return fmt.Errorf("ast/match: pattern matching not supported for language %s", lang)
}

// deniedLanguages is the explicit deny set (Phase D / Q8). Compile() checks
// this set before registry lookup; the error message names the language and
// its stored per-language rationale (the map value).
//
// Most entries are config/markup languages whose tree-sitter grammars lack the
// structural depth required for the parse-substitute-walk loop: yaml/toml/css/
// html/sql/dockerfile/cue/svelte/markdown/protobuf/hcl all parse, but their AST
// shapes either bottom out in scalar leaves (yaml/toml/markdown/dockerfile) or
// in non-substitutable token soup (css/html/sql/cue/protobuf/hcl/svelte). A
// placeholder substitution engine has nothing meaningful to bind in those
// grammars; a hard "not supported" error is more honest than registering a
// default LangConfig that silently never matches.
//
// PHP is denied for a DIFFERENT reason — not grammar shallowness but a SIGIL
// COLLISION: a PHP variable is written with the same `$` the pattern DSL
// reserves for placeholders, so no pattern can name a specific variable and
// `$_` / `$$$` do not compile. Its stored reason names the collision.
var deniedLanguages = map[treesitter.Language]string{
	treesitter.LangYaml:       "config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk",
	treesitter.LangToml:       "config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk",
	treesitter.LangCSS:        "config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk",
	treesitter.LangHTML:       "config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk",
	treesitter.LangSQL:        "config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk",
	treesitter.LangDockerfile: "config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk",
	treesitter.LangCue:        "config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk",
	treesitter.LangSvelte:     "config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk",
	treesitter.LangMarkdown:   "config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk",
	treesitter.LangProtobuf:   "config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk",
	treesitter.LangHCL:        "config/markup language; tree-sitter grammar lacks the structural depth for parse-substitute-walk",
	treesitter.LangPHP:        "a PHP variable is written with the same $ sigil the pattern DSL reserves for placeholders, so no pattern can name a specific variable and $_ / $$$ do not compile; un-denying requires an alternate placeholder sigil (future work)",
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
