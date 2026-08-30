// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// DELETE GROUP-MERGE MEASUREMENT RECORD.
//
// STEP 1.3 IS SATISFIED BY RECORDED EVIDENCE RATHER THAN BY A LIVE CAPTURE, on a
// coordinator ruling. The live corpus is held by a shared daemon that predates this
// instrumentation and serves four active lanes; recutting it is forbidden, stopping
// it is a cross-lane decision, and a second concurrent instance is unguarded by any
// data-directory lock. The ruling is that a pre-fix live capture adds ZERO
// information, because the one question the pre-fix step gates on — does the
// constituency closure widen on the REAL corpus — is already answered at real scale
// by the recorded ship-diff evidence below, and the accumulator identity that makes
// the counters trustworthy is proved exactly by the engine's own numbers at test
// scale. The DECIDING gate legs were deliberately made intra-session postfix-only in
// the plan's second review round, so nothing downstream depends on a prefix capture.
//
// EVERY NUMBER BELOW CARRIES ITS PROVENANCE. The two scales are kept apart on
// purpose: mixing them would let a value derived from the identity be checked
// against the identity, which is a check that cannot fail.
//
// === MEASURED-AT-REAL-SCALE — taken on the live corpus at main f434a95c and
// recorded in the project's re-measurement record. These answer the STOP gate. ===
//
// The daemon's own knowledge/default bm25v2 ship-diff lines across the delete window
// (the field is skipped_as_present, quoted verbatim from the finding):
//
//	10:42:37 resident=129 shipped=2   skipped_as_present=127
//	10:43:01 resident=131 shipped=128 skipped_as_present=3     <- delete 1
//	10:51:51 resident=131 shipped=126 skipped_as_present=5     <- delete 2
//	10:52:24 resident=132 shipped=1   skipped_as_present=131
//
// A single-id delete republishes 126-128 of ~131 partitions where every ordinary
// cycle republishes 0-2. Republishing 128 partitions IS the group rebuilding 128
// partitions, so closed_buckets on the live BM25 leg is 126-128, far above 1.
// BucketCountFor(108586) = 128 independently agrees. THE STOP GATE IS NOT TRIGGERED:
// the closure widens on the real corpus and Phase 2's narrowing is the right fix.
//
// resident_segments is read from the 10:43:01 line, which sits in the steady 128-135
// band rather than inside the post-delete transient the source finding records at
// 10:52:39 (resident=258), where the count overstates warmth.
//
// THE PROVENANCE POINTER LIVES ON THE PLAN STEP, NOT HERE. This file ships in the
// OSS tree, where the repo's leak gate forbids knowledge-graph node ids and
// internal tracker refs, so the id of the re-measurement record that sourced the
// real-scale numbers is carried on the step node's disposition instead. The
// scale labels below are what this file asserts about its own numbers.
//
// ful1530_realscale_closed_buckets=128
// ful1530_prefix_delete_ms=228726
// ful1530_prefix_create_ms=188
// ful1530_resident_segments=128
// ful1530_manifest_segments=128
//
// THE WARMTH PAIR IS RE-READ IN THE POSTFIX SESSION, NOT CARRIED FORWARD, because it
// is what says the client measuring below was warm rather than cold. Re-read
// first-hand 2026-08-29 on the recut daemon: manifest_segments from
// ~/.knowledge/segments/rebuildstate/knowledge/default/manifest.json, whose bm25v2
// entry reads count=128 docs=109478; resident_segments from the live engine's own
// "L2 write diff resolved" record for that graph and format, which reads resident=128.
// A DIFFERENT STAMPER from the live engine reading, which is what makes the pair an
// anchor rather than a process quoting itself. Both are the bm25v2 leg — the same
// format the postfix operands below are read from, because a warmth claim about one
// format's engine says nothing about the other's.
//
// === MEASURED-AT-TEST-SCALE — this session, worktree at d5d2414b, emitted by the
// Phase 1.1 instrumentation and read off the BM25 leg selected by its format value.
// These prove the accumulator counts what the plan thinks it counts. ===
//
// Raw records, both from one DeleteFromBuckets on the closure-widening fixture:
//
//	msg="segmentdist: group_rebuild_begin" format=bm25v2 dirty_buckets=1 closed_buckets=4 union_segments=3
//	msg="segmentdist: group_rebuild"       format=bm25v2 elapsed_ms=16 resolved_segments=3 walked_segments=12 max_walked_segments=3
//	msg="segmentdist: delete_from_buckets" ids=1 hnsw_ms=184 bm25_ms=78
//
// ful1530_prefix_walked=12
// ful1530_prefix_max_walked=3
// ful1530_prefix_resolved_segments=3
// ful1530_prefix_closed_buckets=4
//
// THE ful1530_measured_head MARKER NOW SITS WITH THE POSTFIX VALUES, not here. It used
// to sit directly above these four, which read as though it stamped them; the gate has
// always used it as the staleness anchor for the POSTFIX capture, so it is recorded
// beside the values it actually stamps. The worktree these test-scale numbers came from
// is named in this block's own header above and is unchanged.
//
// THE IDENTITY HOLDS EXACTLY: walked 12 == closed_buckets 4 x max_walked 3, and
// max_walked 3 == resolved_segments 3. Every one of those four numbers was reported
// independently by the engine; none is derived from the identity being checked, so
// the check can fail and is not an identity check against itself.
//
// dirty_buckets=1 against closed_buckets=4 is the closure widening under
// observation, and it is what TestGroupRebuildDiagnosticEmitsBeforeTheExpensiveCall
// asserts as the catcher for the post-mutation dirty read.
//
// === POSTFIX — MEASURED 2026-08-29, post-recut smoke, run stamp
// 2026-08-29T06:42:50Z, against the recut daemon serving the real corpus. ===
//
// The postfix capture is the program's finish -> rebuild -> redeploy -> smoke-test
// endgame. It could not be taken at authoring time, because taking it required the
// same daemon access the prefix capture was denied; these are the values that run
// produced.
//
// THE DELETE WAS DELIBERATELY PLACED IN THE DRAIN WINDOW, which is what makes these
// numbers a worst case rather than timing luck. A single-node delete measured after a
// drain has settled is the EASY case and says nothing about the shape this ticket
// changed. This one was issued while EIGHTY freshly sealed write tails were resident
// and undrained, and it in fact overlapped a concurrent eighty-partition drain rebuild
// still in flight (that drain's own pair reads dirty_buckets=80 closed_buckets=80
// union_segments=80). So the reading is both in-window and contended.
//
// ful1530_measured_head=06e34129
// ful1530_postfix_walked=1
// ful1530_postfix_max_walked=1
// ful1530_postfix_resolved_segments=1
// ful1530_postfix_closed_buckets=1
// ful1530_postfix_delete_ms=290
// ful1530_postfix_create_ms=141
//
// === REFILLED 2026-08-29 AT A HEAD CARRYING THE DEFERRAL, and the refill was required
// rather than cosmetic: the values above it described a delete path that no longer
// exists. ===
//
// THE OPERANDS NOW COME FROM THE bm25v2 LEG, AND THERE IS NO OTHER LEG TO READ. A
// delete used to emit one group_rebuild pair per engine; it now emits NONE for the
// vector format, because that leg is a live-bit kill that rebuilds no partition. The
// record was therefore selected by its FORMAT FIELD rather than by adjacency, and a
// reader comparing this block to the prefix block above is comparing different engines:
// the prefix operands are the vector leg's, these are the field leg's. That is a
// property of the change, not of the sampling.
//
// THE SESSION, so a second person can re-perform it. Warmed with a real vector search
// against this graph before measuring. Created one throwaway node (create_ms=141),
// drove it to residency through the ordinary pipeline — embed writeback observed for
// its id, then a drain sealing it — and ASSERTED residency two ways before deleting:
// the node came back top-ranked from a vector search of this graph, and the delete
// itself emitted a delete_from_buckets record with skipped=false, which a delete of a
// non-resident id cannot do. Then deleted it (delete_ms=290) and read the four operands
// off that record's paired group_rebuild.
//
// delete_ms FELL FROM 1806 TO 290 and the split is the whole reason: the same record
// reads hnsw_ms=0 bm25_ms=80 where the pre-deferral reading was hnsw_ms=1461
// bm25_ms=62. The vector leg's cost did not move — it stopped being on this path.
//
// THE NARROWING LEGS STAY GUARDED AND THAT IS NOT A WEAKENING. closed_buckets is 1
// again, so max_walked and resolved_segments remain the same quantity counted twice and
// a comparison between them can only be an equality. The gate skips those legs at
// closed_buckets=1 for exactly this reason; the 2000ms absolute and the 50x ratio are
// unconditional and both hold.
//
// THE FIRST RELATION HOLDS AND THE SECOND IS NOW DEGENERATE, and saying so is the
// honest reading rather than a weaker one. Every number below was reported
// independently by the engine rather than derived from the relation being checked, so
// the first is still a check that can fail: walked 2 == closed_buckets 1 x max_walked 2.
//
// The second relation, max_walked < resolved_segments, is MEANINGLESS at
// closed_buckets=1 and must not be asserted. With a single partition in the closed
// group, that partition walks its whole constituent list by construction, so max_walked
// and resolved_segments are the SAME QUANTITY counted twice — here both 2 — and a
// comparison between them can only ever be an equality. It is not that the narrowing
// stopped happening; it is that a one-partition group has nothing left to narrow. This
// is the same degeneracy the postfix gate's narrowing legs are guarded against, stated
// here in prose so a reader does not try to restore the comparison.
//
// WHAT CARRIES THE CLAIM INSTEAD IS closed_buckets ITSELF. The delete was issued with
// eighty freshly sealed write tails resident (see above), and it still closed over ONE
// partition. Before per-partition sealing a single write batch sealed ONE segment
// spanning every partition it touched, so that same delete would have closed over all
// eighty — which is the reading the real-scale prefix numbers above record at 126-128.
//
// WHICH FIX EARNED THIS closed_buckets, and why the question is worth answering here.
// An EARLIER postfix capture recorded closed_buckets=3 and was read as the merge-input
// narrowing having collapsed the closure. It was not: that reading was corpus state at
// that instant — no wide write-path tail happened to be resident when the delete ran —
// and it is NOT a property the fix installed.
// The merge-input narrowing changed harvestPartition's input filter and nothing else,
// which searchengine/bucket_swap_group.go:134-135 states in its own words; the
// constituency closure is closeOverConstituency in this package, which that change does
// not touch and cannot bound. A single-id delete measured closed_buckets=124 with that
// fix fully landed, which a closure-collapse reading of it does not survive.
//
// THE READING ABOVE IS A DIFFERENT CLAIM, and it is the one per-partition sealing does
// earn. It was not taken on a quiet corpus: eighty write tails were resident and
// undrained at the moment of the delete, which is exactly the state that produced 124.
// The closure stayed at one partition because no write-path segment spans more than its
// own partition any more, so there is nothing for it to close over. That is a property
// of the segment layout rather than of the instant it was sampled in.
//
// The delete_ms reading of 1806 against the real-scale prefix 228726 is an end-to-end
// timing, and it is CONTENDED rather than clean: the delete overlapped a concurrent
// eighty-partition drain rebuild. Its two legs are also inverted against the prefix
// shape — hnsw_ms=1461 against bm25_ms=62, where the pre-fix leg split had BM25
// carrying essentially the whole cost. A contended in-window number is the right one to
// fence with, but it is not a clean floor and should not be quoted as one.
//
// === THE 240-ID AFTER-MEASUREMENT — MEASURED 2026-08-29 on the same recut daemon, in
// the same session as the postfix block above. This is the "after" half of the
// before/after the deferral ticket asked for. ===
//
// ful1606_after240_ids=240
// ful1606_after240_hnsw_ms=0
// ful1606_after240_bm25_ms=1565
// ful1606_after240_delete_ms=1565
//
// THE BEFORE, for the comparison these numbers exist to make: the same shape measured
// 10734ms, of which the vector leg was the overwhelming majority and the field leg
// about 1717ms. The after is 1565ms with the vector leg at ZERO. The field leg did not
// get faster and was never expected to — it is frozen synchronous by the ticket's own
// scope — so essentially the whole reduction is the vector reconstruction leaving the
// delete path. That is the ticket's claim, measured rather than modeled.
//
// delete_ms IS THE DELETE'S OWN SERVICE TIME, hnsw_ms + bm25_ms off the
// delete_from_buckets record, which is the same instrument the before was read from.
// It is deliberately NOT the end-to-end call, and the gap is worth stating because it
// is large: the tool call around this delete took about 41s of wall clock. Almost none
// of that is the delete. It is the store tombstoning 240 nodes, plus — in this fixture
// specifically — a concurrent 10.2s vector drain rebuilding the 111 partitions that
// CREATING the 240 probe documents had just dirtied. That drain is the WRITE path,
// which this ticket explicitly does not touch, and it would not be present in a delete
// of documents that were already resident. Quoting the 41s as the delete's cost would
// be attributing the write path's work to the delete.
//
// THE MEASUREMENT WAS CONTENDED, and left contended on purpose. The daemon was
// simultaneously running a full post-merge collect of this repo — summarizing and
// embedding — and the deferred re-emit lane was draining a mask of several hundred ids
// throughout. An uncontended number would be the easy case and would say less.
//
// HOW IT WAS TAKEN, re-performably: 240 throwaway nodes created in one batch, every one
// of them observed reaching the embed pipeline, and both formats observed sealing them
// (the vector pool's resident segment count moved 371 -> 480 and the field pool's
// 128 -> 236). Then all 240 deleted in ONE call, which emitted a single
// delete_from_buckets record reading ids=240 skipped=false. The mask grew by exactly
// 240 immediately afterwards and the deferred lane resumed draining it, which is the
// end-to-end confirmation that the work was deferred rather than skipped.
//
// ful1530_checks_delete_ms=  <- DELIBERATELY UNFILLED. NOT OBTAINABLE, NOT UNKNOWN.
//
// THE MEASUREMENT WAS NOT TAKEN. When these values were recorded the checks graph
// carried no segments at all, so there was no instrumented delete to time; it has
// since been enrolled on the embed axis and could carry them, but nobody has
// measured it and this line records that rather than a number.
//
// THE ORIGINAL JUSTIFICATION IS RETIRED and is not restated, because it was a
// mechanical claim about the segment world that no longer holds. What survives is
// the instruction: do not fill this with a number from another graph's delete to
// make the set look complete. A real reading here needs a real run.

