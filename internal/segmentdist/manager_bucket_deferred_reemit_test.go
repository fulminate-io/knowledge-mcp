// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// budgetFixtureN is the corpus size that derives MORE partitions than the drain's
// budget admits, which is the only shape in which the bound is reachable at all: a
// corpus deriving eight partitions or fewer is served whole by one tick and the
// assertion below would pass without ever exercising the cap. Sized from the engine's
// own constants rather than spelled as a literal, so a change to segment sizing moves
// the fixture with it instead of silently taking the bound out of reach.
var budgetFixtureN = searchengine.DefaultMinSegmentDocs*deferredReEmitPartitionBudget + 1

// maskSpanningPartitions derives masked ids until they span more partitions than the
// budget admits, and returns them with the partition each one routes to. The ids are
// derived through searchengine.BucketOf at the count the caller passes rather than
// hardcoded, so a fixture that drifts below the bound fails its own precondition
// instead of greening the assertion vacuously.
func maskSpanningPartitions(t *testing.T, bucketCount int) ([]searchengine.ExternalID, map[int][]searchengine.ExternalID) {
	t.Helper()

	byPartition := map[int][]searchengine.ExternalID{}
	var mask []searchengine.ExternalID
	for i := range 512 {
		id := fmt.Sprintf("deferred-masked-%d", i)
		mask = append(mask, id)
		b := searchengine.BucketOf(id, bucketCount)
		byPartition[b] = append(byPartition[b], id)
	}
	require.Greater(t, len(byPartition), deferredReEmitPartitionBudget,
		"FIXTURE PRECONDITION: the mask must span strictly more partitions than the budget admits, "+
			"or the bound is never reached and this test passes without exercising it")
	return mask, byPartition
}

