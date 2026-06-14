// SPDX-License-Identifier: Apache-2.0

// match_walk.go — worker pool, per-file orchestration, and per-node
// match application for ast.Match. Split out of match.go so each
// file stays under the file-size warning threshold while keeping match.go
// focused on the public surface (Match, RawMatch, WalkStats).
//
// V2 NOTE: only the per-file matchFile body changed between v1 and v2.
// runWorkers / processFile / mergeMatches / withMatchRecover are
// preserved verbatim from the closed-set engine. The per-file matcher
// now drives matchTree + evalWhere instead of cursor.Exec.

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
	where         *WhereNode
	files         []string
	limit         int
}

// runWorkers fans out file processing across NumCPU workers. Each worker
// owns its own treesitter.Parser AND its own compiled pattern (re-parsed +
// re-compiled from a.patternSource) plus its own sub-pattern compile cache
// — nothing tree-sitter is shared between goroutines, since go-tree-sitter
// Trees (pattern tree, sub-pattern trees) are not safe for concurrent use.
// Returns the aggregated RawMatch slice plus per-call scanned / skipped
// counters and the first per-worker error encountered (worker compile and
// where-tree evaluation errors propagate; parse errors silently skip).
func runWorkers(ctx context.Context, a runWorkerArgs) ([]RawMatch, int, int, error) {
	if len(a.files) == 0 {
		return nil, 0, 0, nil
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
		scanned  atomic.Int64
		skipped  atomic.Int64
		capLimit atomic.Bool
		firstErr atomic.Pointer[errBox]
		wg       sync.WaitGroup
	)

	for range workers {
		wg.Go(withMatchRecover("Match.worker", func() {
			tsp := treesitter.NewParser()
			defer tsp.Close()

			// Per-worker compiled pattern + sub-pattern cache. Mirrors the
			// per-worker parser above: each worker owns its own pattern
			// *Tree, rootQuery, and sub-pattern trees so no tree-sitter
			// state is walked concurrently across goroutines. The extra
			// Parse+Compile is one small, amortized cost per worker (not
			// per file); the caller already validated the source compiles.
			pat, perr := Parse(a.patternSource)
			if perr != nil {
				firstErr.CompareAndSwap(nil, &errBox{err: fmt.Errorf("ast/match: worker re-parse pattern: %w", perr)})
				return
			}
			cp, cerr := Compile(pat, a.lang)
			if cerr != nil {
				firstErr.CompareAndSwap(nil, &errBox{err: fmt.Errorf("ast/match: worker compile pattern: %w", cerr)})
				return
			}
			defer cp.Close()
			cache := map[string]*PatternTree{}
			cacheMu := &sync.Mutex{}
			defer closeSubPatternCache(cache, cacheMu)

			for relPath := range fileCh {
				processFile(ctx, processArgs{
					repoDir:  a.repoDir,
					lang:     a.lang,
					cp:       cp,
					where:    a.where,
					cache:    cache,
					cacheMu:  cacheMu,
					tsp:      tsp,
					relPath:  relPath,
					limit:    a.limit,
					mu:       &mu,
					results:  &results,
					scanned:  &scanned,
					skipped:  &skipped,
					capLimit: &capLimit,
					firstErr: &firstErr,
				})
			}
		}))
	}

	wg.Wait()

	var err error
	if box := firstErr.Load(); box != nil {
		err = box.err
	}
	return results, int(scanned.Load()), int(skipped.Load()), err
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
	cache    map[string]*PatternTree
	cacheMu  *sync.Mutex
	tsp      *treesitter.Parser
	relPath  string
	limit    int
	mu       *sync.Mutex
	results  *[]RawMatch
	scanned  *atomic.Int64
	skipped  *atomic.Int64
	capLimit *atomic.Bool
	firstErr *atomic.Pointer[errBox]
}

// processFile parses one file, applies the v2 walker + where-tree, and
// merges the results into the shared slice under mu. Honors the cap-limit
// short-circuit so workers stop doing real work once limit is hit
// (channel still drains).
func processFile(ctx context.Context, a processArgs) {
	if ctx.Err() != nil {
		return
	}
	if a.capLimit.Load() {
		return
	}
	file := matchFile(ctx, a)
	if file.skipped {
		a.skipped.Add(1)
		return
	}
	a.scanned.Add(1)
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
}

// mergeMatches appends file matches under mu, applying the global limit
// cap and toggling capLimit when reached.
func mergeMatches(a processArgs, matches []RawMatch) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(*a.results)+len(matches) >= a.limit {
		take := a.limit - len(*a.results)
		if take > 0 {
			*a.results = append(*a.results, matches[:take]...)
		}
		a.capLimit.Store(true)
		return
	}
	*a.results = append(*a.results, matches...)
}

