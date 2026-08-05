// SPDX-License-Identifier: Apache-2.0

// count.go — the body-free counting path for ast operation:"count", and the
// walk's two match sinks kept together.
//
// Count mirrors Match's body exactly (nil-gate, discoverScopedFiles, the same
// stats assignment, runWorkers, counters.applyTo, the same DurationMS and
// return order) but hands runWorkers a countTally instead of accumulating a
// walk-wide []RawMatch. It counts the SAME matches Match would return: the
// discovery, the per-node variant trials and the outer-span dedup are
// identical (collectCounts mirrors collectMatches leaf-for-leaf). Only the
// per-match work differs — collectCounts binds a body-free lightMatch
// {span, kind} instead of building a RawMatch, so no per-match Captures map is
// allocated and no node source is copied. Retention drops twice over: from
// O(matches) walk-wide (Match, ~808MB for 874k matches) to a per-file total
// plus per-kind counts (O(files)), and the per-file transient a worker holds
// while tallying is a []lightMatch (value structs) rather than a []RawMatch.
//
// The count-mode walk machinery (lightMatch, tryMatchLight, collectCounts and
// its drivers, the light dedup) lives here beside the count tally rather than
// in match_walk.go: it is count's terminal path and belongs with the sink it
// feeds, and keeping it here leaves match_walk.go headroom under the file-size
// cap. mergeMatches lives here for the same reason — the match-retaining append
// and the count tally are the walk's two terminal operations.

package ast

import (
	"context"
	"sync"
	"time"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// CountTally is the caller-facing result of Count: the total match count and
// its per-file and per-CompiledKind breakdowns. A placeholder-rooted pattern
// binds an empty CompiledKind, so ByKind legitimately carries an empty-string
// key — the count handler emits it unchanged.
type CountTally struct {
	Total  int
	ByFile map[string]int
	ByKind map[string]int
}

// countTally is the walk's count sink — the O(files) analog of the walk-wide
// []RawMatch mergeMatches builds. Every worker records into one instance from
// NumCPU goroutines, so its two maps and its total are all guarded by the same
// mutex; Total is kept under that lock rather than as a separate atomic so a
// reader never sees a total that disagrees with the maps it was summed from.
// One record call = one lock acquisition PER FILE, never per match: each worker
// aggregates its file's per-kind counts locally first (recordLightCountTally)
// and merges them in a single call, so contention stays the order mergeMatches
// had.
type countTally struct {
	mu     sync.Mutex
	total  int
	byFile map[string]int
	byKind map[string]int
}

// newCountTally allocates a countTally with its maps ready to record into.
func newCountTally() *countTally {
	return &countTally{
		byFile: map[string]int{},
		byKind: map[string]int{},
	}
}

// record merges one file's tally — its match count and its per-kind counts —
// under a single mutex acquisition. A file reaches record exactly once (one
// worker owns it), so byFile[path] is set rather than accumulated across
// workers; += is used only so a caller passing the same path twice sums rather
// than clobbers.
func (t *countTally) record(path string, count int, byKind map[string]int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.total += count
	t.byFile[path] += count
	for k, n := range byKind {
		t.byKind[k] += n
	}
}

// applyTo writes the tally into the caller-facing CountTally, mirroring
// walkCounters.applyTo. It is the only place the accumulator becomes the result
// struct, so both maps are read under the lock that guarded every write.
func (t *countTally) applyTo(c *CountTally) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c.Total = t.total
	c.ByFile = t.byFile
	c.ByKind = t.byKind
}

