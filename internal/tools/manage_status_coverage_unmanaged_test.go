// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_unmanaged_test.go — the ZERO-INTERACTION pin for the
// coverage walk, and the rendering of the rows it declines to read.
//
// IT IS THE SIBLING OF TestManageStatusSweepDoesNotAdmit (bootstrap
// client_workingset_test.go), which pins the FIRST half of the operative rule:
// "manage operations do not count towards interaction", i.e. a status sweep must
// not ADMIT a graph into the working set. That test passing says nothing about
// the second half, which is the half this file pins: a status sweep must not
// INTERACT with a graph outside the working set either. The recorded decision
// (CEO 2026-08-12, verbatim) states both together — "there must not be any
// background process in the client process that requests or interacts with
// graphs in any way unless some kind of mcp query like search, mutate, collect
// has interacted with it directly. manage operations do not count towards
// interaction." — and only the admission half had a regression pin.
//
// WHAT COUNTS AS AN INTERACTION HERE, and why only two of the walk's three
// per-graph calls are counted as one. The rule's subject is MATERIALIZING a graph
// nobody asked about, and the three calls do not share that cost:
//
//   - Stats(IncludeCoverage:true) is a COUNT, and counting is not interacting
//     ("why cant we just do a count and not consider it managed"). A server
//     answers it from counts it already maintains for the graph rather than by
//     opening the graph, and one that cannot answer without opening it fails the
//     call instead — so nothing becomes resident either way. It is issued for
//     every row, and this file's fixtures assert it IS issued rather than that it
//     is not.
//   - ShippedSegmentDocCount — on the daemon this routes to
//     Manager.LoadResidentDocCount, which imports every segment id in the
//     graph's L2 cache. STILL DECLINED, and pinned here.
//   - LiveResidentDocCount / ResidentDocCount — no load, but Manager.managerFor
//     LAZILY CONSTRUCTS the per-graph engine, its cache directory and its branch
//     seed, which is state created for a graph nobody asked about. STILL DECLINED,
//     and pinned here.
//
// WHAT THIS FILE CAN AND CANNOT MEASURE, stated plainly because the honest scope
// is narrower than the rule. The counter is coverageSegReader.probed — the fake's
// own recorder at the two daemon-side seams — so what is pinned here is that no
// segment probe is issued for an unmanaged graph IN THIS PROCESS. Whether the
// SERVER materializes a graph in response to a Stats RPC is not observable from a
// client fake at all; that half is pinned where residency is observable, by
// TestStatsDoesNotMakeAGraphResident (knowledge-server store package), which reads
// the store's own residency probe before and after a Stats call.

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// workingSetCoverageDeps is a coverageDeps that ALSO answers the optional
// workingSetReader capability, which the production *bootstrap.client satisfies
// and no other fake in this package does.
//
// Every other fixture in the package deliberately leaves it unsatisfied, so
// inWorkingSetFor's unwired default (true — "report membership for a deps that
// cannot answer") keeps their bands exactly as they were. This one is the only
// fixture that can put a row in the unmanaged band at all.
type workingSetCoverageDeps struct {
	*coverageDeps
	members map[string]bool
}

func (d *workingSetCoverageDeps) InWorkingSet(gt kgtypes.GraphType, name string) bool {
	return d.members[probeSegKey(gt, name)]
}

