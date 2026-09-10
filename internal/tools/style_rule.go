// SPDX-License-Identifier: Apache-2.0

// style_rule.go — the style-rule vocabulary shared by the two surfaces that
// touch it: the compact index read (style_rule_index.go) and the rule-list
// import (style_rule_import.go).
//
// WHY THEY SIT TOGETHER RATHER THAN ONE COPY PER SURFACE. The severity ladder
// and the scope predicate are the two rules a reviewer has to enumerate to know
// a style rule means the same thing on both surfaces, and a rule spelled once is
// a rule they can find. That is the same reasoning
// refusePracticeLanguageOnWrite's doc records for the practice write gate.
//
// THE SCOPE PREDICATE IS APPLIED CLIENT-SIDE AND THAT IS THE DESIGN, not a
// missing optimization. A style rule's repo and path scope are OPTIONAL, so an
// unscoped rule is language-wide and belongs in the index for every repo and
// every path: the selection is "scope absent OR scope matches". Metadata
// predicates cannot express it — engine.LowerMetaPredicates emits one predicate
// per key and the server chains every one of them CONJUNCTIVELY onto a single
// store query — so a predicate on the scope key would drop exactly the rules
// that apply everywhere, which is the majority of them. The natural instinct is
// to move this filter into the predicate map; do not.

package tools

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// styleRuleSeverity resolves a caller-supplied severity against the CHECK
// contract's ladder, which is foundation.Severity.
//
// AN UNMAPPABLE VALUE IS AN ERROR NAMING THE VALUE AND THE WHOLE LADDER, NEVER A
// DEFAULT. Defaulting would silently relabel a critical rule as info, and a
// style rule and its sister check have to agree on one scale or a shaped rule
// could not be mirrored into a check without a re-mapping step nobody owns. The
// practice corpus's older `high` / `medium` values are NOT on this ladder and
// are refused here for exactly that reason.
//
// IT IS A HAND-WRITTEN SWITCH BECAUSE THE PACKAGE THAT OWNS THE VOCABULARY
// EXPORTS NO PARSER: corpus.parseSeverity, corpus.admittedSeverities and
// corpus.severityVocabulary are all unexported, and corpus.ParseCheck — the only
// exported entry — takes a whole check node. corpusscan.checkSeverity faces the
// identical cross-package problem and resolves it the same way; this is that
// shape, naming the caller's own locator instead of a check id.
func styleRuleSeverity(where, value string) (foundation.Severity, error) {
	switch foundation.Severity(value) {
	case foundation.SeverityInfo, foundation.SeverityNotice,
		foundation.SeverityWarning, foundation.SeverityCritical:
		return foundation.Severity(value), nil
	default:
		return "", fmt.Errorf(
			"%s carries severity=%q, which is not on the severity ladder (%s, %s, %s, %s)",
			where, value,
			foundation.SeverityInfo, foundation.SeverityNotice,
			foundation.SeverityWarning, foundation.SeverityCritical)
	}
}

// styleRuleScope is a rule's OPTIONAL narrowing, as read off its metadata.
//
// ONE DECLARATION, IN kgtypes. It was a local struct while the practice index
// was the only reader; the corpus scan's per-check narrowing is the second, and
// the scanner cannot import this package (tools imports the scanner), so the
// declaration moved to the package that already owns the two key spellings. The
// local name stays so this file and its tests keep one spelling for one thing —
// the same split capStyleColumn and styleIndexRowBound already record.
type styleRuleScope = kgtypes.StyleScope

// styleScopeFromMetadata reads a rule's scope off its node metadata.
//
// A MALFORMED style_scope_paths VALUE IS AN ERROR, not an empty scope. An
// unparseable array would otherwise read as "this rule applies everywhere",
// which is the widest possible answer to a question the data could not answer.
func styleScopeFromMetadata(md map[string]string) (styleRuleScope, error) {
	return kgtypes.StyleScopeFromMetadata(md)
}

// styleScopePathsDecode reads the JSON-array encoding of style_scope_paths.
func styleScopePathsDecode(raw string) ([]string, error) {
	return kgtypes.StyleScopePathsDecode(raw)
}

// styleScopePathsEncode renders path prefixes as the JSON-array metadata value.
func styleScopePathsEncode(paths []string) (string, error) {
	return kgtypes.StyleScopePathsEncode(paths)
}

// styleScopeMatches reports whether a rule with this scope applies to the repo
// and paths a caller named.
//
// EACH LEG IS "ABSENT OR MATCHES", ON BOTH SIDES. An absent scope on the RULE
// means the rule applies everywhere; an absent selector from the CALLER means
// there is nothing to exclude by. Either way the leg passes, and a rule carrying
// no scope at all is in every index — which is the cell that fails first if this
// filter is ever moved into the metadata predicate map.
//
// THE PATH LEG MATCHES AT PATH-SEGMENT BOUNDARIES, through the SAME predicate
// the corpus walk applies to its own scope (parser.MatchesPathPrefixes): a rule
// scoped to `pkg` admits `pkg/x.go` and never the sibling `pkgextra/x.go`. A
// bare strings.HasPrefix here would give a style rule a different notion of
// "under this path" from the scan that enforces it.
func styleScopeMatches(sc styleRuleScope, repo string, paths []string) bool {
	if sc.RepoSet && repo != "" && sc.Repo != repo {
		return false
	}
	if !sc.PathsSet || len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		if parser.MatchesPathPrefixes(p, sc.Paths) {
			return true
		}
	}
	return false
}