// Count walks every file under repoDir matching lang+scope exactly as Match
// does, but records each file's match count and per-kind breakdown into a
// countTally instead of retaining the matches themselves. where may be nil
// (pure structural count). The returned WalkStats is field-for-field what Match
// would report over the same corpus (DurationMS aside), because Count runs the
// identical discovery + worker walk and reads the same walkCounters.
func Count(
	ctx context.Context,
	repoDir string,
	lang treesitter.Language,
	cp *CompiledPattern,
	where *WhereNode,
	scope Scope,
) (CountTally, WalkStats, error) {
	start := time.Now()
	stats := WalkStats{}

	if cp == nil || len(cp.Variants) == 0 {
		return CountTally{}, stats, errMatchNilPattern
	}

	files, report, err := discoverScopedFiles(ctx, repoDir, lang, scope)
	if err != nil {
		return CountTally{}, stats, err
	}
	stats.ExcludedByRule = report.ExcludedByRule
	stats.ExcludedSamples = report.ExcludedSamples
	stats.ExcludedTruncated = report.ExcludedTruncated
	stats.DiscoveryPath = report.DiscoveryPath

	tally := newCountTally()
	_, _, counters, walkErr := runWorkers(ctx, runWorkerArgs{
		repoDir:       repoDir,
		lang:          lang,
		patternSource: cp.Source,
		pinContext:    cp.pin,
		where:         where,
		files:         files,
		tally:         tally,
	})
	counters.applyTo(&stats)
	stats.DurationMS = time.Since(start).Milliseconds()

	var result CountTally
	tally.applyTo(&result)

	if ctx.Err() != nil {
		return result, stats, ctx.Err()
	}
	if walkErr != nil {
		return result, stats, walkErr
	}
	return result, stats, nil
}

// lightMatch is the count-mode analog of a RawMatch: just the outer span (the
// dedup key, identical to outerSpan(RawMatch)) and the compiled variant's
// RootKind (the by_kind key, identical to RawMatch.CompiledKind). No Captures
// map, no source copy — everything the count tally reads and nothing else.
type lightMatch struct {
	span byteRange
	kind string
}

// lightVariantMatch pairs a lightMatch with the index of the variant that
// produced it, mirroring variantMatch so the count dedup can prefer the
// earliest candidate regardless of emission order.
type lightVariantMatch struct {
	match   lightMatch
	variant int
}

// tryMatchLight is the count-mode counterpart of tryMatch: it runs the SAME
// structural match + where-tree filter but binds a body-free lightMatch instead
// of building a RawMatch. mc.caps/mc.nodes are pure scratch here — reset at the
// top and never retained — so no per-match allocation survives the call.
func (mc *matchContext) tryMatchLight(ctx context.Context, n *sitter.Node, v *compiledVariant) (lightMatch, bool, error) {
	mc.caps.reset()
	clearNodeMap(mc.nodes)
	var matched bool
	if mc.where != nil {
		matched = matchTreeWithNodes(v.Tree, n, mc.src, mc.caps, mc.nodes)
	} else {
		matched = matchTree(v.Tree, n, mc.src, mc.caps)
	}
	if !matched {
		return lightMatch{}, false, nil
	}
	if mc.where != nil {
		scope := mc.outerScope.withMatchCaptures(mc.caps, mc.nodes, mc.src)
		ok, werr := evalWhere(ctx, mc.where, scope)
		if werr != nil {
			return lightMatch{}, false, werr
		}
		if !ok {
			return lightMatch{}, false, nil
		}
	}
	return lightMatch{
		span: byteRange{Start: n.StartByte(), End: n.EndByte()},
		kind: v.RootKind,
	}, true, nil
}

// collectCounts is the count-mode counterpart of collectMatches: it runs every
// compiled variant against ONE parse and returns the deduped light-match set.
// Query-driven variants get their own cursor; every walk-driven variant shares
// ONE walkAll. The dedup mirrors collectMatches exactly (absorbLightMatch is
// absorbMatch over lightMatch), so the count it produces is the count Match's
// deduped RawMatch set would have.
func (mc *matchContext) collectCounts(ctx context.Context, root *sitter.Node) ([]lightMatch, error) {
	var (
		out      []lightMatch
		seen     = map[byteRange]dedupeSlot{}
		walkDone bool
	)
	for i := range mc.cp.Variants {
		v := &mc.cp.Variants[i]
		var (
			found []lightVariantMatch
			err   error
		)
		switch {
		case v.rootQuery != nil:
			found, err = mc.collectCountsViaQuery(ctx, root, v, i)
		case walkDone:
			continue
		default:
			walkDone = true
			found, err = mc.collectCountsViaWalk(ctx, root)
		}
		if err != nil {
			return nil, err
		}
		for _, lvm := range found {
			absorbLightMatch(&out, seen, lvm)
		}
	}
	return out, nil
}

