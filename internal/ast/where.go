// SPDX-License-Identifier: Apache-2.0

// where.go — JSON where-tree evaluator for v2 patterns.
//
// The v2 DSL replaces the line-based where-clause grammar with a recursive
// JSON boolean tree composed of three composers (all/any/not) and six
// leaves (kind / matches / equals / same_node / inside_pattern /
// contains_pattern). The last two compose sub-patterns recursively, so the
// evaluator delegates back into compilePattern + matchTree when it
// encounters them.
//
// Key locked decisions:
//
//   - Sub-pattern recursion HARD CAPPED at 8 levels (Q10) — explicit
//     error on overflow.
//   - Cross-language sub-pattern DEFERRED in v1 (Q7) — error on
//     SubPatternLeaf.Language != scope.lang.
//   - `matches` regex operates on the verbatim sequence-capture text slice
//     as ONE string (Q6). Pre-compiled ONCE per evalWhere call and cached
//     on the WhereNode.
//   - `same_node`: single-node captures via *sitter.Node.Equal (T3-1);
//     sequence captures via outer-byte-span (Q5).
//   - Cross-scope `$outer.X` capture references walk the scope.parent
//     chain (Q11); unresolved references return an explicit error.
//   - Per-WORKER sub-pattern compile cache keyed by sub-pattern source
//     string. Each match worker owns its own cache (a worker never shares
//     a tree-sitter *Tree with another goroutine); the mutex guards the
//     map against the depth-first recursion within that single worker.

package ast

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// WhereNode is the JSON where-tree. Exactly one of {composer field, leaf
// field} should be populated per node; nodes with multiple set are
// evaluated as an implicit `all` over the populated fields.
type WhereNode struct {
	All []*WhereNode `json:"all,omitempty"`
	Any []*WhereNode `json:"any,omitempty"`
	Not *WhereNode   `json:"not,omitempty"`

	Kind            *KindLeaf       `json:"kind,omitempty"`
	Matches         *MatchesLeaf    `json:"matches,omitempty"`
	Equals          *EqualsLeaf     `json:"equals,omitempty"`
	SameNode        *SameNodeLeaf   `json:"same_node,omitempty"`
	SameText        *SameTextLeaf   `json:"same_text,omitempty"`
	InsidePattern   *SubPatternLeaf `json:"inside_pattern,omitempty"`
	ContainsPattern *SubPatternLeaf `json:"contains_pattern,omitempty"`
}

// KindLeaf checks the tree-sitter kind of a captured node. Either Is or Of
// can hold a single string or list. When both are present the capture's
// kind must equal at least one of them. Empty Is means "any kind".
type KindLeaf struct {
	Of string   `json:"of"`
	Is []string `json:"is"`
}

// UnmarshalJSON for KindLeaf accepts both `is: "kind"` and `is: ["k1", "k2"]`.
func (l *KindLeaf) UnmarshalJSON(data []byte) error {
	var raw struct {
		Of string          `json:"of"`
		Is jsonStringOrArr `json:"is"`
	}
	if err := jsonUnmarshal(data, &raw); err != nil {
		return err
	}
	l.Of = raw.Of
	l.Is = raw.Is.values
	return nil
}

// MatchesLeaf is the regex leaf. Regex is pre-compiled on first eval and
// cached in compiled. The compileOnce sync.Once guards the cache.
type MatchesLeaf struct {
	Of    string `json:"of"`
	Regex string `json:"regex"`

	compileOnce sync.Once
	compiled    *regexp.Regexp
	compileErr  error
}

// EqualsLeaf checks literal text equality.
type EqualsLeaf struct {
	Of    string `json:"of"`
	Value string `json:"value"`
}

// SameNodeLeaf checks two-or-more captures bind to the same AST node. Per
// Q11, entries can be `$outer.X` (one parent level), `$outer.outer.X`
// (two), etc.
type SameNodeLeaf struct {
	Captures []string `json:"captures"`
}

