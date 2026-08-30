// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_probe_test.go — the two properties of the coverage
// table's segment-coverage probe wave that no rendering test can see: how many
// probes may be in flight at once, and that a row's numbers land by INDEX rather
// than by completion order.
//
// They are separated from the rendering tests because both need a reader that
// MANIPULATES TIME — one parks probes open to make the wave's width observable,
// the other randomizes completion order to make an order-dependent assembly fail.
// A reader that answers instantly makes both properties invisible.
package tools

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// recordProbe appends one probed key to coverageSegReader's log under its lock.
// Every seam method that records a probe goes through it, so no caller can
// reintroduce a bare append.
//
// IT LIVES HERE, beside the wave whose concurrency requires it, rather than beside
// the stub it belongs to: several rows are inside those seam methods at once now,
// which an unguarded append turns into a data race the -race build fails on.
func (r *coverageSegReader) recordProbe(key string) {
	r.probedMu.Lock()
	r.probed = append(r.probed, key)
	r.probedMu.Unlock()
}

// probedKeys returns a COPY of the recorded keys, so an assertion iterating them
// cannot race a probe still in flight. Tests read probes through this rather than
// touching the slice directly.
func (r *coverageSegReader) probedKeys() []string {
	r.probedMu.Lock()
	defer r.probedMu.Unlock()
	out := make([]string, len(r.probed))
	copy(out, r.probed)
	return out
}

// probeSegKey spells the (graphType, name) key the coverage probe is addressed
// by, in the same form coverageSegReader.segKey uses — so a fixture here and a
// fixture there name the same graph the same way.
func probeSegKey(gt kgtypes.GraphType, name string) string {
	if name == "" {
		return string(gt)
	}
	return string(gt) + "/" + name
}

// probeCodeBases builds n code base-graph names in a fixed, sortable order, so a
// fixture can ask for a wave WIDER than the concurrency bound without hand-listing
// repos. Two-digit padding keeps the enumeration order and the lexical order the
// same, which is what lets the determinism test state its expectation as a literal.
func probeCodeBases(n int) []string {
	names := make([]string, 0, n)
	for i := range n {
		names = append(names, fmt.Sprintf("repo%02d", i))
	}
	return names
}

// gatedSegReader is a SegmentCoverageReader that MEASURES the probe wave instead of
// serving numbers quickly: every ShippedSegmentDocCount call registers itself as in
// flight, PARKS until waveSize probes have gathered (or waveTimeout elapses), and
// records the high-water mark of simultaneous probes.
//
// THE PARK IS WHAT MAKES THE READING REAL. Without it a bounded caller and a serial
// caller are indistinguishable to a counter: each probe returns before the next is
// issued, so the high-water mark reads 1 either way. Parking holds every admitted
// probe open until the whole permitted window is visible at once.
//
// THE TIMEOUT IS WHAT KEEPS THE TEST A TEST rather than a deadlock. A serial caller
// never gathers waveSize probes, and even a correctly bounded caller's FINAL wave is
// short when the probe count is not a multiple of the bound. Both must finish and
// report their measurement rather than hang.
type gatedSegReader struct {
	waveSize     int
	waveTimeout  time.Duration
	coveredByKey map[string]int

	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	total       int
	// gathered is closed when waveSize probes are simultaneously parked, releasing
	// them together, and then replaced for the next wave.
	gathered chan struct{}
}

// enter registers one probe as in flight and returns the channel it should park on.
func (g *gatedSegReader) enter() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gathered == nil {
		g.gathered = make(chan struct{})
	}
	g.inFlight++
	g.total++
	if g.inFlight > g.maxInFlight {
		g.maxInFlight = g.inFlight
	}
	ch := g.gathered
	if g.inFlight >= g.waveSize {
		close(ch)
		g.gathered = make(chan struct{})
	}
	return ch
}

func (g *gatedSegReader) leave() {
	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
}

func (g *gatedSegReader) peak() (maxInFlight, total int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxInFlight, g.total
}

func (g *gatedSegReader) ShippedSegmentDocCount(
	_ context.Context, gt kgtypes.GraphType, name string,
) (int, error) {
	ch := g.enter()
	defer g.leave()
	select {
	case <-ch:
	case <-time.After(g.waveTimeout):
	}
	return g.coveredByKey[probeSegKey(gt, name)], nil
}

// The remaining seam methods answer from the same read-only map or from zero: this
// reader exists to time the shipped probe, not to serve a band.
func (g *gatedSegReader) ResidentDocCount(gt kgtypes.GraphType, name string) int {
	return g.coveredByKey[probeSegKey(gt, name)]
}

