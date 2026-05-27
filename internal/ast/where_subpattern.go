// SPDX-License-Identifier: Apache-2.0

// where_subpattern.go — sub-pattern (inside_pattern / contains_pattern)
// evaluation for the v2 where-tree. Split from where.go to keep both
// files under the 500-LOC fail threshold.
//
// Sub-patterns recurse back through compilePattern + matchTree under a
// per-Match-call compile cache. Recursion depth is hard-capped at
// subPatternMaxDepth (= 8) per Q10. Cross-language sub-patterns are
// deferred in v1 per Q7 (error on Language != scope.lang).

package ast

import (
	"context"
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// subPatternSearch is the search direction discriminator for sub-pattern
// evaluation: ancestor (inside_pattern) or descendant (contains_pattern).
type subPatternSearch int

const (
	ancestorSearch subPatternSearch = iota
	descendantSearch
)

// evalSubPattern compiles the sub-pattern (cached) and walks
// ancestors/descendants of the bound capture's node looking for a match.
// Recursion depth + cross-language guards apply.
func evalSubPattern(ctx context.Context, l *SubPatternLeaf, scope *evalScope, dir subPatternSearch) (bool, error) {
	if scope.depth >= subPatternMaxDepth {
		return false, errSubPatternDepth
	}
	if l.Language != "" && treesitter.Language(l.Language) != scope.lang {
		return false, fmt.Errorf("%w (outer=%s, sub=%s)", errCrossLanguageSubPattern, scope.lang, l.Language)
	}

	_, node, err := resolveCapture(scope, l.Of)
	if err != nil {
		return false, err
	}
	if node == nil {
		// The capture exists but has no live target node (e.g., empty
		// sequence capture). Sub-pattern can't bind against nothing.
		return false, nil
	}

	pt, err := getOrCompileSubPattern(ctx, scope, l.Pattern)
	if err != nil {
		return false, err
	}

	candidates := candidateNodes(node, dir)
	for _, c := range candidates {
		caps := newCaptures()
		nodes := map[string]*sitter.Node{}
		if !matchTreeWithNodes(pt, c, scope.src, caps, nodes) {
			continue
		}
		if l.Where == nil {
			bindAs(scope, l.As, c)
			return true, nil
		}
		sub := scope.pushSubPattern(caps, nodes, scope.src)
		ok, werr := evalWhere(ctx, l.Where, sub)
		if werr != nil {
			return false, werr
		}
		if ok {
			bindAs(scope, l.As, c)
			return true, nil
		}
	}
	return false, nil
}

// bindAs writes the matched ancestor/descendant node into the caller's
// scope under name, so subsequent leaves in the same composer can
// resolve it via resolveCapture. No-op when name is empty (the common
// case — most sub-pattern uses don't need the matched node downstream).
//
// The binding fires only when the leaf returns true. In `not` wrappers
// the binding still happens on inner-match (because the inner reached
// `return true`), then the surrounding `not` flips the verdict — so
// referencing the `as` capture from a sibling-of-`not` is a usage
// smell. Documented on SubPatternLeaf.As.
func bindAs(scope *evalScope, name string, node *sitter.Node) {
	if name == "" || scope == nil || node == nil {
		return
	}
	if scope.captures == nil {
		scope.captures = newCaptures()
	}
	scope.captures.byName[name] = nodeToCapture(node, scope.src)
	if scope.nodeByName == nil {
		scope.nodeByName = map[string]*sitter.Node{}
	}
	scope.nodeByName[name] = node
}

// getOrCompileSubPattern returns the cached compile of source, or compiles
// it under scope.lang and stores in the cache. The cache is mu-protected
// because evalWhere runs concurrently per-file in match orchestration.
func getOrCompileSubPattern(ctx context.Context, scope *evalScope, source string) (*PatternTree, error) {
	scope.cacheMu.Lock()
	if pt, ok := scope.cache[source]; ok {
		scope.cacheMu.Unlock()
		return pt, nil
	}
	scope.cacheMu.Unlock()

	cfg, ok := langConfigFor(scope.lang)
	if !ok {
		return nil, errLanguageNotSupported(scope.lang)
	}
	pat, err := Parse(source)
	if err != nil {
		return nil, fmt.Errorf("ast/where: parse sub-pattern %q: %w", source, err)
	}
	pt, err := compilePattern(ctx, pat, cfg)
	if err != nil {
		return nil, fmt.Errorf("ast/where: compile sub-pattern %q: %w", source, err)
	}

	scope.cacheMu.Lock()
	defer scope.cacheMu.Unlock()
	if existing, ok := scope.cache[source]; ok {
		// Lost the race — discard ours, return cached.
		pt.Close()
		return existing, nil
	}
	scope.cache[source] = pt
	return pt, nil
}

// candidateNodes returns the set of nodes to search under for a sub-
// pattern. ancestor: walk via Parent() up the chain. descendant: pre-order
// DFS over named children.
func candidateNodes(root *sitter.Node, dir subPatternSearch) []*sitter.Node {
	if root == nil {
		return nil
	}
	if dir == ancestorSearch {
		var out []*sitter.Node
		for cur := root.Parent(); cur != nil; cur = cur.Parent() {
			out = append(out, cur)
		}
		return out
	}
	// Descendant search includes the root itself first (so a contains_pattern
	// against the bound capture's own node matches), then named children.
	var out []*sitter.Node
	walkAll(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		out = append(out, n)
	})
	return out
}