// SameTextLeaf checks two-or-more captures share the same source text.
// Used when the captures are different AST node occurrences but should
// refer to the same identifier (e.g., a deferred close on the same
// variable name as the receiver short_var_decl). same_node compares
// node identity (different occurrences = different nodes); same_text
// compares the captured `.Text` field which is the verbatim source slice.
//
// Cross-scope refs work the same as same_node: bare names look up in
// local scope, `$outer.X` chains walk parent scopes.
type SameTextLeaf struct {
	Captures []string `json:"captures"`
}

// SubPatternLeaf is shared by inside_pattern + contains_pattern. The
// capture's bound node is the search root: ancestors (inside_pattern) or
// descendants (contains_pattern).
//
// As, when non-empty, names the matched ancestor/descendant node so
// subsequent leaves in the SAME local scope can reference it (e.g.,
// `inside_pattern: {of: $match, pattern: "func ...", as: "FN"}` followed
// by `contains_pattern: {of: "FN", pattern: "..."}`). The binding is
// written to the calling scope on a successful match — meaningful for
// positive uses; in `not` arms the binding still fires on inner-match but
// the outer verdict flips, so referencing the `as` capture from a
// sibling-of-`not` leaf is a usage smell (the binding may not be set if
// the inner failed and `not` returned true).
type SubPatternLeaf struct {
	Of       string     `json:"of"`
	Pattern  string     `json:"pattern"`
	Language string     `json:"language,omitempty"`
	Where    *WhereNode `json:"where,omitempty"`
	As       string     `json:"as,omitempty"`
}

// evalScope is the per-evaluation scope chain. captures holds the local
// match's bindings; parent points at the surrounding evaluation (nil for
// the outermost). cache is the per-WORKER sub-pattern compile cache (owned
// by the one match worker that built this scope); it is shared across the
// scope chain within that worker.
type evalScope struct {
	captures *Captures
	parent   *evalScope
	depth    int
	cache    map[string]*PatternTree
	cacheMu  *sync.Mutex
	lang     treesitter.Language
	src      []byte
	// node is the bound target node for the local match — used by the
	// walker to look up node identity for same_node leaves.
	nodeByName map[string]*sitter.Node
}

// newOuterScope builds the outermost evalScope. cache + cacheMu are owned
// by a single match worker (one per worker goroutine); the worker passes
// them in and closes the cache at its exit.
func newOuterScope(lang treesitter.Language, cache map[string]*PatternTree, cacheMu *sync.Mutex) *evalScope {
	return &evalScope{
		captures:   newCaptures(),
		cache:      cache,
		cacheMu:    cacheMu,
		lang:       lang,
		nodeByName: map[string]*sitter.Node{},
	}
}

// withCaptures returns a child scope that binds captures + nodeByName from
// a fresh local match while inheriting cache + lang from the parent. depth
// stays 0 — the depth counter applies only to sub-pattern descents.
func (s *evalScope) withMatchCaptures(captures *Captures, nodeByName map[string]*sitter.Node, src []byte) *evalScope {
	return &evalScope{
		captures:   captures,
		parent:     s.parent,
		depth:      s.depth,
		cache:      s.cache,
		cacheMu:    s.cacheMu,
		lang:       s.lang,
		src:        src,
		nodeByName: nodeByName,
	}
}

// pushSubPattern returns a child scope for a sub-pattern descent. depth
// increments; the new scope's parent chain points at the calling scope so
// $outer.X bubbles up.
func (s *evalScope) pushSubPattern(captures *Captures, nodeByName map[string]*sitter.Node, src []byte) *evalScope {
	return &evalScope{
		captures:   captures,
		parent:     s,
		depth:      s.depth + 1,
		cache:      s.cache,
		cacheMu:    s.cacheMu,
		lang:       s.lang,
		src:        src,
		nodeByName: nodeByName,
	}
}

// errSubPatternDepth is returned when sub-pattern recursion exceeds 8
// levels. Q10 hard cap.
var errSubPatternDepth = errors.New("ast/where: sub-pattern recursion exceeded 8 levels (cycle or pathological nesting?)")

