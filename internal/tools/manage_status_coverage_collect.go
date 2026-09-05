// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_collect.go — the coverage table's ASSEMBLY half: the
// bounded concurrent Stats fan-out that reads each target's counts, the segment
// probes, the optional stall/working-set seam readers, and the per-row
// backstop-verification lookup.
//
// Split from manage_status_coverage.go for the 500-line cap, along the seam between
// GATHERING the facts and INTERPRETING them. That sibling keeps the CoverageRow wire
// contract, the disposition vocabulary, the verified formula and the renderers — so
// "what is this row" and "how do we say it" stay together.
//
// WHICH GRAPHS EXIST AT ALL is a THIRD file, manage_status_coverage_targets.go: the
// type walk, the per-instance enumeration and the branch-overlay round. It was
// split out when the enumeration stopped walking the sync-eligible subset and grew
// past the same cap. Same package, no signature changed.
package tools

import (
	"context"
	"sync"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// coverageTarget is one graph instance the coverage table reports on: its row
// label, type/name (for the segment-coverage seam), and the Stats selector.
type coverageTarget struct {
	label  string
	gt     kgtypes.GraphType
	name   string
	target *knowledgev1.GraphSelector
	// overlay marks a BRANCH GRAPH row — a code overlay enumerated from its base,
	// addressed by the composed "base@overlay" key. It records PROVENANCE (this row
	// came from an overlay enumeration) rather than testing the name's SHAPE for an
	// "@", which would misclassify a base repo whose name legitimately contained one.
	//
	// The segment and working-set probes are declined for these rows: both key
	// spaces are keyed by the BASE graph and cannot represent a branch graph.
	overlay bool
	// managed is whether a direct MCP interaction has admitted this graph into this
	// client's working set. It is the ADMISSION GATE for the per-graph reads that
	// MATERIALIZE something — both segment probes, which import the graph's L2 pool
	// and construct its engine in this process — per the recorded decision that
	// manage operations never admit and that nothing may interact with a graph absent
	// a direct admission. It is NOT a gate on the Stats RPC: counting is answered
	// from durable state on both backends and makes no graph resident, so every row
	// gets one. See manage_status_coverage_unmanaged.go for the full split and for
	// why the materializing half cannot be gated on the server.
	//
	// A BRANCH ROW READS ITS BASE'S MEMBERSHIP, which is the honest question for a
	// LOAD gate even though it is the wrong one for a BAND (the field above says
	// why the band declines it). The working set normalizes a name by cutting it at
	// the first "@", and a branch graph only ever exists because a collect ran
	// against that base repo — so "does this client maintain the base" is exactly
	// "is this client entitled to read the branch".
	managed bool
	// imageBytes is the graph image's on-disk size from the catalog enumeration,
	// carried so a DECLINED row has one true durable fact to render. It costs
	// nothing: the enumeration already returns it on every GraphInfo.
	imageBytes int64
}

// coverageStatsConcurrency bounds the parallel Stats(IncludeCoverage:true)
// fan-out. Each RPC does per-graph COUNT work server-side; the bound keeps a
// many-graph install from dogpiling the server while still collapsing the
// wall-clock from O(graphs)×RTT to roughly O(graphs/8)×RTT.
const coverageStatsConcurrency = 8

// coverageProbeConcurrency bounds the parallel per-row segment-coverage probes
// (segCoveredFor). On the logged-in cloud path each probe is a REMOTE MANIFEST
// READ against a backend shared by every user, so the wave is bounded rather than
// unbounded — the bound is the backend protection. Its value is the owner's rule:
// "we can do 6 at a time".
//
// IT IS A SECOND NUMBER RATHER THAN A REUSE of coverageStatsConcurrency, which the
// overlay enumeration does reuse. The two waves protect different things: the Stats
// bound limits per-graph COUNT work on the server, while this one limits concurrent
// reads of a shared object store. Tying them together would mean a future tuning of
// either silently retunes the other.
const coverageProbeConcurrency = 6

// collectCoverageRows issues the per-graph Stats(IncludeCoverage:true) walk once
// and returns the shared []CoverageRow that both the markdown table and the JSON
// coverage[] block render from — so the two never drift. Returns nil when the
// stats seam is unavailable (degraded/headless), and callers omit the block.
//
// The enumeration: the default knowledge graph is emitted explicitly via the
// empty-name selector (its empty instance name is dropped by listGraphNamesOfType),
// then every other BUILTIN graph type is enumerated via listGraphNamesOfType +
// graphsel.GraphSelectorFor, then every code base graph's BRANCH GRAPHS are
// enumerated through the same seam with overlay_of set (coverageTargets).
//
// A BRANCH ROW REPORTS ITS OWN COUNTS AND AN EMPTY BAND. Its Stats selector carries
// the composed "base@overlay" key with an empty Branch, which resolves the branch
// graph's OWN rows rather than the base+overlay composite a Repo+Branch selector
// would — so the number is the storage the adjacent delete control reclaims, and it
// does NOT equal what a repo+branch stats query returns. The four honest-band inputs
// are declined for those rows (see the assembly loop).
//
// The per-graph Stats RPCs run CONCURRENTLY (bounded): against a remote server
// each is a network round trip carrying that graph's coverage COUNTs, and a
// sequential walk cost ~8s across ~22 graphs — most of manage(status)'s
// remaining latency after the liveness probes went no-retry. Row order stays
// (knowledge first, then enumeration order) because results land by index, not
// completion order. A failed Stats drops a MANAGED row, same as the sequential
// walk; an unmanaged row survives its failure and renders the not-read shape,
// because dropping it would delete a graph from the inventory this table exists
// to show.
//
// The segCoveredFor probes run CONCURRENTLY TOO, in their own wave bounded at
// coverageProbeConcurrency — "we can do 6 at a time", per owner decision. On the
// logged-in cloud path each probe is a remote manifest read, and THE BOUND is what
// keeps a many-graph status call from bursting concurrent reads at a backend shared
// by every user; that protection no longer comes from running them one at a time.
// They land by index exactly as the Stats results do, so the wave changes the wall
// clock and nothing about the table. The wave SKIPS the rows the assembly loop
// would not have probed — a dropped row (nil stats) and a branch row — so no probe
// is issued for a key the serial walk never asked about.
//
// ONLY THE REMOTE PROBE MOVED. repairVerifiedFor, consumerAgesFor, inWorkingSetFor
// and segmentStalledSinceFor are pure local map reads and stay on the serial
// assembly loop below: they cost nothing measurable, and keeping the assembly walk
// serial is what their seams' own contracts are written against.
func collectCoverageRows(ctx context.Context, deps ClientDeps) []CoverageRow {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil
	}
	sc, ok := gc.(statsRPC)
	if !ok {
		return nil
	}

	targets := coverageTargets(ctx, deps)

	stats := make([]*knowledgev1.GraphStats, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, coverageStatsConcurrency)
	for i, t := range targets {
		// THE STATS RPC IS ISSUED FOR EVERY ROW, MANAGED OR NOT, and that is the
		// ruling rather than a relaxation of it (CEO 2026-08-28, verbatim: "why cant
		// we just do a count and not consider it managed"). What the operative rule
		// forbids is MATERIALIZING a graph nobody asked about, and counting is not
		// that read: a server answers it from counts it already maintains for the
		// graph rather than by opening the graph, and one that genuinely cannot
		// answer without opening it FAILS THE CALL instead of loading — the row below
		// then falls back to the not-read shape. Either way nothing becomes resident.
		// The reads that DO materialize — both segment probes — stay gated on
		// membership in collectSegProbes.
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			resp, err := sc.Stats(ctx, &knowledgev1.StatsRequest{
				Target:          t.target,
				IncludeCoverage: true,
			})
			if err != nil {
				return
			}
			stats[i] = resp.GetGraphStats()
		})
	}
	wg.Wait()

	probes := collectSegProbes(ctx, deps, targets, stats)

	// ONE clock reading for the whole table, so two rows assembled a millisecond apart
	// cannot disagree about whether the same interval has elapsed.
	now := time.Now().UnixNano()
	rows := make([]CoverageRow, 0, len(targets))
	for i, t := range targets {
		if !t.managed {
			// AN UNMANAGED ROW ALWAYS RENDERS, on either arm. Dropping it would silently
			// delete the graph from the inventory manage(status) exists to show — which
			// is why its Stats failure is not treated like a managed row's below.
			//
			// The counts arm is the ordinary one: the backend answered them without
			// materializing the graph. The fallback arm is a backend that could not,
			// which today is a local server whose image predates the durable count
			// record; that row is assembled from the target alone and says its counts
			// were not read. Neither arm probes the segment pool.
			if stats[i] == nil {
				rows = append(rows, newUnmanagedCoverageRow(t))
				continue
			}
			rows = append(rows, newUnmanagedCountedCoverageRow(t, stats[i]))
			continue
		}
		if stats[i] == nil {
			continue
		}
		if t.overlay {
			// A BRANCH ROW DECLINES ALL FOUR HONEST-BAND PROBES, because every one of
			// them keys on a name its authority's key space cannot represent, so each
			// would answer about a DIFFERENT graph:
			//
			//   - The working set normalizes a name by cutting it at the first "@", so
			//     asking about "agent@launch-fixes" answers about "agent". A branch of a
			//     member base would report as maintained, skip the unmanaged arm and land
			//     on a ratio band — narrating an arm that is not running on this graph.
			//   - The segment pool is base-keyed for the same reason: the reconcile walks
			//     working-set members, so nothing publishes under a branch key. Probing
			//     one would also make a status READ lazily construct a manager and its
			//     directory for an instance that does not exist.
			//   - The two CONSUMER POSITIONS are read off that same base-keyed segment
			//     pool, so they decline for the identical reason. They render "never",
			//     which is the true statement here: nothing publishes segments under a
			//     branch key, so no consumer of this graph has ever held a position.
			//     Their neighbors in that cell — the retained-erasure count and the
			//     newest-erasure age — are NOT declined, because those come from this
			//     graph's own server-side stats and a branch graph really can carry a
			//     journal backlog.
			//
			// The zero values render the no-segments dash, which states the only true
			// thing available: this graph has no segment pool of its own.
			rows = append(rows, newCoverageRow(t.label, stats[i], 0, 0, false, false, false, false, 0, 0, 0))
			continue
		}
		verified := repairVerifiedFor(deps, t.gt, t.name, now)
		rebuildAge, mergeAge := consumerAgesFor(deps, t.gt, t.name, now)
		row := newCoverageRow(t.label, stats[i], probes[i].covered, probes[i].liveResident,
			probes[i].hasSeg, verified, inWorkingSetFor(deps, t.gt, t.name), poolEvictedFor(deps, t.gt, t.name),
			segmentStalledSinceFor(deps, t.gt, t.name), rebuildAge, mergeAge)
		// Assigned AFTER the constructor returns rather than threaded through it:
		// newCoverageRow already takes eleven positional parameters and three of its
		// four callers cannot supply a twelfth.
		row.Degraded = probes[i].degraded
		rows = append(rows, row)
	}
	return rows
}