// absorbLightMatch is absorbMatch (match_collect.go) over lightMatch: it
// collapses two variants that found the SAME outer span into one entry stamped
// by the earliest candidate. Because a lower variant arriving for an already-
// seen span REPLACES the retained entry, its kind wins too — keeping by_kind
// identical to the RawMatch path, where absorbMatch replaces the whole
// RawMatch (and hence its CompiledKind) under the same lowest-variant rule.
func absorbLightMatch(out *[]lightMatch, seen map[byteRange]dedupeSlot, lvm lightVariantMatch) {
	key := lvm.match.span
	if slot, ok := seen[key]; ok {
		if lvm.variant < slot.variant {
			(*out)[slot.pos] = lvm.match
			seen[key] = dedupeSlot{pos: slot.pos, variant: lvm.variant}
		}
		return
	}
	seen[key] = dedupeSlot{pos: len(*out), variant: lvm.variant}
	*out = append(*out, lvm.match)
}

// collectCountsViaQuery mirrors collectViaQuery: it drives the variant's root
// query cursor over the tree and light-matches each capture (descending to the
// effective target when the root compiled to a descended query).
func (mc *matchContext) collectCountsViaQuery(ctx context.Context, root *sitter.Node, v *compiledVariant, idx int) ([]lightVariantMatch, error) {
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(v.rootQuery, root)
	var matches []lightVariantMatch
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
			lm, matched, err := mc.tryMatchLight(ctx, n, v)
			if err != nil {
				return nil, err
			}
			if matched {
				matches = append(matches, lightVariantMatch{match: lm, variant: idx})
			}
		}
	}
	return matches, nil
}

// collectCountsViaWalk mirrors collectViaWalk: one shared top-down walk over
// which every walk-driven variant is tried in candidate order, first match at a
// node winning (a later variant at the same node would produce the same outer
// span and be collapsed by the dedup anyway).
func (mc *matchContext) collectCountsViaWalk(ctx context.Context, root *sitter.Node) ([]lightVariantMatch, error) {
	var (
		matches []lightVariantMatch
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
			lm, matched, err := mc.tryMatchLight(ctx, n, v)
			if err != nil {
				walkErr = err
				return
			}
			if matched {
				matches = append(matches, lightVariantMatch{match: lm, variant: i})
				return
			}
		}
	})
	return matches, walkErr
}

// processCountFile is the count-mode terminal of processFile: it records the
// parsed file (its light-match count feeding MatchesFromDegradedTrees exactly
// as len(matches) does on the match path) and merges the per-file tally,
// retaining no RawMatch.
func processCountFile(a processArgs, file fileResult) {
	a.counters.recordParsed(file.degraded, len(file.lightMatches))
	if file.err != nil {
		a.firstErr.CompareAndSwap(nil, &errBox{err: file.err})
		return
	}
	if len(file.lightMatches) == 0 {
		return
	}
	recordLightCountTally(a, file.lightMatches)
}

// recordLightCountTally aggregates one file's light matches into the count sink:
// it builds the per-RootKind counts locally, then merges the path, the file's
// match count and those kind counts into the shared countTally in a single
// record call — the O(files) counterpart to mergeMatches' O(matches) append.
func recordLightCountTally(a processArgs, matches []lightMatch) {
	byKind := make(map[string]int, len(matches))
	for _, m := range matches {
		byKind[m.kind]++
	}
	a.tally.record(a.relPath, len(matches), byKind)
}

// mergeMatches appends every one of a file's matches under mu. The slice is
// not pre-sized: the true match count is unknown before the walk finishes, and
// append under the single existing mutex is amortized.
func mergeMatches(a processArgs, matches []RawMatch) {
	a.mu.Lock()
	defer a.mu.Unlock()
	*a.results = append(*a.results, matches...)
}
