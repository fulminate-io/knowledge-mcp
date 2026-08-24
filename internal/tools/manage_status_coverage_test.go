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
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// coverageFake is a statsRPC that (a) records every StatsRequest it receives so
// the test can assert IncludeCoverage was set (the T2 gate trigger), (b) serves
// per-graph GraphStats keyed by the resolved instance label, and (c) serves a
// RETURN_MODE_GRAPH_NAMES enumeration — both the per-type BASE enumeration and the
// per-base OVERLAY enumeration a non-empty overlay_of asks for.
//
// The BASE enumeration defaults to a named "code" repo AND a named "practice"
// language (a NON-code embeddable builtin), and an EMPTY name for knowledge
// (mirroring the real drop of empty names) — proving the knowledge row is rendered
// via the explicit empty-name selector, not enumeration, and a non-code embeddable
// graph renders a real segment-coverage cell. Both enumerations are programmable
// per type / per base, so a fixture can seed several code bases carrying DIFFERENT
// backend key forms in a single pass.
type coverageFake struct {
	mu         sync.Mutex // Stats is called concurrently by the coverage fan-out
	reqs       []*knowledgev1.StatsRequest
	statsByKey map[string]*knowledgev1.GraphStats
	// baseNamesByType programs the per-type BASE list. A type that is absent, or
	// whose entry is nil, falls back to the defaults in baseNamesFor — which is what
	// keeps every fixture that programs only statsByKey serving what it always did.
	baseNamesByType map[string][]string
	// overlayKeysByBase programs the OVERLAY keys of one base, IN THE BACKEND FORM
	// the fixture is exercising: cloud reports the full "base@overlay" key, OSS
	// reports the bare overlay name. A base with no entry has no overlays.
	overlayKeysByBase map[string][]string
	// execReqs records every ExecuteRequest the walk issued, so a test can assert
	// WHICH graph each enumeration asked about rather than only what came back.
	execReqs []*knowledgev1.ExecuteRequest
}

func (f *coverageFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	// The request log and both programmable maps are touched under the mutex: the
	// coverage walk enumerates types and overlays concurrently.
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execReqs = append(f.execReqs, req)
	q := req.GetQuery()
	if q == nil || q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if base := q.GetOverlayOf(); base != "" {
		return f.graphNames(f.overlayKeysByBase[base]), nil
	}
	return f.graphNames(f.baseNamesFor(req.GetTarget().GetGraph())), nil
}

// baseNamesFor resolves the BASE list for one graph type: the programmed entry
// when the fixture seeded one, otherwise the historical literals — a named repo
// for code, a named language for practice, nothing for every other type (incl the
// default knowledge graph, whose empty name listGraphNamesOfType drops).
//
// An entry present but NIL falls back too, so only a fixture that explicitly seeds
// a non-nil empty slice claims a type has no graphs.
func (f *coverageFake) baseNamesFor(graphType string) []string {
	if names, ok := f.baseNamesByType[graphType]; ok && names != nil {
		return names
	}
	switch graphType {
	case "code":
		return []string{"myrepo"}
	case "practice":
		return []string{"go"}
	}
	return nil
}

// graphNames projects a name list into the GraphInfo carrier, dropping empty names
// as the real enumeration does.
func (f *coverageFake) graphNames(names []string) *knowledgev1.ExecuteResponse {
	var infos []*knowledgev1.GraphInfo
	for _, n := range names {
		if n != "" {
			infos = append(infos, &knowledgev1.GraphInfo{Name: n})
		}
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}
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

// enumerateGraphNames drives coverageFake.Execute the way listGraphNamesOfType and
// listOverlayKeysOfBase do — a RETURN_MODE_GRAPH_NAMES plan against one graph type,
// with overlayOf empty for the base enumeration — and returns the served names.
func enumerateGraphNames(t *testing.T, f *coverageFake, graphType, overlayOf string) []string {
	t.Helper()
	resp, err := f.Execute(context.Background(), &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES,
			OverlayOf:  overlayOf,
		}},
		Target: &knowledgev1.GraphSelector{Graph: graphType},
	})
	require.NoError(t, err)
	names := make([]string, 0, len(resp.GetGraphNames()))
	for _, gi := range resp.GetGraphNames() {
		names = append(names, gi.GetName())
	}
	return names
}