// fileResult is the per-file outcome from matchFile: either a slice of
// RawMatch (success) or skipped=true (logged + drop) or err (where-tree
// evaluation failure; surfaced upward).
type fileResult struct {
	matches []RawMatch
	skipped bool
	err     error
}

// matchFile reads + parses one file, walks its AST top-down, and at every
// named node attempts a structural match against the compiled PatternTree.
// Successful structural matches are filtered through the where-tree
// (when set) before being emitted as RawMatch. Tree close happens via
// defer — preserved CGO discipline.
func matchFile(ctx context.Context, a processArgs) fileResult {
	absPath := filepath.Join(a.repoDir, a.relPath)
	src, err := os.ReadFile(absPath)
	if err != nil {
		slog.Warn("ast match: skip file", "path", a.relPath, "error", err, "reason", "read")
		return fileResult{skipped: true}
	}

	tree, err := a.tsp.Parse(ctx, src, a.lang)
	if err != nil {
		reason := "parse"
		if errors.Is(err, treesitter.ErrParseTimeout) {
			reason = "parse_timeout"
		}
		slog.Warn("ast match: skip file", "path", a.relPath, "error", err, "reason", reason)
		return fileResult{skipped: true}
	}
	defer tree.Close()

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
	matches, err := mc.collectMatches(ctx, tree.RootNode())
	if err != nil {
		return fileResult{err: err}
	}
	return fileResult{matches: matches}
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

// tryMatch attempts a structural match at node n, returning the RawMatch
// on success. Reuses caps/nodes across non-matching candidates;
// allocates fresh ones on match (withMatchCaptures retains references).
func (mc *matchContext) tryMatch(ctx context.Context, n *sitter.Node) (RawMatch, bool, error) {
	mc.caps.reset()
	clearNodeMap(mc.nodes)
	if !matchTreeWithNodes(mc.cp.Tree, n, mc.src, mc.caps, mc.nodes) {
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
	rm := toRawMatch(n, mc.relPath, mc.caps, mc.src)
	mc.caps = newCaptures()
	mc.nodes = make(map[string]*sitter.Node)
	return rm, true, nil
}

// collectMatches finds candidate nodes and runs tryMatch on each. Uses
// the tree-sitter query cursor when a rootQuery is compiled (skips
// irrelevant subtrees in C), otherwise falls back to walkAll.
func (mc *matchContext) collectMatches(ctx context.Context, root *sitter.Node) ([]RawMatch, error) {
	if mc.cp.rootQuery != nil {
		return mc.collectViaQuery(ctx, root)
	}
	return mc.collectViaWalk(ctx, root)
}

func (mc *matchContext) collectViaQuery(ctx context.Context, root *sitter.Node) ([]RawMatch, error) {
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(mc.cp.rootQuery, root)
	var matches []RawMatch
	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}
		for _, c := range m.Captures {
			n := c.Node
			if mc.cp.rootDescended {
				n = effectiveTargetNode(n)
			}
			rm, matched, err := mc.tryMatch(ctx, n)
			if err != nil {
				return nil, err
			}
			if matched {
				matches = append(matches, rm)
			}
		}
	}
	return matches, nil
}

func (mc *matchContext) collectViaWalk(ctx context.Context, root *sitter.Node) ([]RawMatch, error) {
	var (
		matches []RawMatch
		walkErr error
	)
	walkAll(root, func(n *sitter.Node) {
		if walkErr != nil {
			return
		}
		rm, matched, err := mc.tryMatch(ctx, n)
		if err != nil {
			walkErr = err
			return
		}
		if matched {
			matches = append(matches, rm)
		}
	})
	return matches, walkErr
}

// toRawMatch builds the per-match RawMatch from a successful structural
// match. The "match" capture key is reserved for the outer-node binding;
// individual placeholder captures already live in caps.byName. The
// internal "$match" key (synthesized by matchTreeWithNodes for where-tree
// resolution) is filtered out — the bare "match" key is the user-facing
// surface, $match is engine-internal scope plumbing.
func toRawMatch(outer *sitter.Node, relPath string, caps *Captures, src []byte) RawMatch {
	rm := RawMatch{
		FilePath:  relPath,
		StartLine: int(outer.StartPoint().Row) + 1,
		EndLine:   int(outer.EndPoint().Row) + 1,
		Captures:  make(map[string]Capture, len(caps.byName)+1),
	}
	rm.Captures["match"] = nodeToCapture(outer, src)
	for name, cap := range caps.byName {
		if name == "$match" {
			continue
		}
		rm.Captures[name] = cap
	}
	return rm
}
