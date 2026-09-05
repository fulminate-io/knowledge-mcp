// SPDX-License-Identifier: Apache-2.0

// ast_patterns.go — sibling-form alternation for the ast tool: parsing the
// members of patterns[], compiling them, and running the union walk.
//
// The governing rule here is that ONE BAD MEMBER MUST NOT DESTROY THE CALL. A
// caller who sends four sibling forms and misspells the third wants the other
// three answered, with the third's failure named — not an error that throws
// away three working walks. So parse and compile accumulate per-member failures
// instead of returning on the first one, and the members that worked still run.
//
// Two boundaries keep that from becoming its own silent-zero:
//
//   - When EVERY member fails, the call still errors. A success carrying no
//     results and a list of errors would be exactly the shape this tool's
//     honesty work exists to remove.
//   - The singular `pattern` form is untouched. One pattern that fails is still
//     a hard error, because there is no usable half to preserve.
//
// Split out of ast.go, which is against the file-size cap and shared by several
// concerns; the alternation machinery is self-contained enough to own a file.

package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// patternFailure is one alternation member that could not be used, reported to
// the caller under `pattern_errors`. Index and Pattern identify WHICH member
// failed — without them a caller reading a partial result cannot tell which of
// their sibling forms went missing, which would make the partial result its own
// kind of silence.
type patternFailure struct {
	Index   int    `json:"index"`
	Pattern string `json:"pattern"`
	Error   string `json:"error"`
}

// indexedPattern carries a parsed member's position and source alongside it, so
// a failure discovered later at COMPILE time can still be reported against the
// same patterns[i] the caller wrote.
type indexedPattern struct {
	index   int
	source  string
	pattern ast.Pattern
}

// matchReply is operation=match's response: MatchResults verbatim, plus the
// per-member failures. The embedding is what keeps the existing keys at the top
// level — encoding/json promotes an anonymous struct's fields — so this adds
// pattern_errors without disturbing anything already on the wire.
type matchReply struct {
	ast.MatchResults
	PatternErrors []patternFailure `json:"pattern_errors,omitempty"`
}

// buildAstPatterns parses one or more DSL patterns from the args.
// Mutually-exclusive: pattern OR patterns, never both.
//
// It returns the members that parsed, the failures of those that did not, and
// an error ONLY when the call cannot proceed at all: neither argument supplied,
// both supplied, the singular pattern failing, or every member of patterns[]
// failing. A partial success returns a non-empty first result and a non-empty
// second one together.
func buildAstPatterns(a astArgs) ([]indexedPattern, []patternFailure, error) {
	hasSingle := strings.TrimSpace(a.Pattern) != ""
	if hasSingle && len(a.Patterns) > 0 {
		return nil, nil, fmt.Errorf("specify pattern OR patterns, not both")
	}
	if !hasSingle && len(a.Patterns) == 0 {
		return nil, nil, fmt.Errorf("operation=%s requires pattern (or patterns for sibling-form alternation)", a.Operation)
	}
	if hasSingle {
		// The singular form keeps its original contract exactly: the parse error
		// is the call's error, undecorated.
		p, err := ast.Parse(a.Pattern)
		if err != nil {
			return nil, nil, err
		}
		return []indexedPattern{{index: 0, source: a.Pattern, pattern: p}}, nil, nil
	}
	out := make([]indexedPattern, 0, len(a.Patterns))
	var failures []patternFailure
	for i, src := range a.Patterns {
		s := strings.TrimSpace(src)
		if s == "" {
			failures = append(failures, patternFailure{
				Index: i, Pattern: src, Error: fmt.Sprintf("patterns[%d] is empty", i),
			})
			continue
		}
		p, err := ast.Parse(s)
		if err != nil {
			// The message text is unchanged from when this aborted the call, so
			// a failure reads exactly as it always did; only its blast radius
			// shrank from the whole call to the one member.
			failures = append(failures, patternFailure{
				Index: i, Pattern: s, Error: fmt.Errorf("patterns[%d] %q: %w", i, s, err).Error(),
			})
			continue
		}
		out = append(out, indexedPattern{index: i, source: s, pattern: p})
	}
	if len(out) == 0 {
		return nil, failures, allPatternsFailedError(failures)
	}
	return out, failures, nil
}

// allPatternsFailedError is the hard error for a call where not one member
// survived. It names every failure rather than only the first: with nothing
// left to run, the caller's next move is fixing all of them.
func allPatternsFailedError(failures []patternFailure) error {
	if len(failures) == 1 {
		// One member, so there is no index to disambiguate and no usable half to
		// report — the member's failure IS the call's failure, worded as it was
		// before accumulation existed.
		return errors.New(failures[0].Error)
	}
	msgs := make([]string, 0, len(failures))
	for _, f := range failures {
		msgs = append(msgs, f.Error)
	}
	return fmt.Errorf("no pattern could be used: %s", strings.Join(msgs, "; "))
}