// TestDeferredReEmitIDsServesWholePartitionsWithinTheBudget pins the two halves of the
// selector's contract that no text pattern can see, because both are arithmetic over a
// derived count: it serves WHOLE partitions, and it serves no more of them than the
// budget admits.
//
// The two fail differently. Exceeding the budget puts an unbounded rebuild back on a
// background tick, which is the cost this deferral exists to bound. Serving a PARTIAL
// partition is worse and quieter: the trim keys on partitions the drains published, so
// an id left behind in a partition that was re-emitted is trimmed by nothing and
// re-offered on every tick forever — a lane that fires on the same cause indefinitely.
func TestDeferredReEmitIDsServesWholePartitionsWithinTheBudget(t *testing.T) {
	requireMeasurementRun(t)
	ctx := context.Background()
	gt := kgtypes.GraphCode
	const name = "deferred-budget"

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	docs := bothFormatDocs(budgetFixtureN, "defbudget-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	hnswDM := mgr.managerFor(gt, name)
	require.GreaterOrEqual(t, hnswDM.engine.ResidentDocCount(), residentBackstopFloor,
		"FIXTURE PRECONDITION: the vector pool must clear the floor, or the selector declines and this test "+
			"asserts a bound against an empty slice")
	require.GreaterOrEqual(t, mgr.bm25ManagerFor(gt, name).engine.ResidentDocCount(), residentBackstopFloor,
		"FIXTURE PRECONDITION: the field pool must clear the floor for the same reason")

	bucketCount := searchengine.BucketCountFor(hnswDM.engine.DistinctResidentDocCount())
	mask, byPartition := maskSpanningPartitions(t, bucketCount)
	t.Logf("corpus derives %d partitions; the mask of %d ids spans %d of them, budget is %d",
		bucketCount, len(mask), len(byPartition), deferredReEmitPartitionBudget)

	mgr.SetGraphTombstones(gt, name, mask)
	served := mgr.deferredReEmitIDs(gt, name)
	require.NotEmpty(t, served, "the selector must serve work when the pools are loaded and the mask is non-empty")

	servedPartitions := map[int][]searchengine.ExternalID{}
	for _, id := range served {
		b := searchengine.BucketOf(id, bucketCount)
		servedPartitions[b] = append(servedPartitions[b], id)
	}
	require.Len(t, servedPartitions, deferredReEmitPartitionBudget,
		"the drain must take exactly the budget's worth of partitions from a mask that spans more")

	// WHOLE, IN BOTH DIRECTIONS. Every id of a served partition is present, and no id of
	// an unserved one is — a selector returning a prefix of the mask would satisfy the
	// count assertion above and fail this one.
	for b, ids := range servedPartitions {
		require.ElementsMatch(t, byPartition[b], ids,
			"partition %d was served partially: the trim keys on published partitions, so an id left "+
				"behind here is trimmed by nothing and re-offered forever", b)
	}
	require.Less(t, len(served), len(mask),
		"a mask spanning more partitions than the budget admits cannot be served whole in one tick")
}

// deferredDrainFixture seeds a corpus in BOTH formats that derives more partitions than
// the drain's budget, drains it once so real segments exist to rebuild, and seals a mask
// spanning every partition through the production route (sealDeletedIDs) so the durable
// record and the in-memory seed agree exactly as they would after a delete.
//
// The masked ids are deliberately NOT corpus documents: what is under test is which
// partitions a drain re-emits and discharges, and an id's routing does not depend on
// whether a document for it exists.
func deferredDrainFixture(t *testing.T, name string) (*Manager, kgtypes.GraphType, []searchengine.ExternalID) {
	t.Helper()

	ctx := context.Background()
	gt := kgtypes.GraphCode
	dir := t.TempDir()

	mgr := closeOnCleanup(t, NewManager(dir, 0))
	docs := bothFormatDocs(budgetFixtureN, "defdrain-"+name+"-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	bucketCount := searchengine.BucketCountFor(mgr.managerFor(gt, name).engine.DistinctResidentDocCount())
	mask, byPartition := maskSpanningPartitions(t, bucketCount)
	require.NoError(t, mgr.sealDeletedIDs(gt, name, mask))
	t.Logf("fixture %s: corpus derives %d partitions, mask spans %d of them, budget %d",
		name, bucketCount, len(byPartition), deferredReEmitPartitionBudget)

	return mgr, gt, mask
}

// persistedMask reads the durable tombstone record — what a restart would import from,
// as opposed to what this process remembers.
func persistedMask(t *testing.T, m *Manager, gt kgtypes.GraphType, name string) []searchengine.ExternalID {
	t.Helper()
	_, ids, err := m.LoadRebuildState(gt, name)
	require.NoError(t, err)
	return ids
}

// TestDeferredDrainTrimsExactlyThePartitionsItEmitted pins the trim's operand and its
// shape: a drain discharges the ids whose partitions it PUBLISHED and no others, and the
// per-format predicate is a CONJUNCTION.
//
// Asserting only the shrink is satisfied by a trim that empties the record, which is the
// Phase 1 defect wearing a new coat. Asserting only the survival is satisfied by a trim
// that never fires. And a fixture where both formats always publish the same partitions
// cannot tell a conjunction from a disjunction at all, which is why the second subtest
// drives the predicate directly.
func TestDeferredDrainTrimsExactlyThePartitionsItEmitted(t *testing.T) {
	requireMeasurementRun(t)
	ctx := context.Background()

	t.Run("the drain discharges what it published and leaves what it did not reach", func(t *testing.T) {
		const name = "deferred-trim"
		mgr, gt, mask := deferredDrainFixture(t, name)

		served := mgr.deferredReEmitIDs(gt, name)
		require.NotEmpty(t, served, "PRECONDITION: the selector must offer work, or the trim has nothing to discharge")
		require.Less(t, len(served), len(mask),
			"PRECONDITION: the mask must span more partitions than one tick serves, or the survival leg is vacuous")

		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

		servedSet := map[searchengine.ExternalID]struct{}{}
		for _, id := range served {
			servedSet[id] = struct{}{}
		}
		var expectedSurvivors []searchengine.ExternalID
		for _, id := range mask {
			if _, gone := servedSet[id]; !gone {
				expectedSurvivors = append(expectedSurvivors, id)
			}
		}

		require.ElementsMatch(t, expectedSurvivors, persistedMask(t, mgr, gt, name),
			"the durable record must lose exactly the ids whose partitions both formats published — "+
				"no more (an emptied record resurrects everything the budget never reached) and no fewer "+
				"(a trim that never fires re-emits the same partitions forever)")
		require.ElementsMatch(t, expectedSurvivors, mgr.graphTombstones(gt, name),
			"and the in-memory seed must follow disk, or this process and a restart mask different sets")
	})

	t.Run("an id published by only ONE format stays masked", func(t *testing.T) {
		const name = "deferred-conjunction"
		mgr, gt, mask := deferredDrainFixture(t, name)

		// The two pools derive their counts independently, so an id's partition number
		// under one format is not its partition number under the other. This drives the
		// predicate with the two formats DISAGREEING, which the drain fixture above
		// cannot produce (both its pools hold the same corpus and publish in lockstep).
		// The state is reachable in production: ReplaceBucketGroup records no entry for
		// a partition whose harvest came back empty — a partition whose every member is
		// now dead — so one format can publish where the other did not.
		const hnswCount, bm25Count = 16, 4
		victim := mask[0]
		hnswPartition := searchengine.BucketOf(victim, hnswCount)
		bm25Partition := searchengine.BucketOf(victim, bm25Count)

		before := persistedMask(t, mgr, gt, name)
		require.Contains(t, before, victim, "PRECONDITION: the id under test must start masked")

		require.NoError(t, mgr.trimReEmittedTombstones(
			gt, name, []searchengine.ExternalID{victim},
			map[int]bool{hnswPartition: true}, hnswCount,
			map[int]bool{}, bm25Count))
		require.ElementsMatch(t, before, persistedMask(t, mgr, gt, name),
			"an id whose vector partition was published but whose field partition was NOT must stay "+
				"masked: the field blob still carries it, and dropping the mask entry resurrects it on "+
				"the next import")

		// KNOWN POSITIVE, SAME RUN. Without it the assertion above is equally satisfied
		// by a trim that never removes anything at all.
		require.NoError(t, mgr.trimReEmittedTombstones(
			gt, name, []searchengine.ExternalID{victim},
			map[int]bool{hnswPartition: true}, hnswCount,
			map[int]bool{bm25Partition: true}, bm25Count))
		require.NotContains(t, persistedMask(t, mgr, gt, name), victim,
			"CONTROL: the SAME id with BOTH formats' partitions published must leave the record, or the "+
				"survival above proves only that the trim is inert")
	})
}

// TestDeferredReEmitConvergesAndThenStops is the convergence property, and the second
// half is the one that matters: a lane that can fire forever on the same cause is a
// defect wearing the shape of a handled condition, not a bounded background pass.
func TestDeferredReEmitConvergesAndThenStops(t *testing.T) {
	requireMeasurementRun(t)
	ctx := context.Background()
	const name = "deferred-converge"
	mgr, gt, mask := deferredDrainFixture(t, name)

	require.NotEmpty(t, mgr.deferredReEmitIDs(gt, name), "PRECONDITION: there is work to converge")
	require.Less(t, len(mgr.deferredReEmitIDs(gt, name)), len(mask),
		"PRECONDITION: the mask must need MORE than one tick, or convergence is untested")

	// KNOWN POSITIVE FOR THE PROBE, taken on the first tick: a drain that really
	// re-emits partitions emits group_rebuild_begin. Without this the silence asserted
	// after convergence is indistinguishable from a probe pointed at nothing.
	first := captureDrainLog(t, func() { require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name)) })
	require.Contains(t, first, "group_rebuild_begin",
		"CONTROL: a drain with mask work outstanding MUST re-emit partitions, or the silence below means nothing")

	ticks := 1
	for len(persistedMask(t, mgr, gt, name)) > 0 {
		require.Less(t, ticks, 64, "the drain must converge, not grind: the mask is still non-empty")
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
		ticks++
	}
	require.Greater(t, ticks, 1, "convergence must have taken more than one tick, or the budget was never the binding constraint")
	t.Logf("the mask of %d ids converged in %d ticks at a budget of %d partitions per tick",
		len(mask), ticks, deferredReEmitPartitionBudget)

	// THE STEADY STATE. Not merely "the record is empty" — an empty record with a
	// still-firing drain is a different bug with the same record state.
	quiet := captureDrainLog(t, func() { require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name)) })
	require.NotContains(t, quiet, "group_rebuild_begin",
		"a drain with nothing outstanding must re-emit NO partition — a lane that keeps firing on the "+
			"same discharged cause is the shape this deferral must not have")
	require.Empty(t, persistedMask(t, mgr, gt, name), "and the record stays empty")
}