// unmanagedFixture builds the shared fixture: two code repos of identical shape,
// one INSIDE the working set and one outside it, plus the default knowledge graph
// (inside). The two code repos are identical in every respect the walk can see
// EXCEPT membership, which is what makes the managed one a valid control for the
// unmanaged one's zeros.
func unmanagedFixture() (*coverageFake, *coverageSegReader, *workingSetCoverageDeps) {
	fake := &coverageFake{
		baseNamesByType: map[string][]string{
			"code":     {"managedrepo", "foreignrepo"},
			"practice": {},
		},
		statsByKey: map[string]*knowledgev1.GraphStats{
			"knowledge":        {NonProxyNodeCount: 10, SummarizedCount: 10, BinaryVectorCount: 10},
			"code/managedrepo": {NonProxyNodeCount: 800, SummarizedCount: 800, BinaryVectorCount: 800},
			"code/foreignrepo": {NonProxyNodeCount: 366842, SummarizedCount: 366842, BinaryVectorCount: 366842},
		},
		fileSizeByName: map[string]int64{"foreignrepo": 102_720_798, "managedrepo": 4_096},
	}
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{"code/managedrepo": 800, "code/foreignrepo": 366842, "knowledge/default": 10},
		residentByKey: map[string]int{"code/managedrepo": 800, "code/foreignrepo": 366842, "knowledge/default": 10},
	}
	deps := &workingSetCoverageDeps{
		coverageDeps: &coverageDeps{gc: fake, segCov: seg},
		members: map[string]bool{
			"code/managedrepo":  true,
			"knowledge/default": true,
		},
	}
	return fake, seg, deps
}

// statsTargetsFor returns the row labels every issued StatsRequest addressed, in
// the coverageFake's own key spelling — so the assertion below counts REQUESTS
// ISSUED rather than rows rendered.
func statsTargetsFor(fake *coverageFake) []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	out := make([]string, 0, len(fake.reqs))
	for _, r := range fake.reqs {
		sel := r.GetTarget()
		switch sel.GetGraph() {
		case "code":
			out = append(out, "code/"+sel.GetRepo())
		case "practice":
			out = append(out, "practice/"+sel.GetLanguage())
		default:
			out = append(out, "knowledge")
		}
	}
	return out
}

func countOf(keys []string, want string) int {
	n := 0
	for _, k := range keys {
		if k == want {
			n++
		}
	}
	return n
}

// TestManageStatusDoesNotMaterializeUnmanagedGraphs is the decision's client-side
// pin, and it measures MATERIALIZATION rather than traffic.
//
// It drives BOTH status render paths — the markdown coverage table and the
// format:json coverage[] block — against one fixture holding a non-working-set
// graph, and asserts that the two reads which materialize something in this
// process are not issued for it, WHILE the read that materializes nothing is.
//
// THE POSITIVE STATS ASSERTION IS PART OF THE PIN, NOT A CONTROL FOR IT. An
// earlier revision asserted the Stats RPC was NOT issued either, which cost the
// row every count it had and shipped four zeros to a JSON consumer for a
// 366k-node graph. Counting is not interacting; loading is. So the Stats RPC
// being ISSUED for the unmanaged row is now the required behaviour and is
// asserted as such.
//
// THE KNOWN-POSITIVE IS IN THE SAME RUN AND ON THE SAME INSTRUMENT. A zero from a
// counter nothing ever incremented is indistinguishable from a zero earned by a
// gate, so the probe zero is asserted beside a NON-ZERO probe count for the
// managed repo taken from the same recorder in the same render. Without that
// control this test passes against a walk that was simply never driven.
func TestManageStatusDoesNotMaterializeUnmanagedGraphs(t *testing.T) {
	fake, seg, deps := unmanagedFixture()

	out := renderLLMCoverage(context.Background(), deps)
	require.NotEmpty(t, out, "precondition: the coverage table must have rendered")

	json := map[string]any{}
	addLocalDaemonJSON(context.Background(), deps, json)
	require.Contains(t, json, "coverage", "precondition: the json path must have assembled coverage rows")

	statsTargets := statsTargetsFor(fake)
	probes := seg.probedKeys()

	// KNOWN-POSITIVE first, so a later zero is a reading rather than an absence.
	require.Positive(t, countOf(statsTargets, "code/managedrepo"),
		"control: the walk must issue a Stats RPC for the graph this client DOES maintain")
	require.Positive(t, countOf(probes, "code/managedrepo"),
		"control: the walk must probe the segment pool of the graph this client DOES maintain")

	assert.Positive(t, countOf(statsTargets, "code/foreignrepo"),
		"a graph outside the working set must still be COUNTED: the Stats RPC is answered "+
			"from durable state on both backends and makes no graph resident")
	assert.Zero(t, countOf(probes, "code/foreignrepo"),
		"a graph outside the working set must receive NO segment probe: ShippedSegmentDocCount "+
			"imports its whole L2 pool and LiveResidentDocCount constructs its engine")
}