// matchAll compiles each pattern and runs ast.Match, unioning the results.
// Walk stats are summed: FilesScanned/FilesSkipped take the max across runs
// (same repo, single-pass scans), DurationMS sums (sequential walks). The
// exclusion report is taken from the FIRST walk rather than merged: every
// pattern walks the same repo under the same scope, so each run rediscovers the
// identical decline set, and summing them would multiply one exclusion by the
// number of alternation members.
//
// A member that fails to COMPILE joins prior (parse-time) failures and is
// skipped; the rest still walk. When that leaves nothing to run at all, the
// call errors — the same all-fail boundary buildAstPatterns enforces at parse
// time, applied one stage later.
//
// It also returns the compiled-variant descriptors, because it owns the Compile
// call and so is the only place that sees them. With `patterns` alternation the
// descriptors are the concatenation across every pattern, deduped on (root
// kind, contexts) — otherwise a caller cannot tell which alternation member
// contributed which kind. pin narrows every pattern's union identically: a
// caller who censused with a pin and then rewrites with the same pin must get
// the same set both times.
//
// Perf note: each pattern triggers an independent repo walk. For a worker
// running N corpus annotations × M sibling forms each, this is N×M walks.
// The MVP shape is acceptable for the current corpus size; if the worker's
// total wall-clock becomes a concern, the engine can be extended to compile
// all patterns up front and dispatch matchTree per node per pattern in a
// single walk (constant-N file IO). Removing the compile fail-fast does not
// change that shape — it changes only which walks are skipped, so a call with
// several bad members now does the work for the good ones, which is the point.
func matchAll(ctx context.Context, a astArgs, lang treesitter.Language, patterns []indexedPattern, priorFailures []patternFailure, where *ast.WhereNode, repoDir string, scope ast.Scope) ([]ast.RawMatch, []ast.CompiledVariant, []ast.CompiledVariant, ast.WalkStats, []patternFailure, error) {
	var union []ast.RawMatch
	var walk ast.WalkStats
	failures := priorFailures
	ran := 0
	compiled := newVariantDeduper()
	narrowed := newVariantDeduper()
	for _, ip := range patterns {
		cp, err := ast.Compile(ip.pattern, lang, a.Context)
		if err != nil {
			failures = append(failures, patternFailure{
				Index: ip.index, Pattern: ip.source,
				Error: fmt.Errorf("compile pattern %q: %w", ip.source, err).Error(),
			})
			continue
		}
		ran++
		compiled.add(cp.Describe())
		// The narrowed member readings are disclosed on their OWN channel so a
		// caller can see the variant the keyword rule dropped and the pin that
		// restores it, without it polluting the kept `compiled` set.
		narrowed.add(cp.DescribeNarrowed())
		raws, w, merr := ast.Match(ctx, repoDir, lang, cp, where, scope)
		cp.Close()
		if merr != nil {
			// A walk that fails is not a member that could not be used — it is
			// the walk itself breaking, so it stays fatal.
			return nil, nil, nil, ast.WalkStats{}, failures, merr
		}
		union = append(union, raws...)
		mergeWalkStats(&walk, w)
	}
	if ran == 0 {
		return nil, nil, nil, ast.WalkStats{}, failures, allPatternsFailedError(failures)
	}
	return union, compiled.out, narrowed.out, walk, failures, nil
}

// countAll is the operation=count counterpart to matchAll: it compiles each
// pattern and runs ast.Count — the body-free walk that retains a per-file tally
// instead of every RawMatch — merging the tallies ADDITIVELY (total sums,
// by_file and by_kind sum per key) exactly as matchAll unions its raws with a
// plain no-dedupe append. Walk stats fold through the SAME mergeWalkStats
// arithmetic and both variant-descriptor lists dedupe through the SAME
// variantDeduper, so the count response is byte-for-byte what building it from
// matchAll's []RawMatch produced — only the retention differs. It mirrors
// matchAll's compile-failure accumulation and all-fail boundary.
func countAll(ctx context.Context, a astArgs, lang treesitter.Language, patterns []indexedPattern, priorFailures []patternFailure, where *ast.WhereNode, repoDir string, scope ast.Scope) (ast.CountTally, []ast.CompiledVariant, []ast.CompiledVariant, ast.WalkStats, []patternFailure, error) {
	tally := ast.CountTally{ByFile: map[string]int{}, ByKind: map[string]int{}}
	var walk ast.WalkStats
	failures := priorFailures
	ran := 0
	compiled := newVariantDeduper()
	narrowed := newVariantDeduper()
	for _, ip := range patterns {
		cp, err := ast.Compile(ip.pattern, lang, a.Context)
		if err != nil {
			failures = append(failures, patternFailure{
				Index: ip.index, Pattern: ip.source,
				Error: fmt.Errorf("compile pattern %q: %w", ip.source, err).Error(),
			})
			continue
		}
		ran++
		compiled.add(cp.Describe())
		narrowed.add(cp.DescribeNarrowed())
		ct, w, cerr := ast.Count(ctx, repoDir, lang, cp, where, scope)
		cp.Close()
		if cerr != nil {
			return ast.CountTally{}, nil, nil, ast.WalkStats{}, failures, cerr
		}
		tally.Total += ct.Total
		for path, n := range ct.ByFile {
			tally.ByFile[path] += n
		}
		for kind, n := range ct.ByKind {
			tally.ByKind[kind] += n
		}
		mergeWalkStats(&walk, w)
	}
	if ran == 0 {
		return ast.CountTally{}, nil, nil, ast.WalkStats{}, failures, allPatternsFailedError(failures)
	}
	return tally, compiled.out, narrowed.out, walk, failures, nil
}