// TestDeleteWalksOnlySpanningConstituents proves the merge input actually SHRANK,
// PER PARTITION rather than in aggregate, on the real DeleteFromBuckets path.
//
// STEP 2.2 PROVES THE NARROWING IS SAFE; THIS PROVES IT HAPPENED. Those are
// different claims and a changeset can satisfy either without the other: a
// narrowing that was never wired up is perfectly safe.
//
// THE AGGREGATE ALONE CANNOT CARRY THE CLAIM, which is why max_walked_segments
// exists. A fix that narrowed 127 of 128 partitions still drops the sum, and would
// pass a total-only gate while leaving one partition walking the whole union.
// Every partition's slice is bounded by the max, so max < resolved_segments proves
// EVERY partition narrowed, in one comparison.
//
// THE DENOMINATOR IS resolved_segments, NOT union_segments. In-test there is no
// concurrent publisher so the two coincide, but against the live corpus they
// diverge: ReplaceBucketGroup resolves against its own snapshot and skips
// constituents a concurrent load already dropped. Asserting against the engine's
// own number keeps this test and the Phase 3.2 gate checking the SAME relation, so
// a passing test cannot mean something different from a passing gate.
//
// BOTH EXPECTATIONS ARE ARITHMETIC OVER NUMBERS THE PRODUCTION PATH EMITTED. Nothing
// here recomputes what the fix should have walked through the same membership
// helper the fix uses — that would be a tautology, green whether or not the
// narrowing happened.
func TestDeleteWalksOnlySpanningConstituents(t *testing.T) {
	ctx := context.Background()
	const name = "delete-union-cost"

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt := kgtypes.GraphCode

	// The straddle shape: seed aligned to one partition count, then carry the corpus
	// across a doubling WITHOUT realigning, so the seeded segments span several of
	// the partitions the delete then derives. That span is what the closure widens
	// over and what the narrowing then removes from the partitions it does not reach.
	seed := prefixIDs(vecContentDocsSeed(diagSeedN, 0), "cost-seed-")
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, seed))
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, seed))
	window := prefixIDs(vecContentDocsSeed(diagWindowN, diagSeedN), "cost-win-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, window))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, window))

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{seed[0].ID}))
	slog.SetDefault(prev)

	logged := buf.String()
	begin := diagRecord(t, logged, `msg="segmentdist: group_rebuild_begin"`, name, "bm25v2")
	after := diagRecord(t, logged, `msg="segmentdist: group_rebuild"`, name, "bm25v2")

	closedBuckets := diagInt(t, begin, "closed_buckets")
	resolvedSegments := diagInt(t, after, "resolved_segments")
	walkedSegments := diagInt(t, after, "walked_segments")
	maxWalked := diagInt(t, after, "max_walked_segments")

	// ECHO THE SCALARS so the numbers are recoverable from a run rather than only
	// from an assertion message that fires on failure.
	t.Logf("ful1530_union_walked closed_buckets=%d resolved_segments=%d walked_segments=%d max_walked_segments=%d union_segments=%d",
		closedBuckets, resolvedSegments, walkedSegments, maxWalked, diagInt(t, begin, "union_segments"))

	// (3) THE FIXTURE MUST HAVE WIDENED, or (1) and (2) are vacuous: a single-partition
	// group has nothing to narrow away and both comparisons pass for free.
	if closedBuckets <= 1 {
		t.Fatalf("DEGENERATE FIXTURE: the closed group holds %d partition(s); a one-partition group cannot demonstrate a per-partition narrowing", closedBuckets)
	}
	require.Greater(t, resolvedSegments, 1,
		"DEGENERATE FIXTURE: the group resolved %d constituent(s); with fewer than two there is nothing a narrowing could remove", resolvedSegments)

	// (1) EVERY PARTITION NARROWED.
	require.Less(t, maxWalked, resolvedSegments,
		"the widest partition still walked %d of the %d resolved constituents — at least one partition is being handed the whole union, which is the multiplier this ticket removes\nafter: %s",
		maxWalked, resolvedSegments, after)

	// (2) THE AGGREGATE DROPPED.
	require.Less(t, walkedSegments, closedBuckets*resolvedSegments,
		"the group walked %d constituent-merges against the pre-fix %d (closed_buckets %d x resolved_segments %d), so the total merge input did not shrink\nafter: %s",
		walkedSegments, closedBuckets*resolvedSegments, closedBuckets, resolvedSegments, after)
}

