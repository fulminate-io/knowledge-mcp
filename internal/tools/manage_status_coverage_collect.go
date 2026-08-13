// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_collect.go — the coverage table's ASSEMBLY half: which
// graph instances the table covers, the bounded concurrent Stats fan-out that reads
// their counts, and the per-row backstop-verification lookup.
//
// Split from manage_status_coverage.go for the 500-line cap, along the seam between
// GATHERING the facts and INTERPRETING them. Its sibling keeps the CoverageRow wire
// contract, the disposition vocabulary, the verified formula and the renderers — so
// "what is this row" and "how do we say it" stay together, and only the RPC walk
// lives here. Same package, no signature changed.
package tools

import (
	"context"
	"fmt"
	"sync"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// coverageTarget is one graph instance the coverage table reports on: its row
// label, type/name (for the segment-coverage seam), and the Stats selector.
type coverageTarget struct {
	label  string
	gt     kgtypes.GraphType
	name   string
	target *knowledgev1.GraphSelector
}

// coverageStatsConcurrency bounds the parallel Stats(IncludeCoverage:true)
// fan-out. Each RPC does per-graph COUNT work server-side; the bound keeps a
// many-graph install from dogpiling the server while still collapsing the
// wall-clock from O(graphs)×RTT to roughly O(graphs/8)×RTT.
const coverageStatsConcurrency = 8

// collectCoverageRows issues the per-graph Stats(IncludeCoverage:true) walk once
// and returns the shared []CoverageRow that both the markdown table and the JSON
// coverage[] block render from — so the two never drift. Returns nil when the
// stats seam is unavailable (degraded/headless), and callers omit the block.
//
// The enumeration is identical to the historical renderLLMCoverage walk: the
// default knowledge graph is emitted explicitly via the empty-name selector (its
// empty instance name is dropped by listGraphNamesOfType), then every other
// SyncEligibleGraphType is enumerated via listGraphNamesOfType +
// graphsel.GraphSelectorFor.
//
// The per-graph Stats RPCs run CONCURRENTLY (bounded): against a remote server
// each is a network round trip carrying that graph's coverage COUNTs, and a
// sequential walk cost ~8s across ~22 graphs — most of manage(status)'s
// remaining latency after the liveness probes went no-retry. Row order stays
// (knowledge first, then enumeration order) because results land by index, not
// completion order. A failed Stats drops its row, same as the sequential walk.
// segCoveredFor stays on the assembly loop deliberately: on the logged-in cloud
// path each call is a remote manifest read, and running them sequentially keeps
// a many-graph status call from bursting concurrent reads at a backend shared
// by every user.
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

	// ONE clock reading for the whole table, so two rows assembled a millisecond apart
	// cannot disagree about whether the same interval has elapsed.
	now := time.Now().UnixNano()
	rows := make([]CoverageRow, 0, len(targets))
	for i, t := range targets {
		if stats[i] == nil {
			continue
		}
		segCovered, liveResident, hasSeg := segCoveredFor(ctx, deps, t.gt, t.name)
		verified := repairVerifiedFor(deps, t.gt, t.name, now)
		rows = append(rows, newCoverageRow(t.label, stats[i], segCovered, liveResident,
			hasSeg, verified, inWorkingSetFor(deps, t.gt, t.name), segmentStalledSinceFor(deps, t.gt, t.name)))
	}
	return rows
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

// coverageTargets enumerates every graph instance the coverage table covers, in
// the table's deterministic order: the default knowledge graph first (explicit
// empty-name selector — its empty instance name is dropped by
// listGraphNamesOfType), then every other SyncEligibleGraphType in order, each
// instance in enumeration order. The per-type name enumerations are independent
// RPCs, so they run concurrently; a failed enumeration drops that type's rows,
// same as the historical sequential walk.
func coverageTargets(ctx context.Context, deps ClientDeps) []coverageTarget {
	types := kgtypes.SyncEligibleGraphTypes()
	perType := make([][]string, len(types))
	var wg sync.WaitGroup
	for i, gt := range types {
		if gt == kgtypes.GraphKnowledge {
			// Emitted explicitly below via the empty-name selector; enumerating
			// it again would skip the empty-name default and/or double-count.
			continue
		}
		wg.Go(func() {
			names, err := listGraphNamesOfType(ctx, deps, string(gt))
			if err != nil {
				return
			}
			perType[i] = names
		})
	}
	wg.Wait()

	targets := []coverageTarget{{
		label: "knowledge",
		gt:    kgtypes.GraphKnowledge,
		// The Stats SELECTOR uses the empty instance name (that is the stats wire
		// contract for the default graph), but the segment probe is a different key
		// space: the default knowledge graph's segments live under "default", which
		// the segment reconcile seeds explicitly for this exact reason — the default
		// instance enumerates an empty name that ListGraphNamesOfType drops. Leaving
		// this empty probes a key nothing writes, reporting the primary corpus as
		// uncovered however well covered it is, and makes the reader lazily
		// construct a manager for an instance that does not exist.
		name:   "default",
		target: &knowledgev1.GraphSelector{Graph: ""},
	}}
	for i, gt := range types {
		for _, name := range perType[i] {
			targets = append(targets, coverageTarget{
				label:  fmt.Sprintf("%s/%s", gt, name),
				gt:     gt,
				name:   name,
				target: graphsel.GraphSelectorFor(gt, name, false),
			})
		}
	}
	return targets
}