// variantDeduper accumulates compiled-variant descriptors across alternation
// members, deduping on the (root kind, contexts) key. matchAll and countAll
// share it so both report the identical descriptor set — in pattern order, the
// repeat collapsed — for the same patterns[].
type variantDeduper struct {
	seen map[string]struct{}
	out  []ast.CompiledVariant
}

func newVariantDeduper() *variantDeduper {
	return &variantDeduper{seen: map[string]struct{}{}}
}

func (d *variantDeduper) add(vs []ast.CompiledVariant) {
	for _, cv := range vs {
		key := cv.RootKind + "\x00" + strings.Join(cv.Contexts, ",")
		if _, dup := d.seen[key]; dup {
			continue
		}
		d.seen[key] = struct{}{}
		d.out = append(d.out, cv)
	}
}

// mergeWalkStats folds one pattern's walk stats into the running union with the
// alternation arithmetic, shared by matchAll and countAll so the count path's
// WalkStats is field-for-field what the match path reports:
//   - FilesScanned: MAX (same repo, single-pass scans).
//   - FilesSkipped: MAX, and its three by-cause counters travel WITH it as one
//     group — merging them field-by-field could pair one pass's total with
//     another pass's breakdown and break files_skipped == sum of its causes.
//   - FilesWithParseErrors: MAX. MatchesFromDegradedTrees: SUM (every pattern's
//     matches join the union, so degraded-origin matches total across passes).
//   - TestFilesScanned and TestFilesExcluded: MAX each, matching FilesScanned,
//     because every member rediscovers the identical file set and the scope
//     filter is the same on every pass. EVERY COUNTER NEEDS ITS RULE HERE, and
//     that is not a style note: this function is a hand-kept per-field policy
//     with no compiler pointing at it, so a WalkStats field added without a line
//     below reads zero on every patterns:[...] call — and on every single-pattern
//     tool call too, since both go through this fold — while the engine reports
//     it correctly to every other caller.
//   - The exclusion report + DiscoveryPath: taken from the FIRST walk only —
//     every pattern rediscovers the identical decline set, so summing would
//     multiply one exclusion by the member count.
//   - DurationMS: SUM (sequential walks).
func mergeWalkStats(dst *ast.WalkStats, w ast.WalkStats) {
	if w.FilesScanned > dst.FilesScanned {
		dst.FilesScanned = w.FilesScanned
	}
	if w.FilesSkipped > dst.FilesSkipped {
		dst.FilesSkipped = w.FilesSkipped
		dst.SkippedRead = w.SkippedRead
		dst.SkippedParseError = w.SkippedParseError
		dst.SkippedParseLimit = w.SkippedParseLimit
	}
	if w.FilesWithParseErrors > dst.FilesWithParseErrors {
		dst.FilesWithParseErrors = w.FilesWithParseErrors
	}
	if w.TestFilesScanned > dst.TestFilesScanned {
		dst.TestFilesScanned = w.TestFilesScanned
	}
	if w.TestFilesExcluded > dst.TestFilesExcluded {
		dst.TestFilesExcluded = w.TestFilesExcluded
	}
	dst.MatchesFromDegradedTrees += w.MatchesFromDegradedTrees
	if dst.DiscoveryPath == "" {
		dst.DiscoveryPath = w.DiscoveryPath
		dst.ExcludedByRule = w.ExcludedByRule
		dst.ExcludedSamples = w.ExcludedSamples
		dst.ExcludedTruncated = w.ExcludedTruncated
	}
	dst.DurationMS += w.DurationMS
	// CleanHint (replace path only) unions across alternation members: each
	// member walks the same files, so a path's hint is identical whichever member
	// recorded it. Delegated to ast because the hint element type is unexported.
	ast.MergeCleanHint(dst, w)
}