// segProbe is one row's segment-coverage answer, carried from the concurrent probe
// wave back to the assembly loop. A row the wave SKIPPED keeps the zero value,
// which is the same (0, 0, false) triple segCoveredFor returns for a graph with no
// rebuildable segments — so a skipped row and a declining one render identically,
// as they did when the probe ran inline.
type segProbe struct {
	covered      int
	liveResident int
	hasSeg       bool
	// degraded is read on the SAME bounded worker as the two counts above, so the
	// serial assembly loop stays free of it.
	degraded map[string]int
}

// collectSegProbes runs the per-row segment-coverage probes CONCURRENTLY, bounded
// at coverageProbeConcurrency, and returns them BY TARGET INDEX. Indexing is what
// keeps the table deterministic: the assembly loop reads probes[i] beside stats[i],
// so a probe that finishes last still lands on its own row.
//
// IT DECLINES THE ROWS WHOSE POOL MUST NOT BE TOUCHED, and that is a correctness
// requirement rather than an optimization. An UNMANAGED row declines the probe
// because both of segCoveredFor's reads interact — one imports the graph's whole L2
// segment pool, the other constructs its engine — and nothing may interact with a
// graph no direct MCP call has admitted. THIS IS THE ONE GATE MEMBERSHIP STILL
// GUARDS: that row's COUNTS are read, off a Stats RPC that materializes nothing, so
// its stats slot is now populated and the nil-stats clause below no longer stands in
// for this one. A row whose Stats RPC failed has nothing to probe against, and a
// BRANCH row declines the probe because the segment key space is base-keyed and
// cannot represent a branch graph. Probing either would ask the seam about a
// different graph than the row reports — and the real reader lazily CONSTRUCTS a
// manager and its cache directory for whatever key it is handed, so a status READ
// would create state for an instance that does not exist.
//
// It mirrors the Stats fan-out's semaphore idiom above rather than introducing a
// second one: goroutine per target, permit taken inside, result written to its own
// slot. A failed probe leaves the zero triple, exactly as the inline call did.
func collectSegProbes(
	ctx context.Context, deps ClientDeps, targets []coverageTarget, stats []*knowledgev1.GraphStats,
) []segProbe {
	probes := make([]segProbe, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, coverageProbeConcurrency)
	for i, t := range targets {
		if !t.managed || stats[i] == nil || t.overlay {
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			covered, liveResident, hasSeg := segCoveredFor(ctx, deps, t.gt, t.name)
			probes[i] = segProbe{
				covered: covered, liveResident: liveResident, hasSeg: hasSeg,
				degraded: bm25DegradeFor(deps, t.gt, t.name),
			}
		})
	}
	wg.Wait()
	return probes
}

