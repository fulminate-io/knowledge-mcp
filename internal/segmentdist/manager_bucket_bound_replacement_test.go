// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound_replacement_test.go — THE LATCH OVER A SET THAT NO LONGER
// EXISTS: a census latch armed over the resident set a reset's layer swap then
// DISCARDS keeps governing the re-drain that refills the engine.
//
// THE REGIME IS THE ONE MEASURED ON THE v0.10.6 CANDIDATE, corpus knowledge/default.
// Eleven consecutive censuses landed on the same floor and armed the latch there; the
// rebuild's swap then took the resident count from that floor to 128
// (`rebuild_segments: run complete ... bm25_pruned=3675 resident_segments=128`), the
// BM25 arm re-drained the whole corpus from a reset cursor, and NO census ran for the
// whole climb — the gate compares the new set's count against an arming point taken
// over a set the swap had already retired, plus the slack of the CURRENT bucket count.
// The count reached 5,727 against a budget of 4,096 with no consolidation, no failed
// partition and nothing in the log to explain it.
//
// THE ROW DRIVES THE REAL MANAGER AND THE REAL SWAP. The latch is armed by the
// production bound over a real fixture, the swap is the production
// StageRebuildPartition + FinalizeRebuild pair (which runs finalizeResetLayer for BOTH
// formats in one call), and the re-drain is the seal path's own call shape. Nothing
// here reaches into the latch to set it: a fixture that armed the atomics by hand
// would pin the test's arithmetic rather than the bound's.

package segmentdist

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// The fixture's shape, in the bound's own arithmetic at replaceBuckets (budget 4,096,
// low-water 2,048, slack 2,048 — the regime the reproduced run's re-drain ran under,
// because the corpus crossed 131,072 documents and BucketCountFor doubled to 256).
//
// replaceSpanning + replaceSoloTails sits one partition's worth of tails ABOVE the
// budget, and the tails are the only candidates a per-partition swap can take: the
// census consolidates that one partition, gives back nine segments against a
// threshold of one half-slack, and ARMS — the arming regime, reached through the
// production rule rather than declared.
const (
	replaceBuckets    = 256
	replaceSpanning   = 4090
	replaceSoloTails  = 10
	replaceRebuildDoc = 200
)

// TestCensusLatchClearsWhenTheResidentSetIsReplaced is requirement 1: the first census
// on a set the rebuild's swap just published runs at the ORDINARY gate.
//
// WHAT RED LOOKS LIKE, and it is the shape the capture holds: the re-drain climbs from
// the swapped-in layer past the budget and on toward the armed floor plus a whole
// slack, paying no census and consolidating nothing, while the engine fans every
// search out over a resident set half again the size the fan-out budget was measured
// at. WHAT GREEN LOOKS LIKE: the first crossing after the swap censuses, one write
// batch above the budget.
//
// BOTH FORMATS, and they are driven by ONE FinalizeRebuild rather than two: the swap
// is one generic body (finalizeResetLayer) instantiated per format, so a clear reached
// by only one of them is exactly the drift this row exists to catch.
func TestCensusLatchClearsWhenTheResidentSetIsReplaced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const name = "latch-clear-on-replacement"
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	spanning, solo := replacementFixture(t, replaceSpanning, replaceSoloTails)
	hnswArm := mgr.managerFor(boundGraphType, name)
	bm25Arm := mgr.bm25ManagerFor(boundGraphType, name)
	armedHNSW := armReplacementLatch(t, "hnswv3", hnswArm, spanning, solo)
	armedBM25 := armReplacementLatch(t, "bm25v2", bm25Arm, spanning, solo)

	// THE SWAP, through the production pair. The staged corpus is small on purpose:
	// the layer it publishes is what the engine holds afterwards, which is the
	// reproduced run's 3,675 -> 128.
	stageRebuildRun(t, ctx, mgr, boundGraphType, name, vecContentDocs(replaceRebuildDoc))
	res, err := mgr.FinalizeRebuild(ctx, boundGraphType, name)
	require.NoError(t, err)
	require.True(t, res.Swapped,
		"PRECONDITION: the reset's layer swap must LAND for both formats — a skipped publish also returns a nil "+
			"error, and every assertion below would then be about the set the latch was armed over")

	// THE LATCH IS READ BEFORE THE RE-DRAIN AND ASSERTED AFTER IT, and the order is
	// load-bearing rather than stylistic. The re-drain settles the latch itself, so the
	// reading has to be taken here; asserting on it here as well would stop the run at
	// the atomics and never show what the unfixed gate DOES with them, which is the
	// behaviour the ticket's row is about and the number the capture holds.
	swappedHNSW := readLatch(hnswArm)
	swappedBM25 := readLatch(bm25Arm)

	driveReplacementRedrain(t, "hnswv3", hnswArm, armedHNSW)
	driveReplacementRedrain(t, "bm25v2", bm25Arm, armedBM25)
	requireLatchClearedByReplacement(t, "hnswv3", armedHNSW, swappedHNSW)
	requireLatchClearedByReplacement(t, "bm25v2", armedBM25, swappedBM25)
}

