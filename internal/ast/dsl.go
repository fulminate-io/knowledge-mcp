// SPDX-License-Identifier: Apache-2.0

// Package ast provides structural code search over tree-sitter ASTs via a
// placeholder-template DSL and a JSON boolean where-tree, working uniformly
// across every language registered in the codegraph treesitter package.
//
// dsl.go — v2 placeholder-template parser. Parse converts a user-facing
// pattern string into a Pattern carrying the raw source plus a slice of
// Placeholder records describing every `$X` / `$$$X` / `$_` / `$$$_`
// occurrence and its byte range within the source.
//
// Lexer rules (the locked DSL surface):
//
//   - `$_`             — single-node wildcard, no capture name.
//   - `$X`             — single-node placeholder, capture name X.
//   - `$$$_`           — sequence wildcard, no capture name.
//   - `$$$X`           — sequence placeholder, capture name X.
//   - bare `$`         — error (errParserBareDollar).
//   - `$$<not $>`      — error (errParserDoubleDollar).
//   - `$$$<not ident>` — error (errParserTripleDollar).
//   - `$$$$`           — error (`$$$` followed by `$`, errParserTripleDollar).
//   - tree_sitter_query: prefix — error (errParserTreeSitterPrefix); the
//     escape hatch is deleted in v2.
//
// Identifier rule: ASCII letters, digits, underscore; must not start with a
// digit. Mirrors the Go-style identifier rule the deleted dsl_lex.go
// readPlaceholderTail used.

package ast

import (
	"errors"
	"fmt"
	"strings"
)

// PlaceholderKind discriminates the four placeholder shapes the v2 walker
// understands.
type PlaceholderKind int

const (
	// KindNode binds a single named tree-sitter node ($X).
	KindNode PlaceholderKind = iota
	// KindNodeWild matches a single tree-sitter node without binding ($_).
	KindNodeWild
	// KindSeq binds zero or more sibling nodes as a sequence ($$$X).
	KindSeq
	// KindSeqWild matches zero or more sibling nodes without binding ($$$_).
	KindSeqWild
)

// String renders the placeholder kind for diagnostics.
func (k PlaceholderKind) String() string {
	switch k {
	case KindNode:
		return "node"
	case KindNodeWild:
		return "node_wild"
	case KindSeq:
		return "seq"
	case KindSeqWild:
		return "seq_wild"
	default:
		return "unknown"
	}
}

// Placeholder is one occurrence of `$X` / `$$$X` / `$_` / `$$$_` in a parsed
// DSL source string.
//
//   - Name is the capture name (e.g. "X"). Empty for wildcards.
//   - Kind discriminates single-node vs sequence and named vs wildcard.
//   - OffsetStart / OffsetEnd are the byte offsets of the placeholder token
//     within Pattern.Source (OffsetEnd is exclusive). The engine uses the
//     offsets in B.2 to splice in reserved-prefix identifiers when handing
//     the substituted string to tree-sitter.
type Placeholder struct {
	Name        string
	Kind        PlaceholderKind
	OffsetStart int
	OffsetEnd   int
}

// Pattern is the parsed form of a DSL source string.
//
//   - Source is the verbatim user-supplied DSL.
//   - Placeholders is the ordered list of placeholder occurrences in
//     Source. Empty for fully literal patterns.
type Pattern struct {
	Source       string
	Placeholders []Placeholder
}

// rawPrefix is the deleted Phase-A escape hatch. Parse rejects this prefix
// outright — the user-facing escape hatch is gone in v2.
const rawPrefix = "tree_sitter_query:"

var (
	// errParseEmpty is returned for empty / whitespace-only input.
	errParseEmpty = errors.New("ast/dsl: empty pattern source")

	// errParserBareDollar is returned for a stray `$` not followed by an
	// identifier or `_`.
	errParserBareDollar = errors.New("ast/dsl: bare $ — expected $X / $_ / $$$X / $$$_")

	// errParserDoubleDollar is returned for `$$<not $>` — `$$` is not a
	// valid placeholder prefix.
	errParserDoubleDollar = errors.New("ast/dsl: bare $$ — expected $$$X / $$$_ for sequence captures")

	// errParserTripleDollar is returned for `$$$<not identifier and not _>`
	// (covers `$$$$` since the trailing `$` is neither letter nor `_`).
	errParserTripleDollar = errors.New("ast/dsl: bare $$$ — expected $$$X (named) or $$$_ (wildcard)")

	// errParserTreeSitterPrefix is returned for the deleted Phase-A escape
	// hatch.
	errParserTreeSitterPrefix = errors.New("ast/dsl: tree_sitter_query: prefix is not supported in v2 — author the pattern with $X / $$$X placeholders")
)