// bm25DegradeFor reads one graph's accumulated BM25 drop census through the same
// nil-safe seam segCoveredFor uses. An unwired seam reports no drops, which is the
// only honest answer available to a reader with no way to look.
func bm25DegradeFor(deps ClientDeps, gt kgtypes.GraphType, name string) map[string]int {
	sr := deps.SegmentCoverage()
	if sr == nil {
		return nil
	}
	return sr.BM25DegradeCounts(gt, name)
}

// repairVerifiedFor answers whether the backstop has verified this graph's band,
// through the same nil-safe seam segCoveredFor uses. An unwired seam reports
// unverified, which renders as cache-aged — honest for a reader with no way to know.
//
// The FORMULA it defers to (repairVerifiedFrom) deliberately stays in the sibling
// file beside the disposition it feeds: this function is the seam read, that one is
// the policy.
func repairVerifiedFor(deps ClientDeps, gt kgtypes.GraphType, name string, nowNanos int64) bool {
	sr := deps.SegmentCoverage()
	if sr == nil {
		return false
	}
	st, ok := sr.RepairVerification(gt, name)
	return repairVerifiedFrom(st, ok, nowNanos)
}

// segmentStallReader and workingSetReader are the two OPTIONAL deps capabilities the
// coverage table reads its honest-band inputs through: since when a graph's coverage
// stopped being able to recover, and whether this client maintains the graph at all.
//
// They are TYPE-ASSERTED rather than added to ClientDeps for the reason the
// collectRuntimeProvider seam states (collect_detach.go): a required method would
// have to be implemented by every fake that already implements SegmentCoverage() —
// twenty-five of them — none of which runs a working set or a heal breaker. A deps
// satisfying neither reports 0 and false, which reproduces the pre-existing bands
// exactly: not stalled, and... see inWorkingSetFor for why false is the safe default
// here rather than the alarming one.
type segmentStallReader interface {
	SegmentStalledSince(gt kgtypes.GraphType, name string) int64
}