// captureDrainLog runs fn with the default logger redirected and returns what it wrote.
// The package's partition rebuilds announce themselves on the default logger, so this is
// the only seam that can tell "re-emitted nothing" from "re-emitted the same content".
// A re-emit converging to an identical content hash leaves segment ids unchanged, which
// is exactly why an id-diff probe would report silence for a drain that ran.
func captureDrainLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// TestDrainOnAnUnloadedPoolLeavesTheDurableRecordUntouched reproduces the state a
// scheduling-keyed trim resurrects from, and it is an ORDINARY state rather than an
// exotic one: the first read of a fresh process, before anything has loaded a pool.
//
// Without the corpus-loaded gate the arithmetic collapses — an unloaded pool reports a
// resident count of zero, BucketCountFor derives ONE partition, every masked id maps to
// it, that single partition fits inside any budget, and a trim keyed on scheduling
// empties the whole record. The next import then brings every deleted document back.
func TestDrainOnAnUnloadedPoolLeavesTheDurableRecordUntouched(t *testing.T) {
	requireMeasurementRun(t)
	ctx := context.Background()
	gt := kgtypes.GraphCode
	const name = "deferred-unloaded"
	dir := t.TempDir()

	writer := closeOnCleanup(t, NewManager(dir, 0))
	docs := bothFormatDocs(twoPartitionFixtureN, "defunload-")
	require.NoError(t, writer.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, writer.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, writer.ReEmitDirtyBuckets(ctx, gt, name))
	victim := docs[0].ID
	require.NoError(t, writer.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim}))

	// A SECOND MANAGER OVER THE SAME L2 DIRECTORY, and nothing loads, searches or
	// writes through it before the drain — the ordinary state of a fresh process.
	fresh := closeOnCleanup(t, NewManager(dir, 0))
	require.Zero(t, fresh.managerFor(gt, name).engine.ResidentDocCount(),
		"PRECONDITION: the vector pool must be UNLOADED, or this test exercises the loaded path it exists to distinguish from")
	require.Zero(t, fresh.bm25ManagerFor(gt, name).engine.ResidentDocCount(),
		"PRECONDITION: the field pool must be unloaded for the same reason")
	before := persistedMask(t, fresh, gt, name)
	require.Contains(t, before, victim,
		"PRECONDITION: the durable record must carry the deleted id, or there is nothing a wrongful trim could drop")
	require.NotEmpty(t, fresh.graphTombstones(gt, name),
		"PRECONDITION: the mask must hydrate from disk on the first read — that hydration is what a "+
			"scheduling-keyed trim would then discharge against an unloaded pool")

	require.NoError(t, fresh.ReEmitDirtyBuckets(ctx, gt, name))

	require.ElementsMatch(t, before, persistedMask(t, fresh, gt, name),
		"a drain on an unloaded pool must leave the durable record EXACTLY as it found it")
	require.Contains(t, persistedMask(t, fresh, gt, name), victim,
		"and the deleted id must still be masked, or the next import resurrects it")
}

