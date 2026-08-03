// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// coverageFake is a statsRPC that (a) records every StatsRequest it receives so
// the test can assert IncludeCoverage was set (the T2 gate trigger), (b) serves
// per-graph GraphStats keyed by the resolved instance label, and (c) serves a
// RETURN_MODE_GRAPH_NAMES enumeration that returns a named "code" repo AND a named
// "practice" language (a NON-code embeddable builtin), and an
// EMPTY name for knowledge (mirroring the real drop of empty names) — proving the
// knowledge row is rendered via the explicit empty-name selector, not enumeration,
// and a non-code embeddable graph now renders a real segment-coverage cell.
type coverageFake struct {
	mu         sync.Mutex // Stats is called concurrently by the coverage fan-out
	reqs       []*knowledgev1.StatsRequest
	statsByKey map[string]*knowledgev1.GraphStats
}

func (f *coverageFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil || q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// The code graph reports a named repo and the practice graph a named language (a
	// NON-code embeddable builtin renderLLMCoverage now enumerates); everything else
	// (incl the default knowledge graph) enumerates empty — which listGraphNamesOfType
	// drops.
	switch req.GetTarget().GetGraph() {
	case "code":
		return &knowledgev1.ExecuteResponse{GraphNames: []*knowledgev1.GraphInfo{{Name: "myrepo"}}}, nil
	case "practice":
		return &knowledgev1.ExecuteResponse{GraphNames: []*knowledgev1.GraphInfo{{Name: "go"}}}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

func (f *coverageFake) Stats(_ context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	// Resolve the row label the renderer would use: empty Graph → knowledge; a code
	// graph carries the repo field, a practice graph the language field.
	sel := req.GetTarget()
	key := "knowledge"
	switch sel.GetGraph() {
	case "code":
		key = "code/" + sel.GetRepo()
	case "practice":
		key = "practice/" + sel.GetLanguage()
	}
	st := f.statsByKey[key]
	if st == nil {
		st = &knowledgev1.GraphStats{}
	}
	return &knowledgev1.StatsResponse{GraphStats: st}, nil
}

// coverageSegReader is a SegmentCoverageReader stub: it serves a per-graph covered
// doc count AND live resident doc count keyed by (graphType, name), so the
// renderer's segment-coverage column reads real numbers — shipped covered and live
// resident — for the segment-bearing graphs (knowledge + code).
type coverageSegReader struct {
	coveredByKey  map[string]int
	residentByKey map[string]int
	// liveByKey is the DISTINCT live-searchable count, programmable independently
	// of residentByKey so a test can make the summed and live readings differ —
	// which is the only way to prove which one the column renders.
	liveByKey map[string]int
	// probed records every (graphType, name) key the renderer asked about, so a
	// test can assert WHICH instance key the probe used — not just what it got
	// back. Appended without a lock because segCoveredFor runs on
	// collectCoverageRows' serial assembly loop (it is a local read, not one of
	// the concurrently fanned-out Stats RPCs).
	probed []string
	// verificationByKey is the per-graph backstop record the coverage column's
	// verified formula reads. An absent key reports ok=false — this process never
	// loaded that graph's record — which the column renders as cache-aged.
	verificationByKey map[string]RepairVerification
}

func (r *coverageSegReader) segKey(gt kgtypes.GraphType, name string) string {
	key := string(gt)
	if name != "" {
		key += "/" + name
	}
	return key
}

func (r *coverageSegReader) RepairVerification(
	gt kgtypes.GraphType, name string,
) (RepairVerification, bool) {
	st, ok := r.verificationByKey[r.segKey(gt, name)]
	return st, ok
}

func (r *coverageSegReader) ShippedSegmentDocCount(
	_ context.Context, gt kgtypes.GraphType, name string,
) (int, bool, error) {
	key := r.segKey(gt, name)
	r.probed = append(r.probed, key)
	return r.coveredByKey[key], false, nil
}

func (r *coverageSegReader) ResidentDocCount(gt kgtypes.GraphType, name string) int {
	key := r.segKey(gt, name)
	r.probed = append(r.probed, key)
	return r.residentByKey[key]
}

func (r *coverageSegReader) LiveResidentDocCount(gt kgtypes.GraphType, name string) int {
	key := r.segKey(gt, name)
	r.probed = append(r.probed, key)
	// Fall back to the summed map when a fixture programs only that one, so the
	// pre-existing tests keep their meaning.
	if v, ok := r.liveByKey[key]; ok {
		return v
	}
	return r.residentByKey[key]
}

// coverageDeps is the minimal ClientDeps whose GraphCaller is the coverageFake and
// whose SegmentCoverage seam is an optional coverageSegReader stub (nil when the
// test does not exercise the segment column).
type coverageDeps struct {
	gc     GraphCaller
	segCov SegmentCoverageReader
}

func (d *coverageDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d *coverageDeps) Sink() collector.Sink                         { return nil }
func (d *coverageDeps) RootDir() string                              { return "" }
func (d *coverageDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
func (d *coverageDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d *coverageDeps) WorkerReady() bool                            { return true }
func (d *coverageDeps) PropReady() bool                              { return true }
func (d *coverageDeps) PipelineReady() bool                          { return true }
func (d *coverageDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d *coverageDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d *coverageDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d *coverageDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *coverageDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *coverageDeps) BackendResolver() BackendResolver             { return nil }
func (d *coverageDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *coverageDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *coverageDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *coverageDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *coverageDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *coverageDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d *coverageDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d *coverageDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d *coverageDeps) SegmentCoverage() SegmentCoverageReader   { return d.segCov }
func (d *coverageDeps) PipelineScanner() PipelineScanner         { return nil }

func (d *coverageDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d *coverageDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d *coverageDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d *coverageDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *coverageDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *coverageDeps) TensionsProvider() TensionsProvider   { return nil }

// TestSegCoverageDisposition pins every arm of the coverage-band classifier in
// branch order, plus the two boundary cases that close the band and the catcher
// proving it reads the LIVE count rather than the shipped one.
//
// The branch order is the design: arm 2 (residue) must precede arm 6
// (gap-repairing), because when live exceeds embedded the ratio test in arm 6 is
// also true and a band-first classifier would mislabel the residue class.
func TestSegCoverageDisposition(t *testing.T) {
	// row builds a VERIFIED row. The default matters: only the last arm consults the
	// backstop, so leaving these unverified would relabel the band cases and each
	// landed subtest would stop measuring the arm it names. The unverified shapes are
	// driven explicitly below.
	row := func(embedded, segCovered, liveResident int, hasSeg bool) CoverageRow {
		return CoverageRow{
			Embedded: embedded, SegCovered: segCovered,
			LiveResident: liveResident, HasSegments: hasSeg,
			RepairVerified: true,
		}
	}
	unverified := func(r CoverageRow) CoverageRow {
		r.RepairVerified = false
		return r
	}

	t.Run("no_segments", func(t *testing.T) {
		assert.Equal(t, DispositionNoSegments, segCoverageDisposition(row(100, 0, 0, false)))
	})

	t.Run("residue", func(t *testing.T) {
		// Live exceeds embedded — the hard-delete residue class. Note the ratio
		// test in the gap-repairing arm is ALSO true here, so this passing proves
		// the residue arm runs first.
		assert.Equal(t, DispositionResidue, segCoverageDisposition(row(100, 100, 130, true)))
	})

	t.Run("converged", func(t *testing.T) {
		assert.Equal(t, DispositionConverged, segCoverageDisposition(row(100, 100, 100, true)))
	})

	t.Run("below_floor", func(t *testing.T) {
		// Under the floor the ratio arm is disarmed entirely, even though 10 is
		// far below half of 63.
		assert.Equal(t, DispositionBelowFloor, segCoverageDisposition(row(63, 10, 10, true)))
	})

	t.Run("self_healing", func(t *testing.T) {
		assert.Equal(t, DispositionSelfHealing, segCoverageDisposition(row(100, 20, 20, true)))
	})

	t.Run("gap_repairing", func(t *testing.T) {
		// In the band: at or above half, below converged. This is the state the
		// repair arm services.
		assert.Equal(t, DispositionGapRepairing, segCoverageDisposition(row(100, 99, 99, true)))
	})

	t.Run("floor_boundary_exact", func(t *testing.T) {
		// Embedded EXACTLY at the floor must clear it (the arm tests <, not <=), so
		// the ratio arms below are reached rather than short-circuited.
		assert.Equal(t, DispositionSelfHealing,
			segCoverageDisposition(row(SegmentCoverageFloor, 1, 1, true)))
	})

	t.Run("ratio_boundary_exact", func(t *testing.T) {
		// Live EXACTLY at the threshold is NOT below it, so the row lands in the
		// band rather than in self-healing.
		const embedded = 100
		exactlyHalf := int(CoverageRatioThreshold * float64(embedded))
		assert.Equal(t, DispositionGapRepairing,
			segCoverageDisposition(row(embedded, exactlyHalf, exactlyHalf, true)))
	})

	t.Run("cache_aged_unverified", func(t *testing.T) {
		// The same numbers as gap_repairing, with the backstop's verification withheld.
		assert.Equal(t, DispositionCacheAged,
			segCoverageDisposition(unverified(row(100, 99, 99, true))))
	})

	t.Run("verified_gap_repairing", func(t *testing.T) {
		// The PAIR for the case above: identical numbers, verified. Without it the new
		// arm is satisfiable by returning cache-aged unconditionally, which would erase
		// the distinction the column exists for.
		assert.Equal(t, DispositionGapRepairing, segCoverageDisposition(row(100, 99, 99, true)))
	})

	t.Run("seeded_not_scanned", func(t *testing.T) {
		// THE SEED-TO-COLUMN CATCHER, and it drives the FORMULA rather than the boolean
		// — that is the whole point of it being a separate case. A record that is
		// converged and fresh but NOT scanned is exactly what the backstop's
		// declined-graph seed writes, and a formula reading only Converged and freshness
		// passes both subtests above while failing only this one.
		now := time.Now().UnixNano()
		seeded := RepairVerification{Converged: true, Scanned: false, VerifiedAtNanos: now}
		assert.False(t, repairVerifiedFrom(seeded, true, now),
			"a seeded record records a DECLINE, not an examination")

		r := row(100, 99, 99, true)
		r.RepairVerified = repairVerifiedFrom(seeded, true, now)
		assert.Equal(t, DispositionCacheAged, segCoverageDisposition(r),
			"so the row reads cache-aged rather than claiming a verification nothing performed")

		// KNOWN POSITIVE on the same formula: the calibration's record, which IS scanned,
		// verifies — otherwise this subtest would pass against a formula stuck at false.
		scanned := RepairVerification{Converged: true, Scanned: true, VerifiedAtNanos: now}
		assert.True(t, repairVerifiedFrom(scanned, true, now))
	})

	t.Run("unverified_upper_arms", func(t *testing.T) {
		// The catcher for a verified check hoisted ABOVE the band arms, which would
		// relabel every cold row on every boot and make the column useless exactly when
		// an operator looks at it. Each of these is computed from live readings taken
		// this call — nothing about them is cache-aged.
		assert.Equal(t, DispositionResidue, segCoverageDisposition(unverified(row(100, 100, 130, true))))
		assert.Equal(t, DispositionConverged, segCoverageDisposition(unverified(row(100, 100, 100, true))))
		assert.Equal(t, DispositionBelowFloor, segCoverageDisposition(unverified(row(63, 10, 10, true))))
		assert.Equal(t, DispositionNoSegments, segCoverageDisposition(unverified(row(100, 0, 0, false))))
		assert.Equal(t, DispositionSelfHealing, segCoverageDisposition(unverified(row(100, 20, 20, true))))
	})

	t.Run("stale_verification_expires", func(t *testing.T) {
		// The third clause: a verification older than the interval it was good for stops
		// being trusted, which is what keeps a record from vouching for a graph forever.
		now := time.Now().UnixNano()
		stale := RepairVerification{
			Converged: true, Scanned: true,
			VerifiedAtNanos: now - int64(SegmentRepairBackstopInterval) - 1,
		}
		assert.False(t, repairVerifiedFrom(stale, true, now))
		assert.False(t, repairVerifiedFrom(RepairVerification{}, false, now),
			"and a graph this process never loaded is unverified rather than assumed good")
	})

	t.Run("classifies_on_live_not_shipped", func(t *testing.T) {
		// SegCovered and LiveResident disagree ACROSS a band boundary: the shipped
		// figure says converged, the live figure says the graph is only half
		// searchable. The disposition must follow the live figure — a
		// shipped-classifying helper returns converged here.
		assert.Equal(t, DispositionSelfHealing, segCoverageDisposition(row(100, 100, 40, true)))
	})
}

// TestCoverageRowLiveResidentUsesLiveCount proves the column renders the DISTINCT
// LIVE-SEARCHABLE count rather than the summed per-segment residency figure.
//
// The fake is programmed with DIFFERENT concrete values for the two readings —
// summed 200, live 100 — so neither reading alone can satisfy the assertion. A
// fixture deriving both from one field would collapse the distinction under test
// and pass against either implementation.
func TestCoverageRowLiveResidentUsesLiveCount(t *testing.T) {
	// The stub keys on gt/name, which is the key segCoveredFor probes with.
	const key = "knowledge/default"
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{key: 150},
		residentByKey: map[string]int{key: 200},
		liveByKey:     map[string]int{key: 100},
	}
	deps := &coverageDeps{gc: &coverageFake{}, segCov: seg}

	_, live, hasSeg := segCoveredFor(context.Background(), deps, kgtypes.GraphKnowledge, "default")
	require.True(t, hasSeg)
	require.Equal(t, 100, live,
		"the column must render the DISTINCT live-searchable count (100), not the summed residency figure (200)")
	require.NotEqual(t, 200, live, "reading the summed count is the defect this pins")
}

// TestCoverageRowJSONKeysUnchanged pins the WIRE CONTRACT the Daemon Status web
// Coverage card types against: exactly ten snake_case keys, no eleventh.
//
// It asserts SET EQUALITY rather than a count, deliberately — a count of ten is
// satisfied by dropping one key and adding another, which is precisely the shape a
// careless rename produces. The row is populated with non-zero values throughout so
// no key can be omitted by an accidental omitempty.
func TestCoverageRowJSONKeysUnchanged(t *testing.T) {
	row := CoverageRow{
		Graph: "code/knowledge", Total: 10, Summarized: 9, Embedded: 8,
		SegCovered: 7, LiveResident: 6, HasSegments: true,
		SummaryFail: 1, EmbedFail: 2, SegDisposition: DispositionCacheAged,
		// The new field must contribute NO key — it exists to feed the disposition,
		// and a json tag on it would ship an eleventh key to a ten-key consumer.
		RepairVerified: true,
	}

	raw, err := json.Marshal(row)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	got := make([]string, 0, len(decoded))
	for k := range decoded {
		got = append(got, k)
	}
	require.ElementsMatch(t, []string{
		"graph", "total", "summarized", "embedded", "seg_covered",
		"live_resident", "has_segments", "summary_fail", "embed_fail", "seg_disposition",
	}, got, "the ten pinned keys, and no eleventh")
	require.NotContains(t, decoded, "repair_verified",
		"the verified input is json:\"-\" — it must never reach the wire")
}