// countGroupRebuilds reports how many partition-rebuild records the captured log
// carries for one graph, across BOTH formats.
//
// IT COUNTS RATHER THAN REQUIRING EXACTLY ONE, which is why it exists beside
// diagRecord instead of reusing it: the assertion below is that the count is ZERO,
// and diagRecord's require.Len(1) cannot express that. It deliberately does not
// filter on format either — "no partition was dirtied" is a claim about both corpora,
// and a version that watched only bm25v2 would stay green while the HNSW leg
// rebuilt.
func countGroupRebuilds(logged, graphName string) int {
	n := 0
	for line := range strings.SplitSeq(logged, "\n") {
		if strings.Contains(line, `msg="segmentdist: group_rebuild"`) &&
			strings.Contains(line, "repo="+graphName) {
			n++
		}
	}
	return n
}

// TestDeleteFromBuckets_NeverHeldIdsDirtyNoPartitions pins the cost property that
// makes this client's half of tombstone-keyed erase delivery affordable.
//
// THE FEED NOW CARRIES ERASES FOR IDS THIS POOL MAY NEVER HAVE INDEXED, because the
// server stopped gating delivery on the deleted node having had a vector. Correctness
// is unaffected — removing an id you do not hold removes nothing — but COST is not:
// the partition re-emit marks a partition dirty for every superseded id by hash
// alone, with no residency test, and then rebuilds it through both formats.
//
// THE KNOWN-POSITIVE CONTROL IS THE HALF THAT MAKES THE ZERO MEAN ANYTHING. A zero
// here is indistinguishable from a probe pointed at nothing — a mistyped graph name, a
// handler swapped in too late, a log line that stopped being emitted — so the same
// test deletes a HELD id through the SAME captured handler and requires that one to
// produce records. Only the pair separates "nothing was dirtied" from "nothing was
// watched".
func TestDeleteFromBuckets_NeverHeldIdsDirtyNoPartitions(t *testing.T) {
	ctx := context.Background()
	const name = "delete-never-held"

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt := kgtypes.GraphCode

	seed := prefixIDs(vecContentDocsSeed(diagSeedN, 0), "held-")
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, seed))
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, seed))

	// FIXTURE CONTROL: the ids the delete names must genuinely be absent, and the
	// id the control deletes must genuinely be present. Both are asked of the
	// engine's own searchability predicate, which is the same one the short-circuit
	// consults — so a fixture that failed to seed cannot read as a clean skip.
	neverHeld := []searchengine.ExternalID{"never-held-a", "never-held-b", "never-held-c"}
	require.Len(t, mgr.managerFor(gt, name).engine.UncoveredFrom(neverHeld), len(neverHeld),
		"FIXTURE CONTROL: the vector corpus must hold NONE of the named ids")
	require.Len(t, mgr.bm25ManagerFor(gt, name).engine.UncoveredFrom(neverHeld), len(neverHeld),
		"FIXTURE CONTROL: the field corpus must hold NONE of the named ids")
	held := []searchengine.ExternalID{seed[0].ID}
	require.Empty(t, mgr.bm25ManagerFor(gt, name).engine.UncoveredFrom(held),
		"FIXTURE CONTROL: the control id must be live-searchable, or the known positive proves nothing")

	capture := func(fn func()) string {
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		defer slog.SetDefault(prev)
		fn()
		return buf.String()
	}

	skipped := capture(func() {
		require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, neverHeld))
	})
	require.Equal(t, 0, countGroupRebuilds(skipped, name),
		"ids no partition holds must dirty NOTHING; the re-emit would rebuild both corpora to remove "+
			"documents neither of them carries\nfull log:\n%s", skipped)

	// A SKIP IS STILL REPORTED, and this is the half a partition-count assertion
	// cannot see. The diagnostic is deferred ABOVE the short-circuit precisely so the
	// one call shape that does no work is not the one call shape that reports nothing
	// — and the skip rate is the interesting number exactly when over-delivered
	// erases are what make the skip fire. Asserting the field here is what keeps it
	// from silently stopping being set.
	require.Contains(t, skipped, `msg="segmentdist: delete_from_buckets"`,
		"the skip must still emit its diagnostic\nfull log:\n%s", skipped)
	require.Contains(t, skipped, "skipped=true",
		"a skipped delete must SAY so, or an operator reads two zero timings and cannot tell a "+
			"no-op from a fast one\nfull log:\n%s", skipped)

	// THE KNOWN POSITIVE, through the same handler and the same call.
	fired := capture(func() {
		require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, held))
	})
	require.Positive(t, countGroupRebuilds(fired, name),
		"KNOWN-POSITIVE CONTROL: deleting a HELD id must still rebuild its partition, or the zero "+
			"above means the probe was blind rather than the work being skipped\nfull log:\n%s", fired)
	// THE FIELD'S OTHER VALUE, from the same instrument in the same run. Without this
	// leg a hardcoded skipped=true would satisfy the assertion above, so the flag
	// would read as a measurement while being a constant.
	require.Contains(t, fired, "skipped=false",
		"a delete that DID the work must report skipped=false\nfull log:\n%s", fired)
}