func (g *gatedSegReader) LiveResidentDocCount(gt kgtypes.GraphType, name string) int {
	return g.coveredByKey[probeSegKey(gt, name)]
}

func (g *gatedSegReader) RepairVerification(kgtypes.GraphType, string) (RepairVerification, bool) {
	return RepairVerification{}, false
}

func (g *gatedSegReader) LoadRebuildState(
	kgtypes.GraphType, string,
) (int64, []searchengine.ExternalID, error) {
	return 0, nil, nil
}

func (g *gatedSegReader) LoadMergeWatermark(kgtypes.GraphType, string) (int64, error) {
	return 0, nil
}

// wantProbeConcurrency is the decided bound, written here as a LITERAL rather
// than read off coverageProbeConcurrency. The number is an owner decision — "we
// can do 6 at a time" — so the test has to carry its own copy of it: asserting the
// observed width against the constant that produced it would let a future edit
// raise both together and keep the test green, which is the one change this test
// exists to stop.
const wantProbeConcurrency = 6

// TestCollectCoverageRows_ProbeWaveIsBounded is the criterion for the owner
// decision "we can do 6 at a time": the per-row segment-coverage probes run
// CONCURRENTLY, and never more than six of them at once.
//
// EVERY HALF IS LOAD-BEARING AND EACH CATCHES A DIFFERENT REGRESSION:
//   - the constant pin fails if the decided number is changed without the decision
//     being revisited.
//   - maxInFlight > 1 fails against the one-at-a-time assembly walk this replaced —
//     the state the decision overturned.
//   - maxInFlight <= 6 fails against an UNBOUNDED fan-out, which is the failure the
//     bound exists to prevent (bursting a whole many-graph inventory's worth of
//     manifest reads at a shared backend).
//
// The fixture is deliberately WIDER than the bound — 24 probing rows against a
// bound of 6 — so a bound quietly raised to the row count would be indistinguishable
// from no bound at all, and this test would still catch it.
func TestCollectCoverageRows_ProbeWaveIsBounded(t *testing.T) {
	require.Equal(t, wantProbeConcurrency, coverageProbeConcurrency,
		"the probe wave's bound is an owner decision fixed at %d at a time; changing "+
			"it is a decision to re-take, not a constant to retune", wantProbeConcurrency)

	// 23 code bases + the default knowledge graph = 24 rows that issue a probe.
	// practice is emptied explicitly so the fixture's probe count is exactly the
	// number this test reasons about rather than the fake's historical defaults.
	bases := probeCodeBases(23)
	fake := &coverageFake{baseNamesByType: map[string][]string{
		"code":     bases,
		"practice": {},
	}}
	seg := &gatedSegReader{waveSize: wantProbeConcurrency, waveTimeout: 2 * time.Second}

	rows := collectCoverageRows(context.Background(), &coverageDeps{gc: fake, segCov: seg})
	require.Len(t, rows, 24, "every programmed graph must produce a row")

	maxInFlight, total := seg.peak()
	require.Equal(t, 24, total,
		"every row must have issued exactly one shipped probe — a lower count means the "+
			"wave measured here is not the wave the table is built from")
	assert.Greater(t, maxInFlight, 1,
		"the probes must run concurrently: a high-water mark of 1 is the one-at-a-time "+
			"walk the bounded wave replaced")
	assert.LessOrEqual(t, maxInFlight, wantProbeConcurrency,
		"the probes must stay bounded: more than %d in flight bursts concurrent manifest "+
			"reads at a backend shared by every user", wantProbeConcurrency)
	assert.Equal(t, wantProbeConcurrency, maxInFlight,
		"a 24-wide probe set against a bound of %d must actually fill the window",
		wantProbeConcurrency)
}

// randomDelaySegReader answers each probe after a RANDOM delay, so completion
// order is uncorrelated with issue order. It serves a DISTINCT covered count per
// graph, which is what lets the test assert that each row got ITS OWN number and
// not merely that the labels are ordered.
type randomDelaySegReader struct{ coveredByKey map[string]int }

func (r *randomDelaySegReader) ShippedSegmentDocCount(
	_ context.Context, gt kgtypes.GraphType, name string,
) (int, error) {
	time.Sleep(time.Duration(rand.IntN(8)) * time.Millisecond)
	return r.coveredByKey[probeSegKey(gt, name)], nil
}

func (r *randomDelaySegReader) ResidentDocCount(gt kgtypes.GraphType, name string) int {
	return r.coveredByKey[probeSegKey(gt, name)]
}

func (r *randomDelaySegReader) LiveResidentDocCount(gt kgtypes.GraphType, name string) int {
	return r.coveredByKey[probeSegKey(gt, name)]
}

