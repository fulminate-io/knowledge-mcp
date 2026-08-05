// SPDX-License-Identifier: Apache-2.0

// match_walk.go — worker pool, per-file orchestration, and per-node
// match application for ast.Match. Split out of match.go so each
// file stays under the file-size warning threshold while keeping match.go
// focused on the public surface (Match, RawMatch, WalkStats).
//
// V2 NOTE: only the per-file matchFile body changed between v1 and v2.
// runWorkers and withMatchRecover are preserved verbatim from the closed-set
// engine; processFile no longer is — its result cap was removed, so it carries
// every match into the caller's chosen sink: mergeMatches (count.go) for
// match/replace, or the count tally (recordLightCountTally, count.go) when
// runWorkerArgs.tally is set. The matcher drives matchTree + evalWhere.

package ast

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// withMatchRecover wraps a worker fn with a deferred recover so a tree-
// sitter panic doesn't crash the whole knowledge-server. Mirrors
// withChunkRecover at parser/indexer_chunk.go:22.
func withMatchRecover(site string, fn func()) func() {
	return func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("ast match: goroutine panic",
					"site", site,
					"err", r,
					"stack", string(debug.Stack()))
			}
		}()
		fn()
	}
}

// runWorkerArgs bundles inputs threaded through the worker pool. The
// compiled pattern is NOT shared across workers — each worker re-compiles
// it from patternSource so no tree-sitter *Tree ever crosses a goroutine
// boundary (go-tree-sitter Trees are not safe for concurrent use; the
// per-Tree cachedNode map is unsynchronized). where is shared safely (its
// only lazy state is a sync.Once-guarded regexp, not a tree).
type runWorkerArgs struct {
	repoDir       string
	lang          treesitter.Language
	patternSource string
	// pinContext rides along with the source so every worker compiles the
	// SAME variant set the caller did — an unpinned recompile would widen the
	// union behind the caller's narrowing.
	pinContext string
	where      *WhereNode
	files      []string
	// tally, when non-nil, switches the walk to count mode: processFile records
	// each file's counts into it and retains no matches (count.go).
	tally *countTally
	// emitParseHint, set only on the replace path, asks matchFile to record a
	// per-matched-file parse hint {clean, size, digest} so ApplyReplace can skip
	// the pre-edit re-parse of a file the match already parsed clean.
	emitParseHint bool
}

// runWorkers fans out file processing across NumCPU workers. Each worker
// owns its own treesitter.Parser AND its own compiled pattern (re-parsed +
// re-compiled from a.patternSource) plus its own sub-pattern compile cache
// — nothing tree-sitter is shared between goroutines, since go-tree-sitter
// Trees (pattern tree, sub-pattern trees) are not safe for concurrent use.
// Returns the aggregated RawMatch slice plus the per-call walkCounters (scan,
// per-cause skip and degraded-parse tallies) and the first per-worker error
// encountered (worker compile and where-tree evaluation errors propagate;
// parse errors silently skip).
func runWorkers(ctx context.Context, a runWorkerArgs) ([]RawMatch, map[string]fileParseHint, *walkCounters, error) {
	counters := &walkCounters{}
	if len(a.files) == 0 {
		return nil, nil, counters, nil
	}
	workers := min(runtime.NumCPU(), len(a.files))
	if workers <= 0 {
		workers = 1
	}

	fileCh := make(chan string, workers)
	go withMatchRecover("Match.feeder", func() {
		defer close(fileCh)
		for _, f := range a.files {
			select {
			case fileCh <- f:
			case <-ctx.Done():
				return
			}
		}
	})()

	var (
		mu       sync.Mutex
		results  []RawMatch
		firstErr atomic.Pointer[errBox]
		wg       sync.WaitGroup
	)
	// cleanHints is allocated only on the replace path (emitParseHint); every
	// worker records its matched files into it under mu, alongside results.
	cleanHints := newCleanHintSink(a.emitParseHint)

	for range workers {
		wg.Go(withMatchRecover("Match.worker", func() {
			tsp := treesitter.NewParser()
			defer tsp.Close()

			// Per-worker compiled pattern + sub-pattern cache. Mirrors the
			// per-worker parser above: each worker owns its own pattern
			// *Trees, rootQueries, and sub-pattern trees so no tree-sitter
			// state is walked concurrently across goroutines. The extra
			// Parse+Compile is one small, amortized cost per worker (not
			// per file) — one short parse per registered wrapper rather
			// than one per pattern; the caller already validated the
			// source compiles.
			pat, perr := Parse(a.patternSource)
			if perr != nil {
				firstErr.CompareAndSwap(nil, &errBox{err: fmt.Errorf("ast/match: worker re-parse pattern: %w", perr)})
				return
			}
			cp, cerr := Compile(pat, a.lang, a.pinContext)
			if cerr != nil {
				firstErr.CompareAndSwap(nil, &errBox{err: fmt.Errorf("ast/match: worker compile pattern: %w", cerr)})
				return
			}
			defer cp.Close()
			cache := map[string][]patternVariant{}
			cacheMu := &sync.Mutex{}
			defer closeSubPatternCache(cache, cacheMu)

			for relPath := range fileCh {
				processFile(ctx, processArgs{
					repoDir:       a.repoDir,
					lang:          a.lang,
					cp:            cp,
					where:         a.where,
					cache:         cache,
					cacheMu:       cacheMu,
					tsp:           tsp,
					relPath:       relPath,
					mu:            &mu,
					results:       &results,
					counters:      counters,
					firstErr:      &firstErr,
					tally:         a.tally,
					emitParseHint: a.emitParseHint,
					cleanHints:    cleanHints,
				})
			}
		}))
	}

	wg.Wait()

	var err error
	if box := firstErr.Load(); box != nil {
		err = box.err
	}
	return results, cleanHints, counters, err
}