// errCaptureUnresolved is returned by resolveCapture when a `$outer.X`
// reference can't find a matching binding in the scope chain.
var errCaptureUnresolved = errors.New("ast/where: capture not found in scope chain")

// errCrossLanguageSubPattern is returned for cross-language sub-pattern
// usage (Q7 deferred).
var errCrossLanguageSubPattern = errors.New("ast/where: cross-language sub-pattern not supported in v1")

// subPatternMaxDepth is the hard cap on sub-pattern recursion (Q10).
const subPatternMaxDepth = 8

// evalWhere evaluates the where-tree against the scope. nil where node is
// always true (no filter). Errors propagate; callers MUST surface them
// rather than treat them as no-match.
func evalWhere(ctx context.Context, where *WhereNode, scope *evalScope) (bool, error) {
	if where == nil {
		return true, nil
	}
	// Composers have an implicit AND when multiple are set in the same
	// node. Most LLM-emitted JSON sets exactly one composer / leaf per
	// node; the implicit-AND behavior keeps weirder shapes tractable.
	if len(where.All) > 0 {
		for _, child := range where.All {
			ok, err := evalWhere(ctx, child, scope)
			if err != nil || !ok {
				return ok, err
			}
		}
	}
	if len(where.Any) > 0 {
		matched := false
		for _, child := range where.Any {
			ok, err := evalWhere(ctx, child, scope)
			if err != nil {
				return false, err
			}
			if ok {
				matched = true
				break
			}
		}
		if !matched {
			return false, nil
		}
	}
	if where.Not != nil {
		ok, err := evalWhere(ctx, where.Not, scope)
		if err != nil {
			return false, err
		}
		if ok {
			return false, nil
		}
	}
	return evalLeaves(ctx, where, scope)
}