// styleScopePathRefusal validates ONE scope path against the spelling the walk
// uses, returning the empty string when it is well formed.
//
// ONE IMPLEMENTATION, IN kgtypes, for the reason styleRuleScope records: the
// corpus scan refuses a malformed check scope by the SAME three rules, and two
// copies of "which spellings can never match" is two vocabularies drifting.
func styleScopePathRefusal(p string) string { return kgtypes.StyleScopePathRefusal(p) }

// The index render's column caps and its declared per-row bound.
//
// THE CAPS ARE A CHOICE AND THE BOUND IS THEIR CONSEQUENCE, which is the same
// split corpusscan's own vocabulary documents for its compact render. Both now
// live in kgtypes rather than here, because a SECOND compact render — the
// assembly reference line for a style rule — caps the same columns to the same
// width and declares its block bound in terms of this row bound. The two have to
// agree by construction rather than by two literals that match today, and the
// project renderer cannot import this package (tools imports the renderer, so
// the edge runs one way only). The local names stay so this file and its tests
// keep one spelling for one quantity.
const (
	// styleIndexColumnCap bounds EVERY variable-width column of the row — the
	// severity, the scope, the summary and the sister-check id — in BYTES, cut
	// on a rune boundary with a visible ellipsis. Bytes rather than runes is
	// what makes the cap a width bound: a 60-rune summary can be 240 bytes.
	// Only the row's own id is uncapped, because a truncated id does not
	// resolve and resolving it is what the row is for.
	styleIndexColumnCap = kgtypes.StyleIndexColumnCap
	// styleIndexEmptyBound is the ZERO-ROW render's own bound, and it exists
	// because the row bound cannot answer for that input class: a zero-row block
	// is not empty, it is the one-line empty state, so a sum of per-row bounds
	// gives it a budget of 0 and calls a legitimate render over budget. The two bounds are
	// different quantities — one is per row, one is the fixed body a render with
	// no rows emits — and collapsing them is what made the block-bound helper
	// answer FALSE for the first input class the render defines.
	//
	// It stays LOCAL where the other two moved: the empty state is this arm's
	// own wording and no other render emits that line.
	//
	// The arithmetic: styleIndexEmpty is 21 bytes, declared as 32. A test holds
	// the constant to the line, so growing the wording past the bound is a red
	// rather than a silently wider render.
	//
	// ITS ONE TERM IS FIXED, never budgeted: the zero-row body is a literal this
	// package spells, so the bound is a figure over a figure and there is no
	// input for it to assume a width for. styleIndexBlockBound is therefore
	// FIXED on its zero-row arm and a sum of MEASURED-and-CAPPED row bounds on
	// the other, which is every kind of term it carries.
	styleIndexEmptyBound = 32
)

// styleIndexEllipsis marks a capped column so a truncation is visible rather
// than silent.
const styleIndexEllipsis = kgtypes.StyleIndexEllipsis

// styleIndexRowBound is the per-line bound for one index row, as a FUNCTION of
// the id that row carries.
//
// IT TAKES AN ARGUMENT BECAUSE THE ROW'S ID IS UNCAPPED AND UNVALIDATED. A
// style rule's id is written verbatim from the imported rule list, so its width
// is corpus data; a fixed figure that budgeted a nominal id was false for every
// wider one. kgtypes.StyleIndexRowBound carries the full reasoning.
func styleIndexRowBound(id string) int { return kgtypes.StyleIndexRowBound(id) }

// capStyleColumn bounds one variable-width column, cut on a rune boundary.
//
// ONE IMPLEMENTATION, IN kgtypes. It was inlined here while this file held the
// only compact style-rule render; the assembly reference line is the second, and
// two copies of a cut-on-a-rune-boundary loop are two places for the boundary
// arithmetic to drift.
func capStyleColumn(s string) string { return kgtypes.CapStyleColumn(s) }

// styleScopeColumn renders a rule's scope for the index row.
//
// A RULE WITH NO SCOPE RENDERS `-`, NOT AN EMPTY COLUMN, so "applies everywhere"
// and "the column is missing" are different things a reader can tell apart. Each
// half names itself, because a bare value could not say whether it was a repo or
// a path.
func styleScopeColumn(sc styleRuleScope) string {
	var parts []string
	if sc.RepoSet {
		parts = append(parts, "repo="+sc.Repo)
	}
	if sc.PathsSet {
		parts = append(parts, "paths="+strings.Join(sc.Paths, ","))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}