// TestCensusLatchClearsWhenAGroupSwapRebuildsTheSet is the SECOND replacement site:
// the group swap the drain runs, and the same call the reset's build-window absorb
// makes.
//
// IT IS THE SITE THE BOUND ITSELF DEFERS TO BY NAME. When a census leaves the count
// over budget because every remaining candidate spans several partitions, the warning
// it logs says those segments are "the drain's group rebuild to consolidate" — and
// that rebuild publishes one segment per partition in the closure and unloads the
// tails it covered. A latch armed over the set it replaced would then hold the bound
// off the set it published, which is the same defect as the reset's swap one mechanism
// down.
//
// THE MOCK FORMAT AND ONE ARM, because what is under test is the latch's disposition
// across a call, not what the merge produced: the equivalence rows in this package own
// the answers, and a real-format pair here would cost two engines to assert two
// atomics.
//
// THE ROW IS SERIAL because it reads the clear's own INFO line, which means replacing
// the process-global slog default for the width of the drive — the discipline the
// treadmill row states for the same reason.
func TestCensusLatchClearsWhenAGroupSwapRebuildsTheSet(t *testing.T) {
	engine := searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
	})
	t.Cleanup(engine.Close)
	dm := newDistManager(engine, nil, graphSelector(boundGraphType, "latch-clear-on-group-swap"), "mock")
	budget := searchengine.ResidentSegmentFanoutBudget
	_, slack := residentLowWater(spanCount)

	// The arming regime, built the way the partial-consolidation row builds it: a
	// backlog that spans both partitions and can never be taken, plus a handful of
	// candidates whose consolidation falls short of the threshold.
	for i := range budget - 1 {
		_, err := engine.AddSealAndSupersede(spanningPair(t, i))
		require.NoError(t, err)
	}
	// FIVE candidates, not ten: the threshold at this partition count is half the
	// slack (8), so a consolidation that gives back nine CLEARS the latch and ten would
	// drive the sibling regime the partial-consolidation row already owns.
	for seed := range groupSwapCandidates {
		_, err := engine.AddSealAndSupersede([]searchengine.Document{soloBucketDoc(t, seed)})
		require.NoError(t, err)
	}
	require.Greater(t, engine.ResidentSegmentCount(), budget,
		"PRECONDITION: over budget, or the bound returns at its crossing gate and arms nothing")
	boundResidentSegments(dm, spanCount)
	armed := dm.lastCensusCount.Load()
	require.Positive(t, armed,
		"PRECONDITION: the census must have ARMED the latch — it consolidated the one candidate partition and gave "+
			"back %d segments against the threshold of %d — or there is nothing for the swap to discard",
		groupSwapCandidates-1, slack/2)

	residentBefore := engine.ResidentSegmentCount()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	published, err := groupSwapArrival(t, dm, "group-swap-arrival")
	// THE SECOND SWAP IS THE LOG GUARD'S KNOWN NEGATIVE: the latch is already clear by
	// then, so a clear that announced itself unconditionally would emit a line per swap
	// and bury the armed case the reproduced excursion needed one for.
	_, secondErr := groupSwapArrival(t, dm, "group-swap-arrival-second")
	slog.SetDefault(prev)
	require.NoError(t, secondErr)
	require.NoError(t, err)
	require.NotEmpty(t, published,
		"ANTI-VACUITY: the group swap must have PUBLISHED something, or no set was replaced and the clear below "+
			"would be asserting over an untouched engine")
	require.Less(t, engine.ResidentSegmentCount(), residentBefore,
		"ANTI-VACUITY: and the resident set it left (%d before) must be a different, smaller set", residentBefore)

	require.Zero(t, dm.lastCensusCount.Load(),
		"the group swap rebuilt the partitions the latch was armed over (%d) and retired their constituents, so the "+
			"arming point describes segments that are no longer resident: the next crossing must census the set this "+
			"swap published", armed)
	require.Zero(t, dm.lastCensusFloor.Load(),
		"and its FLOOR must go with it, for the reason the sibling row states: the next census's reduction is "+
			"compared against the count the last one left, and that count was left in the retired set")

	cleared := clearedLatchLines(buf.String())
	require.Len(t, cleared, 1,
		"the clear must announce itself EXACTLY ONCE over the two swaps: once for the armed latch it discarded — the "+
			"line the reproduced excursion's investigation had no way to read — and not at all for the second swap, "+
			"which found nothing armed. Lines: %v", cleared)
	require.Contains(t, cleared[0], "cause=\"bucket group swap\"",
		"and it must name WHICH replacement acted, or an operator reading it cannot tell a drain's group swap from a "+
			"reset's layer swap")
	require.Contains(t, cleared[0], fmt.Sprintf("armed_at=%d", armed),
		"and the arming point it discarded, which is the number the deferral would have been measured from")
}