// matchTreeWithNodes runs the walker AND records target node pointers per
// captured name into nodes. Used by sub-pattern evaluation so same_node
// can compare nodes via *sitter.Node.Equal across scope levels.
//
// Implicit $match binding: on every successful match (both outer and sub-
// pattern), we synthesize a "$match" entry pointing at the outermost
// matched target node. Locked DSL spec — where-tree leaves can reference
// the root of the local match without a user-supplied named placeholder.
// The leading $ keeps "$match" out of the user's bare-name namespace so
// regular captures cannot collide with it. resolveCapture uses literal
// string lookup (only "$outer." prefixes get special handling), so the
// $-prefixed key resolves uniformly through evalKind / evalMatches /
// evalEquals / evalSameNode.
func matchTreeWithNodes(pt *PatternTree, target *sitter.Node, src []byte, caps *Captures, nodes map[string]*sitter.Node) bool {
	if !matchTree(pt, target, src, caps) {
		return false
	}
	// Inject the implicit $match binding BEFORE the nodes-population loop
	// so the same loop populates nodes["$match"] = target via findNodeBySpan
	// (target's own span trivially matches itself at the recursion root).
	caps.byName["$match"] = nodeToCapture(target, src)
	for name, cap := range caps.byName {
		// For both single and sequence captures, attempt a byte-span
		// lookup: single captures bind to the leaf, sequence captures
		// bind to the smallest descendant covering the full sibling
		// span (which may not exist precisely — findNodeBySpan returns
		// nil in that case and same_node falls back to byte-range
		// comparison via the Capture struct).
		nodes[name] = findNodeBySpan(target, cap.StartByte, cap.EndByte)
	}
	return true
}

// findNodeBySpan locates the smallest descendant of root whose byte range
// equals (start, end). Returns nil when no node exactly matches — the
// caller treats nil as "node identity unavailable".
func findNodeBySpan(root *sitter.Node, start, end uint32) *sitter.Node {
	if root == nil {
		return nil
	}
	if root.StartByte() == start && root.EndByte() == end {
		return root
	}
	if root.StartByte() > start || root.EndByte() < end {
		return nil
	}
	for i := uint32(0); i < root.NamedChildCount(); i++ {
		child := root.NamedChild(int(i))
		if found := findNodeBySpan(child, start, end); found != nil {
			return found
		}
	}
	return nil
}