// TestDeferredTrimDoesNotFireWhenTheDrainsPersistFails is the ORDERING property, and no
// other test on this step can see it: every one of them runs the success path, where a
// trim placed before the persist and a trim placed after it are indistinguishable.
func TestDeferredTrimDoesNotFireWhenTheDrainsPersistFails(t *testing.T) {
	requireMeasurementRun(t)
	ctx := context.Background()
	mgr, gt, name, _, ic, docs := deleteRetryFixtureOfSize(t, "deferred-persistfail", twoPartitionFixtureN)

	// THE MASKED IDS ARE REAL CORPUS DOCUMENTS, and they have to be: a partition
	// re-emitted without losing a member rebuilds to the SAME content hash, so it is
	// already in L2, persistResident's diff is empty and there is no write for the
	// injection to fail. This is also the production shape for an id that entered the
	// mask by a route which re-emitted nothing — the rebuild driver's own scan seeding,
	// or the delta consumer's already-known branch.
	var mask []searchengine.ExternalID
	for _, d := range docs[:32] {
		mask = append(mask, d.ID)
	}
	require.NoError(t, mgr.sealDeletedIDs(gt, name, mask))
	require.NotEmpty(t, mgr.deferredReEmitIDs(gt, name),
		"PRECONDITION: the selector must offer work, or the drain below never reaches a write to fail")

	ic.failPut = true
	err := mgr.ReEmitDirtyBuckets(ctx, gt, name)
	require.Error(t, err, "a drain whose L2 write fails must SURFACE the failure, never absorb it")
	require.ErrorIs(t, err, errInjectedPutFailure, "and it must be the disk's error rather than one the drain invented")

	require.ElementsMatch(t, mask, persistedMask(t, mgr, gt, name),
		"the mask must survive a drain that did not make its re-emit durable: trimming an id whose "+
			"rebuilt partition never reached disk drops the only thing masking a document the blob still carries")
	require.ElementsMatch(t, mask, mgr.graphTombstones(gt, name),
		"and the in-memory seed must be intact too, or this process stops masking what disk still records")
}

