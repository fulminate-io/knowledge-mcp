// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"log/slog"
	"regexp"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

// filterPredicates is a drop-in replacement for
// (*sitter.QueryCursor).FilterPredicates that caches compiled #match? /
// #not-match? regexes instead of recompiling them on every match.
//
// Upstream's bindings.go calls regexp.MustCompile(...) inside the per-match
// loop, so a query whose pattern matches N nodes recompiles the same regex N
// times. For the JS/TS test-block query — which matches every call expression
// and then regex-filters by callee name — that was ~21 GB of transient
// garbage on a single ~1900-file repo collect, dominating GC/madvise time.
// Caching by pattern string drops that to one compile per distinct regex per
// process.
//
// The query is supplied explicitly because the cursor's *Query handle is
// unexported; every call site already holds the compiled query
// (compiledQuerySet.{topLevel,calls,imports,typeRefs,testBlocks}). Semantics
// mirror upstream: only eq?/not-eq?/match?/not-match? are interpreted; other
// predicates are ignored; a match passes only if every interpreted predicate
// holds; on failure the returned QueryMatch carries no captures.
func filterPredicates(q *sitter.Query, m *sitter.QueryMatch, src []byte) *sitter.QueryMatch {
	out := &sitter.QueryMatch{ID: m.ID, PatternIndex: m.PatternIndex}
	predicates := q.PredicatesForPattern(uint32(m.PatternIndex))
	if len(predicates) == 0 {
		out.Captures = m.Captures
		return out
	}
	for _, steps := range predicates {
		op := q.StringValueForId(steps[0].ValueId)
		switch op {
		case "eq?", "not-eq?":
			if !evalEqPredicate(q, op == "eq?", steps, m, src) {
				return out
			}
		case "match?", "not-match?":
			if !evalMatchPredicate(q, op == "match?", steps, m, src) {
				return out
			}
		}
	}
	out.Captures = append(out.Captures, m.Captures...)
	return out
}

// evalEqPredicate handles #eq? / #not-eq? — capture-vs-capture or
// capture-vs-string equality. Returns true when the predicate holds (or is
// inapplicable to this match's captures, matching upstream's behavior of
// leaving matchedAll untouched when a referenced capture is absent).
func evalEqPredicate(q *sitter.Query, isPositive bool, steps []sitter.QueryPredicateStep, m *sitter.QueryMatch, src []byte) bool {
	leftName := q.CaptureNameForId(steps[1].ValueId)

	if steps[2].Type == sitter.QueryPredicateStepTypeCapture {
		rightName := q.CaptureNameForId(steps[2].ValueId)
		var left, right *sitter.Node
		for _, c := range m.Captures {
			switch q.CaptureNameForId(c.Index) {
			case leftName:
				left = c.Node
			case rightName:
				right = c.Node
			}
			if left != nil && right != nil {
				return (left.Content(src) == right.Content(src)) == isPositive
			}
		}
		return true
	}

	want := q.StringValueForId(steps[2].ValueId)
	for _, c := range m.Captures {
		if q.CaptureNameForId(c.Index) != leftName {
			continue
		}
		if (c.Node.Content(src) == want) != isPositive {
			return false
		}
	}
	return true
}

// evalMatchPredicate handles #match? / #not-match? — regex test against a
// capture's text, using the process-wide compiled-regex cache.
func evalMatchPredicate(q *sitter.Query, isPositive bool, steps []sitter.QueryPredicateStep, m *sitter.QueryMatch, src []byte) bool {
	capName := q.CaptureNameForId(steps[1].ValueId)
	re := cachedRegexp(q.StringValueForId(steps[2].ValueId))
	if re == nil {
		return true // invalid pattern — already logged once; treat as no filter
	}
	for _, c := range m.Captures {
		if q.CaptureNameForId(c.Index) != capName {
			continue
		}
		if re.MatchString(c.Node.Content(src)) != isPositive {
			return false
		}
	}
	return true
}

// regexpCache memoizes compiled #match? predicate regexes by pattern string.
// Values are *regexp.Regexp; a cached nil means the pattern failed to compile
// (logged once at first sight).
var regexpCache sync.Map

func cachedRegexp(pattern string) *regexp.Regexp {
	if v, ok := regexpCache.Load(pattern); ok {
		re, _ := v.(*regexp.Regexp)
		return re
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		slog.Error("treesitter: #match? predicate regex failed to compile; predicate ignored",
			"pattern", pattern, "error", err)
		// re is nil here — cache it so we log only once.
	}
	actual, _ := regexpCache.LoadOrStore(pattern, re)
	stored, _ := actual.(*regexp.Regexp)
	return stored
}