// groupSwapArrival drives one production group swap over a single arriving document,
// which is the drain's own call shape with the smallest window it can have.
//
// IT RETURNS THE ERROR RATHER THAN FAILING INSIDE, which is the deliberate half of
// that choice: its callers run with the process-global slog default replaced, and a
// t.Fatal here would unwind through the restore and leave every later test in this
// binary writing into a buffer this one owns.
func groupSwapArrival[Q, S any](
	t *testing.T, dm *distManager[Q, S], id string,
) ([]searchengine.SegmentID, error) {
	t.Helper()
	published, _, err := replaceBucketGroups(
		t.Context(), dm, nil, []searchengine.Document{doc(id, "alpha beta")}, nil,
		dm.engine.DistinctResidentDocCount(), nil)
	return published, err
}

// clearedLatchLines returns the clear's own INFO lines, which is the instrument an
// operator has for the state the reproduced excursion left no record of.
func clearedLatchLines(logs string) []string {
	var out []string
	for line := range strings.SplitSeq(logs, "\n") {
		if strings.Contains(line, "cleared the resident-bound census latch") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// latchReading is the gate's own two atomics and the count they were read beside,
// sampled at one instant.
type latchReading struct {
	count    int64
	floor    int64
	resident int
}

// readLatch samples one arm's latch exactly as the gate reads it.
func readLatch[Q, S any](dm *distManager[Q, S]) latchReading {
	return latchReading{
		count:    dm.lastCensusCount.Load(),
		floor:    dm.lastCensusFloor.Load(),
		resident: dm.engine.ResidentSegmentCount(),
	}
}

// armedLatch is what the arming phase measured, carried to the phases that read it so
// no assertion re-derives a number the fixture already established.
type armedLatch struct {
	// floor is the count the arming census LEFT, which is the arming point the gate
	// adds its slack to.
	floor int64
	// censuses is the census count at the moment the swap was driven, so the re-drain
	// reads a DELTA rather than a total.
	censuses int64
}

// armReplacementLatch seals the fixture into one arm and drives the production bound
// until the latch is armed, returning what it armed at.
func armReplacementLatch[Q, S any](
	t *testing.T, format string, dm *distManager[Q, S],
	spanning [][]searchengine.Document, solo []searchengine.Document,
) armedLatch {
	t.Helper()
	budget := searchengine.ResidentSegmentFanoutBudget
	_, slack := residentLowWater(replaceBuckets)

	for _, pair := range spanning {
		_, err := dm.engine.AddSealAndSupersede(pair)
		require.NoError(t, err)
	}
	for _, d := range solo {
		_, err := dm.engine.AddSealAndSupersede([]searchengine.Document{d})
		require.NoError(t, err)
	}
	observed := dm.engine.ResidentSegmentCount()
	require.Greater(t, observed, budget,
		"[%s] PRECONDITION: the fixture must be over budget, or the bound returns at its crossing gate and the "+
			"latch is never armed at all", format)

	boundResidentSegments(dm, replaceBuckets)

	armed := armedLatch{floor: dm.lastCensusCount.Load(), censuses: dm.segmentSpanCensuses()}
	require.Equal(t, int64(1), armed.censuses, "[%s] ANTI-VACUITY: the arming crossing censused", format)
	require.Positive(t, armed.floor,
		"[%s] PRECONDITION: the census must have ARMED the latch — it consolidated the one candidate partition and "+
			"gave back %d segments against a threshold of %d, which is the regime the capture holds; a census that "+
			"cleared leaves nothing for the swap to discard", format, replaceSoloTails-1, slack/2)
	require.Greater(t, armed.floor+int64(slack), int64(budget+replaceBuckets),
		"[%s] PRECONDITION: and the count this latch would re-admit at (%d + %d) must sit ABOVE the ordinary gate's "+
			"first crossing (%d), or the re-drain below censuses on either rule and the row is green whatever the "+
			"swap did with the latch", format, armed.floor, slack, budget+replaceBuckets)
	return armed
}

// requireLatchClearedByReplacement is the DECLARATION, read straight off the atomics
// the gate reads: after a swap that replaced the resident set, the latch and its floor
// describe nothing and are gone.
func requireLatchClearedByReplacement(t *testing.T, format string, armed armedLatch, swapped latchReading) {
	t.Helper()
	require.Less(t, swapped.resident, int(armed.floor),
		"[%s] PRECONDITION: the swap must have REPLACED the set the latch was armed over (%d segments), or there is "+
			"no discarded set for this row to be about", format, armed.floor)
	require.Zero(t, swapped.count,
		"[%s] the layer swap replaced every resident segment, so the arming point taken over the prior set (%d) "+
			"describes a set that no longer exists: the first census on the new set must run at the ordinary gate",
		format, armed.floor)
	require.Zero(t, swapped.floor,
		"[%s] and its FLOOR must go with it: minCensusReduction compares the next census's reduction against the "+
			"count the last one left, and the last one left a count in the retired set", format)
}

// driveReplacementRedrain refills one arm the way the collector arm's re-drain does —
// one segment per partition per write batch, the bound after each batch — and reports
// where the first census landed.
//
// IT STOPS AT budget + slack, which is the ceiling the latch's own header claims and
// the point the reproduced run climbed to: a drive with no stop would seal until the
// armed floor plus a slack on the unfixed tree and the failure would read as a timeout
// rather than as the missing census.
func driveReplacementRedrain[Q, S any](
	t *testing.T, format string, dm *distManager[Q, S], armed armedLatch,
) {
	t.Helper()
	budget := searchengine.ResidentSegmentFanoutBudget
	_, slack := residentLowWater(replaceBuckets)

	seed := replaceRedrainSeed
	peak, censusedAt := 0, 0
	for censusedAt == 0 && peak <= budget+slack {
		for range replaceBuckets {
			_, err := dm.engine.AddSealAndSupersede([]searchengine.Document{replacementDoc(seed)})
			require.NoError(t, err)
			seed++
		}
		if observed := dm.engine.ResidentSegmentCount(); observed > peak {
			peak = observed
		}
		before := dm.segmentSpanCensuses()
		boundResidentSegments(dm, replaceBuckets)
		if dm.segmentSpanCensuses() > before {
			censusedAt = peak
		}
	}
	t.Logf("[%s] re-drain row: first census at observed %d, peak %d (budget=%d, slack=%d, armed floor was %d, "+
		"armed re-admission %d)", format, censusedAt, peak, budget, slack, armed.floor, armed.floor+int64(slack))

	require.Positive(t, censusedAt,
		"[%s] the re-drain after the swap reached %d resident segments — the ceiling budget + slack (%d) — without "+
			"paying a single census: the gate is comparing the new set's count against a floor armed over the set "+
			"the swap retired, and the bound is disabled for the whole climb", format, peak, budget+slack)
	require.LessOrEqual(t, censusedAt, budget+slack,
		"[%s] the first census on the new set must fire before the count exceeds budget + slack at the bucket count "+
			"in force (%d)", format, budget+slack)
	require.LessOrEqual(t, censusedAt, budget+replaceBuckets+replaceRebuildDoc,
		"[%s] and it must fire at the ORDINARY gate — the first crossing, one write batch's seals above the budget "+
			"(%d) — rather than a slack later: a latch cleared by the swap is a latch that defers nothing on the new "+
			"set", format, budget+replaceBuckets+replaceRebuildDoc)
	require.LessOrEqual(t, dm.engine.ResidentSegmentCount(), budget,
		"[%s] and the census must have brought the count back INSIDE the budget, or it censused and found nothing to "+
			"take, which is a different regime from the one this row drives", format)
}

// replaceRedrainSeed starts the re-drain's ids past every id the arming fixture drew,
// so a re-drain document can never supersede a fixture document and shrink the set by
// a route the row does not intend.
// groupSwapCandidates is the candidate partition's tail count for the group-swap row:
// one fewer reduction than minCensusReduction demands, so the census that takes them
// all ARMS the latch instead of clearing it.
const groupSwapCandidates = 5

const replaceRedrainSeed = 100 * (replaceSpanning + replaceSoloTails)

// replacementDoc builds one document carrying both a deterministic 32-byte vector and
// a content field, so the same fixture drives either format's engine.
//
// It is this package's vecContentDocs shape under its OWN id prefix: the rebuild
// corpus this row swaps in uses that helper, and a shared prefix would have the staged
// layer supersede the fixture rather than replace it.
func replacementDoc(i int) searchengine.Document {
	vec := make([]byte, 32)
	for b := range vec {
		vec[b] = byte((i*31 + b*7) % 251)
	}
	return searchengine.Document{
		ID:     fmt.Sprintf("replace-%07d", i),
		Vector: vec,
		Fields: map[string]string{
			searchengine.FieldContent: fmt.Sprintf("alpha beta replace%07d", i),
			searchengine.FieldSummary: "alpha",
		},
	}
}

// replacementFixture draws the arming fixture in ONE pass over an id stream: the
// spanning backlog a per-partition swap may never take, and the solo tails of one
// partition that are the census's only candidates.
//
// It MEASURES each id's partition rather than assuming a spread, the discipline
// spanningPair and armedCorpusDocs state: BucketOf is a hash, and a fixture that
// assumes its shape goes vacuous when the hash changes.
func replacementFixture(
	t *testing.T, spanningSegments, soloDocs int,
) (spanning [][]searchengine.Document, solo []searchengine.Document) {
	t.Helper()
	const soloBucket = 0
	spanning = make([][]searchengine.Document, 0, spanningSegments)
	solo = make([]searchengine.Document, 0, soloDocs)
	var pending searchengine.Document
	pendingBucket := -1
	for i := 0; len(spanning) < spanningSegments || len(solo) < soloDocs; i++ {
		if i > 100*(spanningSegments+soloDocs) {
			t.Fatalf("replacementFixture: only %d of %d spanning segments and %d of %d solo documents after %d ids",
				len(spanning), spanningSegments, len(solo), soloDocs, i)
		}
		d := replacementDoc(i)
		b := searchengine.BucketOf(d.ID, replaceBuckets)
		if len(spanning) < spanningSegments {
			if pendingBucket < 0 {
				pending, pendingBucket = d, b
				continue
			}
			if b == pendingBucket {
				continue
			}
			spanning = append(spanning, []searchengine.Document{pending, d})
			pendingBucket = -1
			continue
		}
		if b == soloBucket {
			solo = append(solo, d)
		}
	}
	return spanning, solo
}