// TestDeferredSelectorDeclinesBelowTheResidencyFloor is the gate's own unit: the floor
// is what makes every partition number in this file meaningful, and a change that
// removes it would leave every other test here green.
func TestDeferredSelectorDeclinesBelowTheResidencyFloor(t *testing.T) {
	requireMeasurementRun(t)
	ctx := context.Background()
	gt := kgtypes.GraphCode
	const name = "deferred-floor"

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	small := bothFormatDocs(residentBackstopFloor-1, "deffloor-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, small))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, small))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
	require.NoError(t, mgr.sealDeletedIDs(gt, name, []searchengine.ExternalID{"deffloor-masked-0", "deffloor-masked-1"}))

	require.Less(t, mgr.managerFor(gt, name).engine.ResidentDocCount(), residentBackstopFloor,
		"PRECONDITION: the pool must sit BELOW the floor, or this asserts the loaded path")

	var served []searchengine.ExternalID
	declined := captureDrainLog(t, func() { served = mgr.deferredReEmitIDs(gt, name) })
	require.Nil(t, served,
		"below the floor the selector declines: a count derived from a near-empty pool collapses every "+
			"masked id onto one partition, and serving that would discharge the whole mask on one publish")

	// THE DECLINE MUST BE ANNOUNCED, and this is the half that matters operationally. A
	// graph whose field corpus never clears the floor is declined on EVERY tick forever
	// while its blob size and its mask grow, so a silent decline is an unbounded leak with
	// nothing in the logs pointing at it. The record has to carry both resident counts,
	// the floor they failed, and how much work is outstanding — an operator cannot act on
	// "declined" alone.
	require.Contains(t, declined, "deferred re-emit DECLINED",
		"a decline must reach the log, or a permanently-declining graph is invisible")
	require.Contains(t, declined, "floor=64", "the record must name the floor that was failed")
	require.Contains(t, declined, "masked=2", "and how many ids are left outstanding by the decline")
	require.Contains(t, declined, "hnsw_resident=", "and the vector pool's reading")
	require.Contains(t, declined, "bm25_resident=", "and the field pool's, since either can be the one below")

	// KNOWN POSITIVE, SAME INSTRUMENT: the same selector on a pool that clears the floor
	// returns work AND announces the serving side, so the nil above is the gate firing
	// rather than the selector being inert, and the silence is a decline rather than a
	// logger that captured nothing.
	loaded, loadedGT, _ := deferredDrainFixture(t, "deferred-floor-positive")
	var loadedServed []searchengine.ExternalID
	serving := captureDrainLog(t, func() {
		loadedServed = loaded.deferredReEmitIDs(loadedGT, "deferred-floor-positive")
	})
	require.NotEmpty(t, loadedServed)
	require.Contains(t, serving, "deferred re-emit serving masked partitions",
		"CONTROL: the serving side announces itself, so the decline above is a different record rather "+
			"than an absent logger")
	require.Contains(t, serving, "partitions_served=8", "and it names how many partitions this tick took")
	require.Contains(t, serving, "partitions_outstanding=16",
		"and how many the mask still spans — the number whose fall over ticks IS convergence")
	require.NotContains(t, serving, "DECLINED", "CONTROL: a graph above the floor is not declined")
}

// TestDeferredReEmitSaysNothingAboutAConvergedGraph pins the other half of the
// observability contract: the lane is announced when it has work or is refusing work,
// and SILENT otherwise.
//
// Without this, the honest fix for a mute lane — log everything — trades an invisible
// leak for a line per graph per tick on every converged graph in the process, which is
// the noise that gets logging turned down and makes the DECLINE above invisible again.
func TestDeferredReEmitSaysNothingAboutAConvergedGraph(t *testing.T) {
	requireMeasurementRun(t)
	ctx := context.Background()
	gt := kgtypes.GraphCode
	const name = "deferred-quiet"

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	docs := bothFormatDocs(budgetFixtureN, "defquiet-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
	require.Empty(t, persistedMask(t, mgr, gt, name),
		"PRECONDITION: the graph must have nothing masked, which is the converged state")

	quiet := captureDrainLog(t, func() { require.Nil(t, mgr.deferredReEmitIDs(gt, name)) })
	require.NotContains(t, quiet, "deferred re-emit",
		"a graph with an empty mask must emit NO deferred-re-emit record at all — neither a decline nor "+
			"a serving line — or every converged graph in the process pays a line per tick")
}
