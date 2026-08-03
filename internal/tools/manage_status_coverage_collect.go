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
// segCoveredFor stays on the assembly loop: it is a local read, not an RPC.
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
		rows = append(rows, newCoverageRow(t.label, stats[i], segCovered, liveResident, hasSeg, verified))
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