type workingSetReader interface {
	InWorkingSet(gt kgtypes.GraphType, name string) bool
}

// segmentStalledSinceFor reads the stall stamp through the optional seam. An
// unwired deps reports 0 — not stalled — which is the honest answer for a client
// with no heal breaker and no publish gate to have given up.
func segmentStalledSinceFor(deps ClientDeps, gt kgtypes.GraphType, name string) int64 {
	r, ok := deps.(segmentStallReader)
	if !ok {
		return 0
	}
	return r.SegmentStalledSince(gt, name)
}

// inWorkingSetFor reads working-set membership through the optional seam.
//
// AN UNWIRED DEPS REPORTS TRUE, which is the one place these two helpers do NOT
// mirror each other. The working set's own default is deny — every consumer treats a
// nil set as empty so a missed wiring under-admits — but that default belongs to the
// arms that ACT on a graph, where doing nothing is the safe failure. This is a
// column, and a fixture that wires no working set would otherwise render every graph
// in the account "unmanaged", replacing every real band with a claim about a
// mechanism that is not present. Reporting membership keeps the pre-existing bands
// for a deps that cannot answer, and only a client that genuinely runs a working set
// can put a row in the unmanaged band.
func inWorkingSetFor(deps ClientDeps, gt kgtypes.GraphType, name string) bool {
	r, ok := deps.(workingSetReader)
	if !ok {
		return true
	}
	return r.InWorkingSet(gt, name)
}