// TestCoverageFake_OverlayKeyForms is the fake's own selftest, and it exists so a
// fixture that silently ignored either programmable map could not make the coverage
// enumeration tests vacuously green.
//
// The fourth leg is the one that protects code this test does not itself call: every
// pre-existing fixture in this package programs statsByKey ONLY, so a defaulting rule
// that returned nothing for an unprogrammed type would silently empty out their
// enumerations. Leg 4 pins the defaults those fixtures depend on.
func TestCoverageFake_OverlayKeyForms(t *testing.T) {
	f := &coverageFake{
		baseNamesByType: map[string][]string{"code": {"agent", "knowledge", "myrepo"}},
		overlayKeysByBase: map[string][]string{
			// The CLOUD form: the full composed catalog key.
			"agent": {"agent@launch-fixes"},
			// The OSS form: the bare overlay name, base prefix already stripped.
			"knowledge": {"fix-a"},
		},
	}

	t.Run("programmed_overlays_in_the_programmed_form", func(t *testing.T) {
		assert.Equal(t, []string{"agent@launch-fixes"}, enumerateGraphNames(t, f, "code", "agent"))
		assert.Equal(t, []string{"fix-a"}, enumerateGraphNames(t, f, "code", "knowledge"))
	})

	t.Run("base_without_overlays_serves_none", func(t *testing.T) {
		assert.Empty(t, enumerateGraphNames(t, f, "code", "myrepo"))
	})

	t.Run("empty_overlay_of_serves_the_base_list", func(t *testing.T) {
		assert.Equal(t, []string{"agent", "knowledge", "myrepo"}, enumerateGraphNames(t, f, "code", ""))
	})

	t.Run("unprogrammed_base_list_keeps_the_historical_literals", func(t *testing.T) {
		bare := &coverageFake{}
		assert.Equal(t, []string{"myrepo"}, enumerateGraphNames(t, bare, "code", ""))
		assert.Equal(t, []string{"go"}, enumerateGraphNames(t, bare, "practice", ""))
		assert.Empty(t, enumerateGraphNames(t, bare, "knowledge", ""),
			"the default knowledge graph still enumerates no name, so its row comes from the explicit selector")
	})
}

// coverageSegReader is a SegmentCoverageReader stub: it serves a per-graph covered
// doc count AND live resident doc count keyed by (graphType, name), so the
// renderer's segment-coverage column reads real numbers — shipped covered and live
// resident — for the segment-bearing graphs (knowledge + code).
type coverageSegReader struct {
	coveredByKey  map[string]int
	residentByKey map[string]int
	// rebuildPosByKey / mergePosByKey are this client's two consumer positions,
	// keyed the same way. An ABSENT key is a consumer that has never recorded a
	// position, which the row must render as "never" rather than as an age.
	rebuildPosByKey map[string]int64
	mergePosByKey   map[string]int64
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

// LoadRebuildState / LoadMergeWatermark serve this client's two consumer
// positions. An UNSET key reports zero, which is the "never recorded a position"
// case the row must render as "never" rather than as an age.
func (r *coverageSegReader) LoadRebuildState(
	gt kgtypes.GraphType, name string,
) (int64, []searchengine.ExternalID, error) {
	return r.rebuildPosByKey[r.segKey(gt, name)], nil, nil
}

func (r *coverageSegReader) LoadMergeWatermark(gt kgtypes.GraphType, name string) (int64, error) {
	return r.mergePosByKey[r.segKey(gt, name)], nil
}

// TestSegCoverageDisposition pins every arm of the coverage-band classifier in
// branch order, plus the two boundary cases that close the band and the catcher
// proving it reads the LIVE count rather than the shipped one.
//
// The branch order is the design: arm 2 (residue) must precede arm 6
// (gap-repairing), because when live exceeds embedded the ratio test in arm 6 is
// also true and a band-first classifier would mislabel the residue class.
func TestSegCoverageDisposition(t *testing.T) {
	// row builds a VERIFIED, MAINTAINED row. Both defaults matter: only the last arm
	// consults the backstop, and the unmanaged arm pre-empts every band arm below it,
	// so a row left unverified or left out of the working set would relabel the band
	// cases and each landed subtest would stop measuring the arm it names. The
	// unverified and unmanaged shapes are driven explicitly (below, and in
	// TestSegCoverageDisposition_StuckAndUnmanagedBands).
	row := func(embedded, segCovered, liveResident int, hasSeg bool) CoverageRow {
		return CoverageRow{
			Embedded: embedded, SegCovered: segCovered,
			LiveResident: liveResident, HasSegments: hasSeg,
			RepairVerified: true, InWorkingSet: true,
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

	t.Run("evicted_pool_reads_evicted", func(t *testing.T) {
		// The residency budget dropped this pool from RAM, so LiveResident is 0 by
		// construction — the SAME numbers self_healing above is built from. Without
		// the evicted arm this row would render "self-healing", whose legend promises
		// the reader it resolves within one reconcile interval; nothing will touch
		// this graph until a user searches it.
		r := row(100, 20, 20, true)
		r.Evicted = true
		assert.Equal(t, DispositionEvicted, segCoverageDisposition(r))
	})

	t.Run("evicted_false_same_numbers_reads_self_healing", func(t *testing.T) {
		// THE PAIR, and the reason the case above is a measurement: identical numbers
		// with the flag cleared must still reach the ratio arm. Without it an arm
		// hard-wired to return evicted would be green.
		r := row(100, 20, 20, true)
		r.Evicted = false
		assert.Equal(t, DispositionSelfHealing, segCoverageDisposition(r))
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
