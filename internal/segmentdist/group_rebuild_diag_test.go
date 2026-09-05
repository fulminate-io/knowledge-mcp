// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// THE SIZES ARE LOAD-BEARING, and they are the straddle file's for the same reason
// that file states: BucketCountFor is the smallest power of two at or above
// ceil(corpus/MinSegmentDocs), so 2000 documents derive 2 partitions and 2100
// derive 4. Seeding 2000 through the one-shot path lands a layout aligned to 2,
// and adding 100 more carries the corpus across the boundary WITHOUT realigning
// anything — which is what leaves every seeded segment spanning two of the four
// partitions the delete then derives. That span is the closure-widening property
// this test needs, and the fixture asserts it rather than assuming it.
const (
	diagSeedN   = 2000 // derives 2 partitions
	diagWindowN = 100  // carries the corpus to 2100, which derives 4
)

// TestGroupRebuildDiagnosticEmitsBeforeTheExpensiveCall proves the group-rebuild
// instrument FIRES and that its numbers are RIGHT.
//
// IT IS A DIFFERENT INSTRUMENT CLASS FROM THE SOURCE SCANS THAT GUARD THE SAME
// STEP, and that is the whole reason it exists. Those scans grep for key NAMES and
// can never read a value, so they stay green against the one way this
// instrumentation goes silently wrong: closeOverConstituency grows the dirty map IN
// PLACE, so a dirty_buckets read taken AFTER the closure reports the CLOSED size
// and the two keys carry the same number. Only a runtime assertion on the emitted
// values catches that, and assertion (2) below is it.
//
// NOT PARALLEL, DELIBERATELY. It replaces the process-wide slog default to capture
// the records, which a parallel sibling would either race with or bleed into. The
// package's other slog-capturing test (TestRemapPendingStopsReArmingAtTheBound,
// remap_convergence_test.go) is serial for the same reason. The records are
// additionally filtered by graph name and format, so a stray emitter cannot be
// mistaken for this delete's.
func TestGroupRebuildDiagnosticEmitsBeforeTheExpensiveCall(t *testing.T) {
	requireMeasurementRun(t)
	ctx := context.Background()
	const name = "group-rebuild-diag"

	require.NotEqual(t,
		searchengine.BucketCountFor(diagSeedN),
		searchengine.BucketCountFor(diagSeedN+diagWindowN),
		"DEGENERATE FIXTURE: the window must carry the corpus across a partition-count boundary, "+
			"or the layout stays aligned and the closure has nothing to widen")

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt := kgtypes.GraphCode

	// SEED BOTH SERVING ENGINES. A delete re-emits through both formats, and the
	// assertions below read the BM25 leg — an HNSW-only seed would leave the BM25
	// engine with no resident corpus at all, where the closure trivially adds
	// nothing and every assertion here would be vacuous.
	seed := prefixIDs(vecContentDocsSeed(diagSeedN, 0), "diag-seed-")
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, seed))
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, seed))

	// The window arrives the way the embed writeback delivers it — sealed resident
	// without realigning the seed's layout.
	window := prefixIDs(vecContentDocsSeed(diagWindowN, diagSeedN), "diag-win-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, window))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, window))

	// THE FIXTURE'S WIDENING PROPERTY IS CHECKED INDEPENDENTLY OF THE LOG, against
	// the engine the assertions will read, at the count the delete will derive. A
	// fixture that stopped widening must read as a BROKEN PROBE rather than as a
	// defect in the instrument, and only a check that does not go through the
	// records can tell those apart.
	bm := mgr.bm25ManagerFor(gt, name)
	deriveCount := searchengine.BucketCountFor(bm.engine.DistinctResidentDocCount())
	require.Greater(t, deriveCount, 1,
		"DEGENERATE FIXTURE: the delete derives %d partition(s); a single-partition group cannot widen", deriveCount)
	require.Greater(t, diagMaxSpan(bm, deriveCount), 1,
		"DEGENERATE FIXTURE: no resident BM25 segment spans more than one of the %d derived partitions, "+
			"so closeOverConstituency has nothing to pull in and dirty_buckets < closed_buckets cannot hold",
		deriveCount)

	// CAPTURE ONLY THE DELETE. The seeding above drives group rebuilds of its own,
	// so the handler is installed here rather than at the top of the test.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{seed[0].ID}))
	slog.SetDefault(prev)

	logged := buf.String()

	// (1) BOTH RECORDS WERE EMITTED FOR THE BM25 LEG, each selected by its format
	// value rather than by its position in the log. A delete emits a pair per
	// engine, so adjacency pairs nothing.
	begin := diagRecord(t, logged, `msg="segmentdist: group_rebuild_begin"`, name, "bm25v2")
	after := diagRecord(t, logged, `msg="segmentdist: group_rebuild"`, name, "bm25v2")

	// (2) THE CLOSURE WIDENED, AND THE TWO KEYS REPORT DIFFERENT NUMBERS. This is
	// the catcher for the in-place mutation described above: under a post-closure
	// read both keys carry the closed size and this strict inequality goes red.
	dirtyBuckets := diagInt(t, begin, "dirty_buckets")
	closedBuckets := diagInt(t, begin, "closed_buckets")
	require.Less(t, dirtyBuckets, closedBuckets,
		"the constituency closure widened the rebuild set, so dirty_buckets must report the PRE-closure "+
			"count and closed_buckets the post-closure one; equal values mean dirty_buckets was read after "+
			"closeOverConstituency mutated the map in place\nbegin: %s", begin)

	// (3) THE WALK COUNTERS AGREE WITH THEMSELVES AND WITH THE NARROWING.
	//
	// UPDATED BY PHASE 2.1, which is the change that made the old form wrong. Before
	// the narrowing every partition received the whole resolved set, so this asserted
	// two exact identities: walked == closed_buckets x max_walked and max_walked ==
	// resolved_segments. Phase 2.1 hands each partition only the constituents that
	// SPAN it, so both stopped holding by design — measured on this very fixture,
	// max_walked went 3 -> 2 and walked went 12 -> 8 against resolved_segments 3.
	// The assertions below are the relations that survive the narrowing.
	//
	// NONE OF THEM IS RECOMPUTED THROUGH THE MEMBERSHIP HELPER THE FIX USES. Deriving
	// the expected per-partition walk from spans here would be a tautology that is
	// green whether or not the narrowing happened; the STRICT narrowing claim is
	// TestDeleteWalksOnlySpanningConstituents' job, and this test stays about whether
	// the instrument's own arithmetic is sound.
	resolvedSegments := diagInt(t, after, "resolved_segments")
	walkedSegments := diagInt(t, after, "walked_segments")
	maxWalked := diagInt(t, after, "max_walked_segments")
	require.GreaterOrEqual(t, maxWalked, 1,
		"the harvest walked SOMETHING — a zero here means the counter was never wired, which is "+
			"indistinguishable from a genuinely empty group unless this fires\nafter: %s", after)
	require.LessOrEqual(t, maxWalked, resolvedSegments,
		"no partition can walk more constituents than the group resolved\nafter: %s", after)
	require.GreaterOrEqual(t, walkedSegments, maxWalked,
		"the total walk is a SUM over partitions, so it is at least the largest single partition's walk\nafter: %s", after)
	require.LessOrEqual(t, walkedSegments, closedBuckets*maxWalked,
		"the total walk is a sum of %d per-partition walks each bounded by the max, so it cannot exceed "+
			"their product — a larger value means the sum and the max disagree\nafter: %s", closedBuckets, after)

	// ASSERTED AGAINST resolved_segments RATHER THAN union_segments, even though the
	// two coincide here. In-test there is no concurrent publisher, so the engine
	// resolves every constituent the manager offered; against the live corpus they
	// diverge, because the resolve step skips constituents a concurrent load already
	// dropped. Checking the engine-side number keeps this test and the live gate
	// asserting the SAME relation.
	require.Equal(t, resolvedSegments, diagInt(t, begin, "union_segments"),
		"with no concurrent publisher the engine must resolve every constituent offered\nbegin: %s\nafter: %s", begin, after)

	// (4) The elapsed reading is present and sane.
	require.GreaterOrEqual(t, diagInt(t, after, "elapsed_ms"), 0,
		"the after record must carry a non-negative elapsed_ms\nafter: %s", after)
}