// evalLeaves evaluates the six leaf operators on where, ANDing their
// verdicts. Empty leaves are no-ops.
func evalLeaves(ctx context.Context, where *WhereNode, scope *evalScope) (bool, error) {
	if where.Kind != nil {
		ok, err := evalKind(where.Kind, scope)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.Matches != nil {
		ok, err := evalMatches(where.Matches, scope)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.Equals != nil {
		ok, err := evalEquals(where.Equals, scope)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.SameNode != nil {
		ok, err := evalSameNode(where.SameNode, scope)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.SameText != nil {
		ok, err := evalSameText(where.SameText, scope)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.InsidePattern != nil {
		ok, err := evalSubPattern(ctx, where.InsidePattern, scope, ancestorSearch)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.ContainsPattern != nil {
		ok, err := evalSubPattern(ctx, where.ContainsPattern, scope, descendantSearch)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

// resolveCapture walks the scope chain per the ref's `$outer.` prefix
// count. ref="X" looks up in scope.captures; ref="$outer.X" walks one
// parent level; ref="$outer.outer.X" walks two; etc. Returns
// errCaptureUnresolved when the ref can't be resolved — either the
// chain is too short or the named capture doesn't exist at the resolved
// scope.
func resolveCapture(scope *evalScope, ref string) (Capture, *sitter.Node, error) {
	cur := scope
	name := ref
	for strings.HasPrefix(name, "$outer.") {
		name = name[len("$outer."):]
		if cur == nil {
			return Capture{}, nil, fmt.Errorf("%w: %q", errCaptureUnresolved, ref)
		}
		cur = cur.parent
	}
	if cur == nil || cur.captures == nil {
		return Capture{}, nil, fmt.Errorf("%w: %q", errCaptureUnresolved, ref)
	}
	cap, ok := cur.captures.byName[name]
	if !ok {
		return Capture{}, nil, fmt.Errorf("%w: %q", errCaptureUnresolved, ref)
	}
	node := cur.nodeByName[name]
	return cap, node, nil
}

// evalKind checks the bound capture's kind matches at least one of the
// `is` options. Empty `is` means "any kind".
func evalKind(l *KindLeaf, scope *evalScope) (bool, error) {
	cap, _, err := resolveCapture(scope, l.Of)
	if err != nil {
		return false, err
	}
	if len(l.Is) == 0 {
		return true, nil
	}
	if slices.Contains(l.Is, cap.Kind) {
		return true, nil
	}
	return false, nil
}

// evalMatches checks the capture's text against a regex. Per Q6 the regex
// runs on the verbatim text slice; for sequence captures that includes
// inter-sibling whitespace and comments. Pre-compiles ONCE.
func evalMatches(l *MatchesLeaf, scope *evalScope) (bool, error) {
	l.compileOnce.Do(func() {
		l.compiled, l.compileErr = regexp.Compile(l.Regex)
	})
	if l.compileErr != nil {
		return false, fmt.Errorf("ast/where: regex compile %q: %w", l.Regex, l.compileErr)
	}
	cap, _, err := resolveCapture(scope, l.Of)
	if err != nil {
		return false, err
	}
	return l.compiled.MatchString(cap.Text), nil
}

// evalEquals checks literal text equality.
func evalEquals(l *EqualsLeaf, scope *evalScope) (bool, error) {
	cap, _, err := resolveCapture(scope, l.Of)
	if err != nil {
		return false, err
	}
	return cap.Text == l.Value, nil
}

// evalSameNode checks two-or-more captures bind to the same AST node.
// Single-node captures: *sitter.Node.Equal (T3-1). Sequence captures:
// outer (start_byte, end_byte) span (Q5). Mixed (single + sequence) is
// false unless both happen to span the same byte range.
func evalSameNode(l *SameNodeLeaf, scope *evalScope) (bool, error) {
	if len(l.Captures) < 2 {
		return false, fmt.Errorf("ast/where: same_node requires at least 2 captures, got %d", len(l.Captures))
	}
	firstCap, firstNode, err := resolveCapture(scope, l.Captures[0])
	if err != nil {
		return false, err
	}
	for _, ref := range l.Captures[1:] {
		cap, node, err := resolveCapture(scope, ref)
		if err != nil {
			return false, err
		}
		if !sameNodeIdentity(firstCap, firstNode, cap, node) {
			return false, nil
		}
	}
	return true, nil
}

// evalSameText checks two-or-more captures share the same source text.
// Cross-scope refs (`$outer.X`) walk parent scopes the same way as
// same_node. Use when comparing variable-name occurrences across
// siblings — same_node fails because different identifier occurrences
// are different AST nodes; same_text compares the captured verbatim
// text slice.
func evalSameText(l *SameTextLeaf, scope *evalScope) (bool, error) {
	if len(l.Captures) < 2 {
		return false, fmt.Errorf("ast/where: same_text requires at least 2 captures, got %d", len(l.Captures))
	}
	first, _, err := resolveCapture(scope, l.Captures[0])
	if err != nil {
		return false, err
	}
	for _, ref := range l.Captures[1:] {
		cap, _, err := resolveCapture(scope, ref)
		if err != nil {
			return false, err
		}
		if cap.Text != first.Text {
			return false, nil
		}
	}
	return true, nil
}

// sameNodeIdentity returns true when two captures bind to the same AST
// node. For non-sequence captures (Children == nil) it falls back to
// *sitter.Node.Equal when both nodes are non-nil; otherwise compares
// (StartByte, EndByte). For sequence captures it compares the outer span.
// Mixed sequence + single captures still work via the byte-span fallback
// — both shapes carry StartByte/EndByte set to the outer span.
func sameNodeIdentity(a Capture, an *sitter.Node, b Capture, bn *sitter.Node) bool {
	if an != nil && bn != nil && len(a.Children) == 0 && len(b.Children) == 0 {
		return an.Equal(bn)
	}
	return a.StartByte == b.StartByte && a.EndByte == b.EndByte
}

// (Sub-pattern evaluation, sub-pattern compile cache, ancestor/descendant
// candidate enumeration, and matchTreeWithNodes / findNodeBySpan live in
// where_subpattern.go to keep where.go below the file-size fail
// threshold. See that file for inside_pattern + contains_pattern logic.)