// TestSegProbeWaveDeclinesUnmanagedTargets drives collectSegProbes DIRECTLY.
//
// IT USED TO BE THE ONLY PIN ON THE `!t.managed` CLAUSE, and the reason it existed
// is the thing that changed. When the walk declined the Stats RPC for an unmanaged
// row, that row's stats slot was nil and collectSegProbes' pre-existing nil-stats
// clause skipped it anyway — so deleting the membership clause left the whole-walk
// test green and the probe gate unpinned there however correct it was. Exactly the
// state that test could not produce is now the ordinary one: an unmanaged row
// carries REAL counts, so its stats slot is non-nil in the whole walk and the
// membership clause is the sole thing declining its probe.
//
// IT IS KEPT ANYWAY, and not as a leftover. It pins the clause at the function
// boundary rather than through five hundred lines of walk, so a future change to
// how rows are assembled cannot quietly move which gate is doing the work — and it
// asserts the declined row's zero triple directly, which the rendered table only
// shows through a cell.
func TestSegProbeWaveDeclinesUnmanagedTargets(t *testing.T) {
	_, seg, deps := unmanagedFixture()

	targets := []coverageTarget{
		{label: "code/managedrepo", gt: kgtypes.GraphCode, name: "managedrepo", managed: true},
		{label: "code/foreignrepo", gt: kgtypes.GraphCode, name: "foreignrepo", managed: false},
	}
	// BOTH slots non-nil, which is the state the whole walk cannot produce for an
	// unmanaged row and the only state under which the managed flag is the sole
	// discriminator between the two rows.
	stats := []*knowledgev1.GraphStats{{NonProxyNodeCount: 800}, {NonProxyNodeCount: 366842}}

	probes := collectSegProbes(context.Background(), deps, targets, stats)
	require.Len(t, probes, 2)

	probed := seg.probedKeys()
	require.Positive(t, countOf(probed, "code/managedrepo"),
		"control: the managed target IS probed, so the wave really ran")
	assert.Zero(t, countOf(probed, "code/foreignrepo"),
		"the unmanaged target must not be probed even with a populated stats slot beside it")

	assert.True(t, probes[0].hasSeg, "control: the probed row carries a real segment answer")
	assert.False(t, probes[1].hasSeg,
		"the declined row keeps the zero triple, indistinguishable from a row with no pool")
}

// coverageRowsByLabel renders the table and returns each row line by its leading
// graph label, so a test reads the row it means rather than an index into the body.
func coverageRowsByLabel(out string) map[string]string {
	rows := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "| ") {
			continue
		}
		label := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(trimmed, "| "), "|", 2)[0])
		rows[label] = trimmed
	}
	return rows
}