// diagRecord returns the ONE captured log line carrying the given message literal
// for this test's graph and the given format, failing when there is not exactly
// one.
//
// IT SELECTS BY FORMAT AND GRAPH RATHER THAN BY POSITION. A delete emits a
// begin/after pair per engine, so "the next line after the begin" is not this
// record's after; and the package's other tests share the process-wide default
// handler, so the graph identity is what keeps a neighbour's emission out.
//
// THE GRAPH IDENTITY OF A CODE GRAPH IS SPELLED INTO repo=, NOT name=. The emit
// writes all three GraphSelector fields, and for kgtypes.GraphCode the selector
// carries the repository in Repo and leaves Name EMPTY — observed directly in the
// captured records, which read `graph=code name="" repo=group-rebuild-diag`.
// Matching on name= instead finds nothing and reads as "the instrument never
// fired", which is the wrong diagnosis for a correct instrument.
func diagRecord(t *testing.T, logged, msg, graphName, format string) string {
	t.Helper()
	var hits []string
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, msg) &&
			strings.Contains(line, "repo="+graphName) &&
			strings.Contains(line, "format="+format) {
			hits = append(hits, line)
		}
	}
	require.Lenf(t, hits, 1,
		"expected exactly one %s record for format=%s on repo=%s, found %d\nfull log:\n%s",
		msg, format, graphName, len(hits), logged)
	return hits[0]
}

// diagInt reads one integer-valued key out of a TextHandler record.
//
// THE LEADING BOUNDARY IS WHAT MAKES walked_segments AND max_walked_segments
// DISTINGUISHABLE: without it, a search for walked_segments would match inside
// max_walked_segments and silently read the wrong counter.
func diagInt(t *testing.T, record, key string) int {
	t.Helper()
	m := regexp.MustCompile(`(?:^|\s)` + regexp.QuoteMeta(key) + `=(-?\d+)(?:\s|$)`).FindStringSubmatch(record)
	require.Lenf(t, m, 2, "record carries no integer %s key: %s", key, record)
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
}

// diagMaxSpan reports the largest number of partitions any single resident segment
// spans under the given count — the fixture property the closure needs in order to
// widen anything. It reads the same SegmentSpans pass replaceBucketGroups reads, so
// it measures the engine the assertions will report on.
func diagMaxSpan[Q, S any](dm *distManager[Q, S], count int) int {
	widest := 0
	for _, held := range dm.engine.SegmentSpans(count) {
		widest = max(widest, len(held))
	}
	return widest
}