// errBox lets us atomically store an error pointer (atomic.Pointer needs a
// concrete pointee).
type errBox struct{ err error }

// processArgs bundles the per-file processing inputs so processFile keeps
// runWorkers' cognitive complexity below the lint cap. cp, cache, and
// cacheMu are the WORKER-LOCAL compiled pattern + sub-pattern cache
// (populated inside the worker goroutine), not shared across workers — so
// the tree-sitter trees they reference are only ever walked by this one
// goroutine.
type processArgs struct {
	repoDir  string
	lang     treesitter.Language
	cp       *CompiledPattern
	where    *WhereNode
	cache    map[string][]patternVariant
	cacheMu  *sync.Mutex
	tsp      *treesitter.Parser
	relPath  string
	mu       *sync.Mutex
	results  *[]RawMatch
	counters *walkCounters
	firstErr *atomic.Pointer[errBox]
	// tally is the count-mode sink (count.go). When non-nil processFile records
	// into it instead of appending to results; nil is the match/replace path.
	tally *countTally
	// emitParseHint + cleanHints are the replace-path parse-hint sink. When
	// emitParseHint is set, matchFile records a {clean, size, digest} hint for
	// each matched file and processFile merges it into cleanHints under mu.
	// cleanHints is nil (and untouched) on the match/count read paths.
	emitParseHint bool
	cleanHints    map[string]fileParseHint
}

// processFile parses one file, applies the v2 walker + where-tree, and
// merges the results into the shared slice under mu. Every file handed to it
// is processed: the only early return is context cancellation, which is not a
// cap. An earlier result-cap short-circuit used to abort the remaining files
// once the walk had accumulated enough matches; it truncated FilesScanned as
// well as the results, so the walk under-reported its own coverage. It has
// been removed.
//
// This is also where the walk's accounting happens: a declined file is
// attributed to the counter for its cause, and a parsed one is recorded with
// whether its tree carried ERROR nodes, so a caller can tell a clean result
// from one read off a tree tree-sitter had to recover.
func processFile(ctx context.Context, a processArgs) {
	if ctx.Err() != nil {
		return
	}
	file := matchFile(ctx, a)
	if file.reason != skipNone {
		a.counters.recordSkip(file.reason)
		return
	}
	// Count mode records the parsed file (with its light-match count feeding
	// MatchesFromDegradedTrees exactly as len(matches) would) and the per-file
	// tally, retaining no RawMatch (count.go).
	if a.tally != nil {
		processCountFile(a, file)
		return
	}
	a.counters.recordParsed(file.degraded, len(file.matches))
	if file.err != nil {
		// Prefer the first observed error so the caller surfaces a
		// stable, well-defined failure cause.
		a.firstErr.CompareAndSwap(nil, &errBox{err: file.err})
		return
	}
	if len(file.matches) == 0 {
		return
	}
	mergeMatches(a, file.matches)
	if file.hint != nil {
		recordCleanHint(a, file.hint)
	}
}

// newCleanHintSink allocates the replace-path parse-hint map, or returns nil on
// the match/count paths where emitParseHint is false and no hint is collected.
func newCleanHintSink(emit bool) map[string]fileParseHint {
	if !emit {
		return nil
	}
	return map[string]fileParseHint{}
}