// Parse parses a DSL source string into a Pattern. Walks source byte-by-
// byte; every non-`$` byte is literal text, every `$` enters the
// placeholder lexer.
//
// The signature is stable — cmd/knowledge/internal/tools/ast.go's
// buildAstPattern calls Parse(a.Pattern) with the pattern string.
func Parse(source string) (Pattern, error) {
	if strings.TrimSpace(source) == "" {
		return Pattern{}, errParseEmpty
	}
	if strings.HasPrefix(strings.TrimSpace(source), rawPrefix) {
		return Pattern{}, errParserTreeSitterPrefix
	}

	var phs []Placeholder
	i := 0
	for i < len(source) {
		if source[i] != '$' {
			i++
			continue
		}
		ph, end, err := lexPlaceholder(source, i)
		if err != nil {
			return Pattern{}, err
		}
		phs = append(phs, ph)
		i = end
	}
	return Pattern{Source: source, Placeholders: phs}, nil
}

// lexPlaceholder consumes one placeholder starting at source[start] (which
// MUST be '$') and returns the parsed Placeholder plus the byte offset
// immediately after the placeholder. Errors mirror the four cases in the
// package doc comment.
//
//nolint:gocognit // form-by-form lexer; complexity proportional to grammar.
func lexPlaceholder(source string, start int) (Placeholder, int, error) {
	// Already verified source[start] == '$'.
	n := len(source)

	// Count consecutive '$' starting at start. Cap at 4 so `$$$$` produces
	// the correct triple-dollar error rather than over-counting.
	dollars := 1
	for i := start + 1; i < n && source[i] == '$' && dollars < 4; i++ {
		dollars++
	}

	tail := start + dollars
	switch dollars {
	case 1:
		// `$X` or `$_`.
		if tail >= n {
			return Placeholder{}, 0, errParserBareDollar
		}
		c := source[tail]
		if c == '_' {
			return Placeholder{
				Name:        "",
				Kind:        KindNodeWild,
				OffsetStart: start,
				OffsetEnd:   tail + 1,
			}, tail + 1, nil
		}
		if !isIdentStart(c) {
			return Placeholder{}, 0, errParserBareDollar
		}
		name, end := readIdent(source, tail)
		return Placeholder{
			Name:        name,
			Kind:        KindNode,
			OffsetStart: start,
			OffsetEnd:   end,
		}, end, nil

	case 2:
		// `$$<not $>` is invalid. (Three or four dollars are handled in
		// case 3 / case 4 below.)
		return Placeholder{}, 0, errParserDoubleDollar

	case 3:
		// `$$$X` or `$$$_`.
		if tail >= n {
			return Placeholder{}, 0, errParserTripleDollar
		}
		c := source[tail]
		if c == '_' {
			return Placeholder{
				Name:        "",
				Kind:        KindSeqWild,
				OffsetStart: start,
				OffsetEnd:   tail + 1,
			}, tail + 1, nil
		}
		if !isIdentStart(c) {
			return Placeholder{}, 0, errParserTripleDollar
		}
		name, end := readIdent(source, tail)
		return Placeholder{
			Name:        name,
			Kind:        KindSeq,
			OffsetStart: start,
			OffsetEnd:   end,
		}, end, nil

	default:
		// dollars == 4 — `$$$$` is `$$$` followed by `$`, which is not a
		// valid placeholder start. Triple-dollar error covers it per the
		// step description.
		return Placeholder{}, 0, fmt.Errorf("%w (got $$$$)", errParserTripleDollar)
	}
}

// isIdentStart reports whether c can begin an ASCII Go-style identifier
// (letter or underscore). Mirrors the deleted dsl_lex.go shape.
func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

// isIdentCont reports whether c can continue an ASCII Go-style identifier
// (letter, digit, or underscore).
func isIdentCont(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// readIdent reads a Go-style identifier starting at source[start]. The
// caller has verified source[start] is a valid ident-start byte. Returns
// the identifier text and the byte offset immediately after it.
func readIdent(source string, start int) (string, int) {
	end := start + 1
	for end < len(source) && isIdentCont(source[end]) {
		end++
	}
	return source[start:end], end
}
