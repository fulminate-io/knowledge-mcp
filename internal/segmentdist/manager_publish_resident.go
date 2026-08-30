// SPDX-License-Identifier: Apache-2.0

// manager_publish_resident.go — the DURABILITY path: write this engine's resident
// segments into the L2 disk cache, and refuse a prospective layer that would retire
// a populated one in favor of nothing.
//
// The seam is the MODEL, not the line count: everything here decides what the LIVE
// SET IS, while what stays behind decides which stale ids to DELETE.

package segmentdist

import (
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// THE L2 WRITE ATTEMPT POLICY, in one place because it is a policy rather than a
// tuning constant and the two values only mean anything against each other.
//
// singleL2WriteAttempt is what every path has always done and what every path
// except the delete re-emit keeps doing: write once, return the error. It is named
// rather than spelled 1 at each call site so a reader of those sites sees a stated
// policy instead of an unexplained literal.
//
// l2WriteAttemptsOnDelete is the delete re-emit's bound. It exists because the
// delete re-emit is ENTIRELY LOCAL — the rebuild is in memory, the swap is this
// process's own compare-and-swap, and the L2 disk write is the only step that can
// fail for an operational rather than a programming reason. A local disk write
// failing is rare and is usually transient, so a couple of further attempts is
// worth more than an immediate report. It is deliberately SMALL: this is a net for
// a rare disk error, not an availability mechanism, and a delete that cannot be
// made durable has to say so promptly rather than block on a disk that is not
// coming back.
const (
	singleL2WriteAttempt    = 1
	l2WriteAttemptsOnDelete = 3
)

// THE ABORTED-RECLAIM REPORT POLICY, stated here beside the write-attempt policy
// because the two are the same kind of thing: a per-caller decision about what a
// partition re-emit owes its caller, named rather than spelled as a bare literal at
// each call site.
//
// A re-emit's group swap fires the engine's merge hook, and reclaimMerged ABORTS on a
// failed Put of the consolidated blob — leaving the superseded constituents on disk,
// logging at ERROR, and returning nothing (its signature is the engine's
// OnMergeFunc, which has no return). surfaceAbortedReclaim makes the driver ASK for
// that record and fold it into its own error; logAbortedReclaimOnly leaves the abort
// exactly as it has always been, a log line and nothing more.
//
// IT IS THE DELETE PATH'S ALONE, and the scoping is behavioral rather than cautious.
// Two other drivers reach the merge hook, and surfacing there would CHANGE WHAT THEIR
// CALLERS OBSERVE. ReEmitRebuiltDelta's finalize takes the flag and holds it at
// logAbortedReclaimOnly: an error from it reports swapped=false, which the rebuild
// driver reads as a failed finalize. The reconcile drain does not take the flag at
// all — drainFormat calls replaceBucketGroups and persistResident directly rather
// than through this function — and an error there would return from
// ReEmitDirtyBuckets BEFORE clearDirty, retaining the whole write backlog and
// re-triggering the drain on every tick. Neither caller's failure model was examined
// here, which is the same argument replaceBucket records for the write-attempt bound.
const (
	surfaceAbortedReclaim = true
	logAbortedReclaimOnly = false
)

// l2WriteRetryBackoff is the pause before the FIRST retry; each further retry waits
// a multiple of it. Short enough that an exhausted bound reports promptly, long
// enough that a retry is not simply the same instant re-tried.
const l2WriteRetryBackoff = 20 * time.Millisecond

// persistResident makes this engine's resident set DURABLE: it exports the resident
// segments, drops the ones whose content hash the L2 cache already holds, and writes
// the rest. It returns how many blobs it newly wrote.
//
// IT MAKES ONE WRITE ATTEMPT, which is what every caller but the delete re-emit
// wants. The retrying form is persistResidentWithWriteAttempts below; this is its
// singleL2WriteAttempt case, so the reconcile drain, the rebuild finalize and Flush
// keep exactly the behaviour they have always had.
//
// THE SHAPE OF THE DIFF IS WHAT SURVIVED from its predecessor. Then the diff
// suppressed blobs the SERVER already held and the write to L2 was a side effect of
// the upload response; now the diff asks the L2 cache directly and the write is the
// whole point. The consequence is worth stating because it inverts a documented
// hazard: under the old coupling only SHIPPED blobs were cached, so a
// content-hash-suppressed blob was published but never written locally and the cache
// ran short of the published set. Keying the diff on cache presence makes that
// impossible — a blob is skipped precisely because it is already on disk.
//
// There is no manifest and no refcount-GC, so nothing here can reap anything: the
// destructive act that the old publish gate protected against has moved to the layer
// swap, and prospectiveLayerOK guards it there.
func (m *distManager[Q, S]) persistResident() (int, error) {
	return m.persistResidentWithWriteAttempts(singleL2WriteAttempt)
}

// persistResidentWithWriteAttempts is persistResident with the number of L2 write
// attempts made explicit.
//
// THE EXPORT AND THE DIFF RUN ONCE, and only the WRITE is repeated. Re-exporting per
// attempt would re-derive a resident set that cannot have changed — nothing here
// mutates the engine — and would emit the write-diff record once per attempt, which
// would make an operator read one delete's single write as several.
//
// A SUCCESS ON A LATER ATTEMPT IS INDISTINGUISHABLE FROM A FIRST-TRY SUCCESS to
// every caller: the same blob count and a nil error. That is required rather than
// merely tidy, because the delete's shipped-corpus verdict travels on this error
// alone — the tools layer appends its not-durable qualifier exactly when
// DeleteFromBuckets returns non-nil — so anything else surfaced on a recovered write
// would tell a caller their durable delete was not durable.
//
// AN EXHAUSTED BOUND RETURNS THE LAST ATTEMPT'S ERROR UNWRAPPED, so that qualifier
// names the disk's own failure rather than one this loop invented.
func (m *distManager[Q, S]) persistResidentWithWriteAttempts(writeAttempts int) (int, error) {
	all := m.engine.Export()

	var diff []searchengine.SegmentBlob
	for _, b := range all {
		if _, present := m.cache.sizeOf(b.ID); present {
			continue
		}
		diff = append(diff, b)
	}

	// WRITTEN VERSUS SKIPPED-AS-PRESENT, and this line is the one an operator needed
	// and did not have. Without both numbers on one line the write count reads as the
	// whole story: the 78-second rebuild that truncated a served corpus to a quarter
	// emitted no line at all, and the only detector was a human doing arithmetic after
	// a restart.
	slog.Info("segmentdist: L2 write diff resolved",
		"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
		"format", m.format, "resident", len(all), "written", len(diff),
		"skipped_as_present", len(all)-len(diff))

	if err := m.writeNewBlobsToL2WithAttempts(diff, writeAttempts); err != nil {
		return 0, err
	}
	return len(diff), nil
}

// writeNewBlobsToL2WithAttempts writes the blobs, retrying the WHOLE write up to
// attempts times and returning the last attempt's error when the bound is exhausted.
//
// RE-OFFERING THE ALREADY-WRITTEN BLOBS COSTS NOTHING AND IS WHY THE RETRY IS THE
// WHOLE SLICE. writeNewBlobsToL2 aborts at the first blob it cannot write, so a
// failed attempt may have landed some of them; the cache is content-addressed, so
// re-Putting a blob it already holds is the same bytes under the same name. Resuming
// from the failing element instead would need the loop to report where it stopped,
// for no behavioral gain.
//
// attempts BELOW ONE STILL WRITES ONCE. The loop is written so the write is
// unconditional and the retries are what the bound governs — a caller that computed
// a zero must not silently turn a durability step into a no-op that reports success.
func (m *distManager[Q, S]) writeNewBlobsToL2WithAttempts(
	blobs []searchengine.SegmentBlob, attempts int,
) error {
	var err error
	for attempt := 1; ; attempt++ {
		if err = m.writeNewBlobsToL2(blobs); err == nil {
			return nil
		}
		if attempt >= attempts {
			return err
		}
		slog.Warn("segmentdist: L2 write failed; retrying",
			"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
			"format", m.format, "blobs", len(blobs), "attempt", attempt, "attempts", attempts,
			"error", err)
		time.Sleep(time.Duration(attempt) * l2WriteRetryBackoff)
	}
}

// prospectiveLayerOK is the WIPE GUARD. It returns (true, "") when a built layer may
// replace the current one, or (false, reason) when the swap must be REFUSED.
//
// THE CHECK IS THAT THE LAYER IS NON-EMPTY, and that single check is the whole of the
// corpus-wipe property: an empty live set must NEVER drive a destructive sweep. The
// destructive act here is engine.ReplaceLayer, which retires the ENTIRE prior layer;
// an empty prospective layer would therefore replace a populated corpus with nothing
// and leave the engine serving an empty set until a restart reloaded it.
//
// IT MUST BE CALLED BEFORE engine.ReplaceLayer. A gate that runs after the swap
// passes every behavioral test and protects nothing: reads would already be served
// from the degenerate layer.
//
// The two checks that used to sit beside it are gone with the mechanism each judged.
// The coverage-ratio floor compared the layer against the PRIOR MANIFEST's summed doc
// count, and there is no manifest to compare against. The subset-completeness check
// asked whether the live set was a subset of the source's List(0), and List(0) is now
// the L2 cache the layer was just written to — it would compare the cache against
// itself.
//
// ONE INSIGHT FROM THE RATIO ARM SURVIVES ITS DELETION, carried here at the merge so it
// is not lost with the code that expressed it. A parallel lane had reached the same
// hazard from the other side and fixed it by PROVENANCE: a rebuild whose scan covered
// the whole embedded corpus was allowed to bypass the ratio, because "the full
// derivation from the graph IS the truth and the manifest is the thing being
// corrected" — otherwise a manifest inflated by duplication vetoes its own correction
// and keeps growing by the mechanism that inflated it. That lane's machinery is gone
// with the manifest it compared against, but its PRINCIPLE is the one this changeset
// also acts on, and more strongly: completeness is no longer a flag a caller threads
// in to be trusted, it is a precondition the driver cannot skip, because a scan that
// did not run to exhaustion returns an error and never reaches staging or the
// finalize at all.
//
// SO THIS IS NOT THE GUARD AGAINST A SLIVER REPLACING A CORPUS, and a reader who
// assumes it is will build the wrong thing next. A one-segment layer holding four
// documents passes here against a resident thousand. That shape is guarded ONE LAYER
// UP, and by evidence rather than by size: the rebuild driver's scan returns an error
// on any page failure and terminates only on an empty page, so a run whose drain did
// not complete never reaches staging or this finalize at all
// (tools.scanRebuildSegmentsAs, pinned by TestTruncatedDrainNeverReachesTheFinalize).
// A numeric band HERE would be the wrong instrument regardless of where it sat: "far
// fewer documents than last time" is also what a legitimate mass deletion looks like,
// so a band tuned to catch the wipe refuses the correct rebuild.
//
// A REFUSAL LEAVES THE RESIDENT SET UNTOUCHED. By the time this is consulted the
// built blobs have been written to L2, so a refusal leaves them on disk referenced by
// no layer. Nothing needs un-stamping — the bookkeeping sets a refusal used to have to
// unwind no longer exist.
//
// THOSE BLOBS ARE NOT REAPED BY PruneCache, and this comment used to say they were.
// They went through the pool's L2 cache, so they are in its index, and the prune's
// live set is force-loaded from that same index — see the reap paragraph at the top of
// prune_cache.go. A refused layer's blobs occupy disk until a later write supersedes
// them by content id.
func (m *distManager[Q, S]) prospectiveLayerOK(built []searchengine.SegmentBlob) (bool, string) {
	liveSet := make(map[searchengine.SegmentID]struct{}, len(built))
	for _, b := range built {
		liveSet[b.ID] = struct{}{}
	}
	if len(liveSet) == 0 {
		return false, "empty live set"
	}
	return true, ""
}

// exportedIDs is the id set of an engine Export, used to compute what a layer swap
// superseded: the ids present BEFORE the swap and absent after it.
//
// NAMED exportedIDs, NOT exportedIDSet, because reclaim_test.go already declares a
// package-level exportedIDSet — a generic helper taking a *distManager rather than a
// blob slice. Two package-level functions of that name do not compile, and the
// collision would surface only when the test binary is built.
func exportedIDs(blobs []searchengine.SegmentBlob) map[searchengine.SegmentID]struct{} {
	ids := make(map[searchengine.SegmentID]struct{}, len(blobs))
	for _, b := range blobs {
		ids[b.ID] = struct{}{}
	}
	return ids
}
