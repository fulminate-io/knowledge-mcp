// SPDX-License-Identifier: Apache-2.0

// bucket_group_parallel_test.go proves the CEO's success criterion for the
// parallel bucket harvest: the parallel result is content-identical to the serial
// one — the same content hash per partition — and the FOLD ORDER is gated.
//
// package searchengine_test, sharing bucket_group_idempotence_test.go's fixture
// helpers, for that file's reason: the real hnsw format is required, and
// formats/hnsw imports searchengine, so an in-package test file cannot import it
// without a cycle.
//
// HOW A "SERIAL" ARM STILL EXISTS AFTER THE CODE IS PARALLEL. There is
// deliberately NO serial code path kept in production for a test to call — a dead
// branch nobody runs is worse than no branch. The SCHEDULER is forced instead:
// runtime.GOMAXPROCS(1) makes the pool's goroutines run one at a time, which IS
// serial execution order, while GOMAXPROCS(NumCPU) genuinely overlaps them. Both
// arms execute the SAME production code, which is the entire point — the claim is
// that the OUTPUT does not depend on how the harvest was scheduled.
//
// runtime.NumCPU() is NOT affected by GOMAXPROCS, so the pool still creates
// min(NumCPU, len(work)) workers in both arms; only their overlap differs. That is
// deliberate, and keeps scheduling the single variable between the arms.
package searchengine_test

import (
	"math/rand"
	"runtime"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

const (
	// parallelBucketCount is 8 so the pool is GENUINELY CONTENDED. A one-partition
	// group parallelizes to nothing and would pass forever while checking nothing,
	// which is why runGroupHarvest fails outright below when len(work) < 2.
	parallelBucketCount = 8
	// Small on purpose: the measured 415.5ms/op serial hnsw build applies at
	// n=1024, which this fixture deliberately avoids.
	parallelPerBucket = 32
	// parallelRepeats matches TestChunkFilesParallelOrderIsDeterministic's shape —
	// five contended runs, every one compared against run 0.
	parallelRepeats = 5
)

// groupRun is one whole group swap's observable output.
type groupRun struct {
	// published is ReplaceBucketGroup's own return: the content hash per partition.
	published map[int]searchengine.SegmentID
	// merges is every MergeResult Options.OnMerge delivered during the swap.
	merges []searchengine.MergeResult
	// consumed is the spanning constituent the group retires and does NOT
	// republish — what makes the group's survivor set non-empty.
	consumed searchengine.SegmentID
	// firstWorkBucket is work[0].Bucket, the partition whose entry lands at
	// added[0] when every partition publishes.
	firstWorkBucket int
}

// runGroupHarvest seeds a FRESH engine from the same deterministic spec and drives
// ONE ReplaceBucketGroup over a closed group spanning every partition.
//
// The fixture is built so BOTH of leg (c)'s obligations hold, and it asserts each
// rather than assuming it — an unmet obligation makes that leg vacuous rather than
// failing, which is the outcome worth preventing:
//
//   - EVERY PARTITION PUBLISHES, so added[0] really is work[0]'s entry. added[0]
//     is the first work item in fold order with a NON-NIL entry, not literally
//     work[0]; if work[0] harvested empty, the leg would compare the wrong pair.
//   - THE SPANNING CONSTITUENT IS SUPERSEDED AND NOT REPUBLISHED, so `survivors`
//     is non-empty. If it were empty, fireMergeHook returns early for added[0] too,
//     ZERO invocations fire, and "exactly one" is unsatisfiable.
func runGroupHarvest(t *testing.T, byBucket map[int][]string, flat []string) groupRun {
	t.Helper()

	var mu sync.Mutex
	var merges []searchengine.MergeResult

	eng := searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{
		MinSegmentDocs:     1 << 20,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		// The capture idiom TestGroupReclaimSparesEveryPublishedID uses, with a
		// mutex added because step 2.4 runs this test under -race.
		OnMerge: func(res searchengine.MergeResult) {
			mu.Lock()
			defer mu.Unlock()
			merges = append(merges, res)
		},
	})
	defer eng.Close()

	// ONE segment spanning every partition — the shape that makes a group a group,
	// since its members belong to more than one partition.
	seedRNG := rand.New(rand.NewSource(20260826))
	seed := make([]searchengine.Document, 0, len(flat))
	for _, id := range flat {
		seed = append(seed, searchengine.Document{ID: id, Vector: fuseVector(seedRNG)})
	}
	if err := eng.Add(seed); err != nil {
		t.Fatalf("Add seed corpus: %v", err)
	}
	if err := eng.Flush(); err != nil {
		t.Fatalf("Flush seed corpus: %v", err)
	}
	exported := eng.Export()
	if len(exported) != 1 {
		t.Fatalf("fixture seeded %d segments, want exactly 1 spanning segment", len(exported))
	}
	consumed := exported[0].ID

	// Fresh copies of every seeded id, routed to their partitions. Every partition
	// therefore has incoming documents and every partition publishes.
	deltaRNG := rand.New(rand.NewSource(19700101))
	work := make([]searchengine.BucketWork, 0, parallelBucketCount)
	for b := range parallelBucketCount {
		ids := byBucket[b]
		docs := make([]searchengine.Document, 0, len(ids))
		for _, id := range ids {
			docs = append(docs, searchengine.Document{ID: id, Vector: fuseVector(deltaRNG)})
		}
		work = append(work, searchengine.BucketWork{
			Bucket:     b,
			Superseded: append([]searchengine.ExternalID(nil), ids...),
			Docs:       docs,
		})
	}
	if len(work) < 2 {
		t.Fatalf("fixture built %d work items — a one-partition group parallelizes to nothing and this test would pass forever while checking nothing", len(work))
	}

	published, _, err := eng.ReplaceBucketGroup(parallelBucketCount, []searchengine.SegmentID{consumed}, work)
	if err != nil {
		t.Fatalf("ReplaceBucketGroup: %v", err)
	}

	mu.Lock()
	captured := append([]searchengine.MergeResult(nil), merges...)
	mu.Unlock()

	return groupRun{
		published:       published,
		merges:          captured,
		consumed:        consumed,
		firstWorkBucket: work[0].Bucket,
	}
}