// recordCleanHint merges one matched file's parse hint into the shared
// cleanHints map under mu (the same mutex mergeMatches holds). It is reached
// only on the replace path, where runWorkers allocated cleanHints; the nil-map
// guard is defensive, not an expected branch.
func recordCleanHint(a processArgs, hint *fileParseHint) {
	if a.cleanHints == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleanHints[a.relPath] = *hint
}

// matchFile reads + parses one file, walks its AST top-down, and at every
// named node attempts a structural match against the compiled PatternTree.
// Successful structural matches are filtered through the where-tree
// (when set) before being emitted as RawMatch. Tree close happens via
// defer — preserved CGO discipline.
//
// Each of the two decline paths returns the TYPED cause rather than a bare
// skipped flag, so the walk can report which rule cost it a file instead of
// one undifferentiated total.
func matchFile(ctx context.Context, a processArgs) fileResult {
	absPath := filepath.Join(a.repoDir, a.relPath)
	src, err := os.ReadFile(absPath)
	if err != nil {
		slog.Warn("ast match: skip file", "path", a.relPath, "error", err, "reason", "read")
		return fileResult{reason: skipRead}
	}

	tree, err := a.tsp.Parse(ctx, src, a.lang)
	if err != nil {
		// NO-E2E-FIXTURE: nothing can drive this branch from a fixture — tree-sitter error-recovers malformed input into a tree with ERROR nodes instead of failing, and the binding's other nil-tree causes (cancelled context, no language set) are both unreachable from here. It is wired and proven at the cause-to-counter seam instead, and kept because files_skipped is defined as the sum of its causes.
		reason, logReason := skipParseError, "parse"
		if errors.Is(err, treesitter.ErrParseTimeout) {
			// NO-E2E-FIXTURE: driving this needs a 30-second wall-clock parse overrun, and the limit is an unexported const in the parser wrapper with no setter and no test hook. Proven at the cause-to-counter seam like the branch above.
			reason, logReason = skipParseLimit, "parse_timeout"
		}
		slog.Warn("ast match: skip file", "path", a.relPath, "error", err, "reason", logReason)
		return fileResult{reason: reason}
	}
	defer tree.Close()

	// HasError is an O(1) flag read on a root the walk already holds — a bit
	// tree-sitter maintains during parsing, not a traversal — so the degraded
	// signal costs the walk nothing per file.
	degraded := tree.RootNode().HasError()

	outerScope := newOuterScope(a.lang, a.cache, a.cacheMu)

	mc := matchContext{
		cp:         a.cp,
		where:      a.where,
		relPath:    a.relPath,
		src:        src,
		outerScope: outerScope,
		caps:       newCaptures(),
		nodes:      make(map[string]*sitter.Node),
	}
	// Count mode runs the body-free light walk: it counts the SAME deduped
	// matches collectMatches would return but retains only {span, kind} per
	// match, never a RawMatch (count.go).
	if a.tally != nil {
		lms, err := mc.collectCounts(ctx, tree.RootNode())
		if err != nil {
			return fileResult{degraded: degraded, err: err}
		}
		return fileResult{lightMatches: lms, degraded: degraded}
	}
	matches, err := mc.collectMatches(ctx, tree.RootNode())
	if err != nil {
		return fileResult{degraded: degraded, err: err}
	}
	res := fileResult{matches: matches, degraded: degraded}
	// Replace-path parse hint: only matched files reach ApplyReplace's baseline,
	// so only they need a hint. The digest is over the SAME src bytes the splice
	// will read (readMatchedSources re-reads the file, and the guard re-checks
	// size+digest), so a match certifies the spliced bytes were parsed clean.
	if a.emitParseHint && len(matches) > 0 {
		res.hint = &fileParseHint{clean: !degraded, size: len(src), digest: fnv64a(src)}
	}
	return res
}

// matchContext holds per-file match state. Extracted from matchFile to keep
// cognitive complexity manageable.
type matchContext struct {
	cp         *CompiledPattern
	where      *WhereNode
	relPath    string
	src        []byte
	outerScope *evalScope
	caps       *Captures
	nodes      map[string]*sitter.Node
}