func (r *randomDelaySegReader) RepairVerification(kgtypes.GraphType, string) (RepairVerification, bool) {
	return RepairVerification{}, false
}

func (r *randomDelaySegReader) LoadRebuildState(
	kgtypes.GraphType, string,
) (int64, []searchengine.ExternalID, error) {
	return 0, nil, nil
}

func (r *randomDelaySegReader) LoadMergeWatermark(kgtypes.GraphType, string) (int64, error) {
	return 0, nil
}

// TestCollectCoverageRows_RowsLandByIndex is the determinism criterion for the
// concurrent probe wave: rows come back in TARGET order, and each row carries the
// probe result for ITS OWN graph, however the probes interleave.
//
// The reader randomizes every probe's delay, so an assembly that appended results
// in completion order — the natural mistake when a serial loop becomes a fan-out —
// produces a different permutation on almost every run.
//
// ASSERTING THE COUNTS, NOT JUST THE LABELS, is what makes this a determinism test
// rather than an enumeration test. The labels come from coverageTargets and would
// stay ordered even if every probe result were shuffled between rows; the per-graph
// covered counts are the only value that travels through the concurrent section, so
// they are the ones that can arrive mismatched.
func TestCollectCoverageRows_RowsLandByIndex(t *testing.T) {
	bases := probeCodeBases(23)
	fake := &coverageFake{baseNamesByType: map[string][]string{
		"code":     bases,
		"practice": {},
	}}

	// A DISTINCT covered count per graph, so a row holding another row's result is
	// detectable rather than coincidentally equal.
	covered := map[string]int{probeSegKey(kgtypes.GraphKnowledge, "default"): 1000}
	wantLabels := []string{"knowledge"}
	wantCovered := []int{1000}
	for i, base := range bases {
		covered[probeSegKey(kgtypes.GraphCode, base)] = 2000 + i
		wantLabels = append(wantLabels, "code/"+base)
		wantCovered = append(wantCovered, 2000+i)
	}

	// Repeated because a mis-ordered assembly is a RACE, not a constant: one pass
	// could land in target order by luck, twelve in a row could not.
	for attempt := range 12 {
		rows := collectCoverageRows(context.Background(),
			&coverageDeps{gc: fake, segCov: &randomDelaySegReader{coveredByKey: covered}})
		require.Len(t, rows, len(wantLabels), "attempt %d", attempt)

		gotLabels := make([]string, 0, len(rows))
		gotCovered := make([]int, 0, len(rows))
		for _, row := range rows {
			gotLabels = append(gotLabels, row.Graph)
			gotCovered = append(gotCovered, row.SegCovered)
		}
		require.Equal(t, wantLabels, gotLabels,
			"attempt %d: rows must be in target order, not probe-completion order", attempt)
		require.Equal(t, wantCovered, gotCovered,
			"attempt %d: each row must carry its OWN graph's probe result", attempt)
	}
}

// TestCollectCoverageRows_OverlayRowsIssueNoProbe is the guard on the concurrent
// wave's SKIP set. A branch row declines the segment probe entirely (its key space
// cannot represent a branch graph), and so does a row whose Stats RPC failed — that
// row is dropped. Moving the probes off the assembly loop moved that skip decision
// with them, so it is asserted where it now lives: at the probe wave.
//
// Without this, a wave that probed every target and let the assembly loop discard
// the extra results would pass every other test in this file while making a status
// READ lazily construct a manager and an L2 directory for graphs that do not exist.
func TestCollectCoverageRows_OverlayRowsIssueNoProbe(t *testing.T) {
	fake := &coverageFake{
		baseNamesByType:   map[string][]string{"code": {"repo00"}, "practice": {}},
		overlayKeysByBase: map[string][]string{"repo00": {"repo00@branch-a", "repo00@branch-b"}},
	}
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{"knowledge/default": 5, "code/repo00": 7},
		residentByKey: map[string]int{"knowledge/default": 5, "code/repo00": 7},
	}

	rows := collectCoverageRows(context.Background(), &coverageDeps{gc: fake, segCov: seg})

	// The known positive: the two non-overlay rows DID probe, so an empty `probed`
	// cannot be what makes the absence assertions below pass.
	require.Len(t, rows, 4, "two base rows plus two branch rows")
	assert.Contains(t, seg.probedKeys(), "knowledge/default")
	assert.Contains(t, seg.probedKeys(), "code/repo00")

	for _, key := range seg.probedKeys() {
		assert.NotContains(t, key, "@",
			"a branch row must issue no segment probe: its key space is base-keyed and "+
				"cannot represent a branch graph, so probing one asks about a different graph")
	}
}