// TestUnmanagedRowRendersRealCounts pins the ruling's steady state: a graph this
// client does not maintain reports its REAL counts, in the [unmanaged] band, with
// only the segment cell saying "not read".
//
// THE FIXTURE IS CLOUD-SHAPED IN THE ONE RESPECT THAT MATTERS — the Stats RPC
// answers, which is what a backend that can count without materializing does. The
// row it produces is the same on either backend; what differs is only whether the
// server can answer, and the fallback for one that cannot is the next test.
//
// EVERY ASSERTION HAS ITS CONTROL IN THE SAME RENDER. The managed repo's row is
// read from the same table, so "the unmanaged row carries counts" is distinguished
// from "the fixture served counts to everyone", and the two rows differ in exactly
// the segment cell.
func TestUnmanagedRowRendersRealCounts(t *testing.T) {
	_, _, deps := unmanagedFixture()

	rows := coverageRowsByLabel(renderLLMCoverage(context.Background(), deps))
	unmanagedRow, managedRow := rows["code/foreignrepo"], rows["code/managedrepo"]

	require.NotEmpty(t, unmanagedRow, "the unmanaged graph must still appear in the inventory")
	require.NotEmpty(t, managedRow, "control: the managed graph's row must render too")
	assert.Contains(t, managedRow, "800 of 800", "control: the maintained graph reports its real counts")

	assert.Contains(t, unmanagedRow, "366842 of 366842",
		"a graph outside the working set reports the counts its backend can produce without "+
			"materializing it — counting is not interacting")
	assert.NotContains(t, unmanagedRow, "(empty graph)",
		"a 366k-node graph must never render as empty")

	assert.Contains(t, unmanagedRow, "not read ["+DispositionUnmanaged+"]",
		"the SEGMENT cell is the one that was not read: both probes behind it materialize "+
			"the pool and the engine in this process")
	assert.NotContains(t, unmanagedRow, "shipped ",
		"an unprobed pool must never render 'shipped 0 · live 0', which reads as a measurement")
	assert.Contains(t, managedRow, "shipped 800 · live 800",
		"control: the probed row DOES render the two measurements, so the unmanaged row's "+
			"'not read' is the gate rather than an unprogrammed fixture")

	// A row narrower or wider than its header has its trailing cells silently
	// dropped — the defect TestCoverageTableHeaderMatchesRowCellCount exists to
	// catch, whose fixture wires no working set and so reaches no unmanaged row.
	assert.Equal(t, markdownCells(managedRow), markdownCells(unmanagedRow),
		"the unmanaged row must carry the same cell count as a populated one")
}

// TestUnmanagedRowFallsBackWhenCountsCannotBeProduced pins the OTHER arm: a backend
// that cannot answer the counts without materializing the graph fails the call, and
// the row says so rather than rendering the zeros of an answer nobody gave.
//
// THIS IS THE FALLBACK, NOT THE RULE. It is reachable today only on a local server
// whose image predates the durable count record; a cloud-backed row and a
// current-image local row both take the counts arm above. Rendering it as the
// steady state is what the ruling corrected.
//
// THE UNMANAGED ROW SURVIVES ITS STATS FAILURE WHERE A MANAGED ROW DOES NOT, and
// that asymmetry is asserted here rather than assumed: dropping the row would
// silently delete a graph from the inventory this table exists to show.
func TestUnmanagedRowFallsBackWhenCountsCannotBeProduced(t *testing.T) {
	fake, _, deps := unmanagedFixture()
	fake.statsErrByKey = map[string]bool{"code/foreignrepo": true}

	rows := coverageRowsByLabel(renderLLMCoverage(context.Background(), deps))
	unmanagedRow, managedRow := rows["code/foreignrepo"], rows["code/managedrepo"]

	require.NotEmpty(t, managedRow, "control: the managed graph's row must render too")
	assert.Contains(t, managedRow, "800 of 800",
		"control: a row whose Stats DID answer still reports its counts, so the fallback "+
			"below is this row's answer rather than a dead fixture")

	require.NotEmpty(t, unmanagedRow,
		"a graph whose counts could not be produced must STILL appear in the inventory")
	assert.Contains(t, unmanagedRow, "not read (unmanaged)",
		"the row must SAY its counts were not read rather than rendering a zero that reads "+
			"as a measurement")
	assert.NotContains(t, unmanagedRow, "366842",
		"no count may appear on a row whose counts nobody produced")
	assert.NotContains(t, unmanagedRow, "(empty graph)",
		"a 366k-node graph nobody read must never render as empty")

	// The one durable fact available with no interaction at all — the graph image's
	// size on disk, which the catalog enumeration already returns without loading.
	assert.Contains(t, unmanagedRow, "image",
		"the fallback row renders the durable state that IS available cold: the image size")
	assert.Equal(t, markdownCells(managedRow), markdownCells(unmanagedRow),
		"the fallback row must carry the same cell count as a populated one")
}