// tryMatch attempts a structural match of one variant at node n, returning the
// RawMatch on success. mc.caps/mc.nodes are REUSED across every candidate —
// matching and non-matching alike — reset at the top of each call. Reuse is
// safe on the success path because toRawMatch fully COPIES every field out
// (copyAligns/copyDropped/copyComments make fresh slices, and each Capture's
// Children were built by bindSeq as a fresh per-match slice), and the eval
// scope withMatchCaptures builds never escapes tryMatch — evalWhere completes
// synchronously before the return. So the next candidate's reset() cannot
// corrupt a RawMatch already handed back.
func (mc *matchContext) tryMatch(ctx context.Context, n *sitter.Node, v *compiledVariant) (RawMatch, bool, error) {
	mc.caps.reset()
	clearNodeMap(mc.nodes)
	// where==nil is the dominant placeholder-rooted path. It never consults the
	// nodes map or the synthetic $match capture, so matchTreeWithNodes' extra
	// work — the $match nodeToCapture Content copy and the findNodeBySpan
	// nodes-population loop (where_subpattern.go) — is pure dead allocation here.
	// Delegate to matchTree (walker.go), the same structural match
	// matchTreeWithNodes itself wraps; only the where!=nil path, whose evalWhere
	// resolves captures through nodes/$match, keeps the fuller call. The
	// separate evalSubPattern site (where_subpattern.go) has its own l.Where==nil
	// dead-work but is off this hot path and left as-is by scope.
	var matched bool
	if mc.where != nil {
		matched = matchTreeWithNodes(v.Tree, n, mc.src, mc.caps, mc.nodes)
	} else {
		matched = matchTree(v.Tree, n, mc.src, mc.caps)
	}
	if !matched {
		return RawMatch{}, false, nil
	}
	if mc.where != nil {
		scope := mc.outerScope.withMatchCaptures(mc.caps, mc.nodes, mc.src)
		ok, werr := evalWhere(ctx, mc.where, scope)
		if werr != nil {
			return RawMatch{}, false, werr
		}
		if !ok {
			return RawMatch{}, false, nil
		}
	}
	rm := toRawMatch(n, mc.relPath, mc.caps, mc.src, v)
	return rm, true, nil
}

// collectMatches runs every compiled variant against ONE parse of the file and
// returns the deduped result set.
//
// Query-driven variants (those whose root kind compiled to a tree-sitter
// query) each get their own cursor over the same tree, which preserves the
// C-side per-type indexing the query exists for. Every walk-driven variant
// shares ONE walkAll: the file walk stays O(files), never O(files x variants).
func (mc *matchContext) collectMatches(ctx context.Context, root *sitter.Node) ([]RawMatch, error) {
	var (
		out      []RawMatch
		seen     = map[byteRange]dedupeSlot{}
		walkDone bool
	)
	for i := range mc.cp.Variants {
		v := &mc.cp.Variants[i]
		var (
			found []variantMatch
			err   error
		)
		switch {
		case v.rootQuery != nil:
			found, err = mc.collectViaQuery(ctx, root, v, i)
		case walkDone:
			continue
		default:
			walkDone = true
			found, err = mc.collectViaWalk(ctx, root)
		}
		if err != nil {
			return nil, err
		}
		for _, vm := range found {
			absorbMatch(&out, seen, vm)
		}
	}
	return out, nil
}

func (mc *matchContext) collectViaQuery(ctx context.Context, root *sitter.Node, v *compiledVariant, idx int) ([]variantMatch, error) {
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(v.rootQuery, root)
	var matches []variantMatch
	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}
		for _, c := range m.Captures {
			n := c.Node
			if v.rootDescended {
				n = effectiveTargetNode(n)
			}
			rm, matched, err := mc.tryMatch(ctx, n, v)
			if err != nil {
				return nil, err
			}
			if matched {
				matches = append(matches, variantMatch{match: rm, variant: idx})
			}
		}
	}
	return matches, nil
}

// collectViaWalk runs the single shared walk for every walk-driven variant. At
// each node the variants are tried in candidate order and the first match wins:
// a second variant matching the same node would produce the same outer span and
// be collapsed by the dedupe anyway.
func (mc *matchContext) collectViaWalk(ctx context.Context, root *sitter.Node) ([]variantMatch, error) {
	var (
		matches []variantMatch
		walkErr error
	)
	walkAll(root, func(n *sitter.Node) {
		if walkErr != nil {
			return
		}
		for i := range mc.cp.Variants {
			v := &mc.cp.Variants[i]
			if v.rootQuery != nil {
				continue
			}
			rm, matched, err := mc.tryMatch(ctx, n, v)
			if err != nil {
				walkErr = err
				return
			}
			if matched {
				matches = append(matches, variantMatch{match: rm, variant: i})
				return
			}
		}
	})
	return matches, walkErr
}