// withGOMAXPROCS runs fn with GOMAXPROCS pinned to n and restores the prior value.
func withGOMAXPROCS(n int, fn func()) {
	prior := runtime.GOMAXPROCS(n)
	defer runtime.GOMAXPROCS(prior)
	fn()
}

// samePublished reports the first partition on which two runs disagree.
func samePublished(a, b map[int]searchengine.SegmentID) (int, bool) {
	if len(a) != len(b) {
		return -1, false
	}
	for bucket, id := range a {
		if b[bucket] != id {
			return bucket, false
		}
	}
	return 0, true
}

// TestBucketGroupParallelHarvestIsContentIdenticalToSerial is the determinism
// proof for the parallel bucket harvest, in three legs.
//
// HONEST LABELING OF WHAT EACH LEG DISCRIMINATES. Legs (a) and (b) are
// CHARACTERIZATION GUARDS: against a serial harvest they pass vacuously, because
// scheduling cannot vary an output nothing schedules. They become discriminating
// only once the bounded pool exists, and they are what would catch a future change
// that made the harvest's result depend on completion order. LEG (c) IS DIFFERENT
// — it is discriminating against a REORDERED FOLD whether the harvest is parallel
// or serial, and it is the only thing in this plan that is. That distinction was
// established by execution, not argument: the plan review ran the three existing
// group-swap tests against a build with the harvest fold REVERSED and all three
// passed, because each asserts an order-INDEPENDENT property.
func TestBucketGroupParallelHarvestIsContentIdenticalToSerial(t *testing.T) {
	byBucket, flat := fuseIDsByBucket(t, parallelBucketCount, parallelPerBucket)

	// (a) CROSS-ARM IDENTITY. A SegmentID is the sha256 of the encoded segment
	// bytes, so equality of ids IS content identity — this claims no more.
	var serialArm, parallelArm groupRun
	withGOMAXPROCS(1, func() { serialArm = runGroupHarvest(t, byBucket, flat) })
	withGOMAXPROCS(runtime.NumCPU(), func() { parallelArm = runGroupHarvest(t, byBucket, flat) })

	if len(serialArm.published) != parallelBucketCount {
		t.Fatalf("the serial arm published %d partitions, want %d — every partition must publish or leg (c) below compares the wrong pair", len(serialArm.published), parallelBucketCount)
	}
	if bucket, ok := samePublished(serialArm.published, parallelArm.published); !ok {
		t.Fatalf("partition %d published a DIFFERENT content hash under GOMAXPROCS(%d) than under GOMAXPROCS(1): %s vs %s — the harvest's output depends on how it was scheduled",
			bucket, runtime.NumCPU(), parallelArm.published[bucket][:12], serialArm.published[bucket][:12])
	}

	// (b) REPEAT-RUN STABILITY UNDER CONTENTION.
	var runs []groupRun
	withGOMAXPROCS(runtime.NumCPU(), func() {
		for range parallelRepeats {
			runs = append(runs, runGroupHarvest(t, byBucket, flat))
		}
	})
	for i := 1; i < len(runs); i++ {
		if bucket, ok := samePublished(runs[0].published, runs[i].published); !ok {
			t.Fatalf("contended run %d disagrees with run 0 on partition %d: %s vs %s",
				i, bucket, runs[i].published[bucket][:12], runs[0].published[bucket][:12])
		}
	}

	// (c) FOLD ORDER. Step (5) of the group swap hands the group's whole survivor
	// set to the FIRST entry in `added` and nil to the rest, and fireMergeHook
	// returns early when its removal set is empty — so EXACTLY ONE MergeResult is
	// delivered per group, and it must be work[0]'s. A fold that ran in completion
	// order would move that event to whichever partition finished first.
	for i, run := range runs {
		// FIXTURE OBLIGATION ONE, asserted loudly rather than assumed: every
		// partition published, so added[0] is genuinely work[0]'s entry.
		if len(run.published) != parallelBucketCount {
			t.Fatalf("run %d published %d partitions, want %d — added[0] is the first work item with a NON-NIL entry, so a partition that harvested empty would make this leg compare the wrong pair",
				i, len(run.published), parallelBucketCount)
		}
		// FIXTURE OBLIGATION TWO: the spanning constituent really was superseded and
		// NOT republished. If it were republished, `survivors` would be empty,
		// fireMergeHook would return early for added[0] too, ZERO events would fire,
		// and "exactly one" would be unsatisfiable rather than false.
		for bucket, id := range run.published {
			if id == run.consumed {
				t.Fatalf("run %d: partition %d republished the consumed constituent %s — the group's survivor set is then empty, no reclaim event fires at all, and the assertion below would be vacuous",
					i, bucket, id[:12])
			}
		}

		if len(run.merges) != 1 {
			t.Fatalf("run %d delivered %d MergeResults, want EXACTLY 1 — the group hands its survivor set to added[0] alone and reports the rest with no removals, so a second event is itself a defect",
				i, len(run.merges))
		}
		wantID := run.published[run.firstWorkBucket]
		if got := run.merges[0].Merged.ID; got != wantID {
			t.Fatalf("run %d: the reclaim event fired for segment %s, but work[0] (partition %d) published %s — the harvest results were folded in COMPLETION order rather than in work order, so which segment carries the group's reclaim varies run to run",
				i, got[:12], run.firstWorkBucket, wantID[:12])
		}
		if len(run.merges[0].Removed) == 0 {
			t.Fatalf("run %d: the reclaim event named no removed segments, so it cannot discriminate which entry carried it", i)
		}
	}
}
