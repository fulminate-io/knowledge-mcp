package searchengine

import (
	"errors"
	"log/slog"
	"time"
)

// Metrics is a point-in-time snapshot of engine health.
type Metrics struct {
	SegmentCount int
	DeadRatio    float64
	MergeCount   uint64
}

// mergeTickInterval bounds how often the background merger re-evaluates the
// trigger policy when no write signal arrives.
const mergeTickInterval = 50 * time.Millisecond

// startMerger launches the background merge goroutine. It wakes on a ticker or
// on a write signal (Add/Delete), evaluates the trigger policy, and performs at
// most one merge per wake. Stopped by Close (closing e.stop), which then WAITS for
// this goroutine to exit. Lifecycle: one goroutine per engine, lives until Close.
func (e *SegmentedIndex[Q, S]) startMerger() {
	go func() {
		// FIRST statement, so e.done closes on EVERY exit path — that channel is
		// the only thing Close can wait on, and a return that skipped it would
		// hang Close forever rather than merely failing to join.
		defer close(e.done)
		ticker := time.NewTicker(mergeTickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.stop:
				return
			case <-ticker.C:
				e.maybeMerge()
			case <-e.mergeSignal:
				e.maybeMerge()
			}
		}
	}()
}

// signalMerge nudges the background merger without blocking the caller.
func (e *SegmentedIndex[Q, S]) signalMerge() {
	select {
	case e.mergeSignal <- struct{}{}:
	default:
	}
}

// maybeMerge evaluates the trigger and performs one merge if warranted. Runs
// only on the background goroutine, so at most one merge is in flight.
func (e *SegmentedIndex[Q, S]) maybeMerge() {
	set := e.set.Load()
	chosen := e.pickMergeTargets(set)
	if len(chosen) < 1 {
		return
	}
	// A single dirty segment is worth merging only to reclaim dead docs; multiple
	// segments are merged to cut the segment count. Either way one consolidated
	// all-live segment results.
	e.doMerge(chosen)
}

// pickMergeTargets returns the entries to consolidate, or nil if no trigger
// fires. Trigger: any segment whose dead ratio >= DeletesPctAllowed, OR the
// segment count exceeds SegmentCountTarget (in which case all entries merge down).
func (e *SegmentedIndex[Q, S]) pickMergeTargets(set *segmentSet[Q, S]) []*segmentEntry[Q, S] {
	if len(set.entries) == 0 {
		return nil
	}

	if len(set.entries) > e.opts.SegmentCountTarget {
		return set.entries
	}

	var dirty []*segmentEntry[Q, S]
	for _, entry := range set.entries {
		if entry.meta.DocCount == 0 {
			continue
		}
		dead := entry.live.DeadCount()
		ratio := float64(dead) / float64(entry.meta.DocCount)
		if ratio >= e.opts.DeletesPctAllowed {
			dirty = append(dirty, entry)
		}
	}
	return dirty
}

// doMerge consolidates the chosen entries via mergeEntry, whose format.MergeTo reads the LIVE
// INDEXED data directly from each sealed segment (no source Documents — decoded
// and locally-built segments are merge-equivalent), keeping only members the
// accept predicate marks live. The consolidated segment is all-live; it replaces
// the chosen entries in one CAS swap. In-flight readers keep the old snapshot.
func (e *SegmentedIndex[Q, S]) doMerge(chosen []*segmentEntry[Q, S]) {
	segs := make([]Segment[Q, S], len(chosen))
	accept := make([]func(ExternalID) bool, len(chosen))
	remove := make(map[SegmentID]bool, len(chosen))
	for i, entry := range chosen {
		segs[i] = entry.payload
		accept[i] = func(entry *segmentEntry[Q, S]) func(ExternalID) bool {
			return func(id ExternalID) bool {
				ord, ok := entry.members[id]
				return ok && entry.live.Live(ord)
			}
		}(entry)
		remove[entry.meta.ID] = true
	}

	// ABANDON BEFORE THE EXPENSIVE STEP once Close has been signaled. This is what
	// bounds Close's join to at most one in-flight merge step instead of a whole
	// merge, and it needs no new machinery: returning here without publishing is an
	// exit doMerge ALREADY takes on a mergeEntry error, so the caller-visible
	// outcome — nothing published, constituents untouched — is one this function
	// already produced.
	select {
	case <-e.stop:
		return
	default:
	}

	entry, err := e.mergeEntry(segs, accept)
	if err != nil {
		// A FAILED MERGE USED TO VANISH HERE, and a CONTAINED corruption vanishing
		// is worse than one that crashed: the merge loop re-selects the same
		// constituents on its next tick, re-reads the same bad bytes, and repeats
		// forever with nothing in the log to say why the corpus never consolidates.
		// Containment turned a loud crash into an invisible spin.
		//
		// The corruption arm is routed to the OWNER as well as to the log, because
		// the owner is what quarantines the file; without that the loop keeps its
		// perfect record of failure and no disposition is ever taken. A merge that
		// failed for any other reason — a full disk, a mapping refusal — is logged
		// and left alone, because it is not a segment defect and there is nothing
		// to quarantine.
		if corrupt, ok := errors.AsType[*CorruptSegmentError](err); ok {
			slog.Error("segment merge aborted by a corrupt constituent",
				"error", err,
				"constituents", len(segs),
				"note", "these segments cannot consolidate until the corrupt one is quarantined and rebuilt")
			e.reportCorrupt(corrupt)
			return
		}
		slog.Warn("segment merge failed", "error", err, "constituents", len(segs))
		return
	}
	// THE CONSOLIDATED SEGMENT REMEMBERS WHAT IT REPLACED, in its own STORED BYTES
	// (supersession.go). That is what lets a cold load decline these constituents with
	// no external state: the stored corpus is all a restarted process has, and until
	// this record existed it said only "here are some blobs".
	//
	// ITS COHORT IS ITSELF. A background merge publishes ONE output and that output
	// carries every live member of every constituent it consumed, so its presence alone
	// is proof enough to decline them.
	removed := sortedSegmentIDs(remove)
	stampSupersession(entry, removed, []SegmentID{entry.meta.ID})

	// And again before publishing: a merge that finished after the close was
	// signaled must not swap itself in, because publishing is what makes the
	// OnMerge reclaim below fire.
	select {
	case <-e.stop:
		return
	default:
	}

	// Publish via CAS. If the set changed under us (concurrent Add/Import), the
	// chosen entries by ID still apply: withReplaced drops them by SegmentID and
	// appends the consolidated entry. Retry on lost CAS, re-reading the set.
	for {
		cur := e.set.Load()
		next := cur.withReplaced(e.format, remove, entry)
		if e.set.CompareAndSwap(cur, next) {
			break
		}
	}
	e.mergeCnt.Add(1)
	// AND THE SETTLE COUNTER IS ARMED HERE, ONE LINE AFTER THE PUBLISH COUNTER, so
	// that from this point on every way out of this function counts the merge as
	// settled: the hook fired and returned, the hook was skipped because OnMerge is
	// nil, or the blobParts failure below returned without firing it at all. A
	// counter incremented at each of those arms by hand would be one arm behind the
	// next author who adds a fourth; a deferred increment registered at the publish
	// cannot be.
	//
	// WHAT THE PAIR MEANS TO A READER: mergeCnt says a merge is VISIBLE (its
	// segments are swapped in), settleCnt says a merge is FINISHED (nothing this
	// function started is still running). Between them sits the reclaim window — the
	// interval in which the consolidated segment is live and its owner has not yet
	// written it to L2 — which is precisely what an observer that wants to inspect
	// post-merge state has to wait out. Neither counter is read by the merge path.
	//
	// A PANICKING HOOK STILL SETTLES, because deferred calls run as a panic unwinds.
	// That is the direction to fail in: the merger goroutine is dying either way,
	// and an observer waiting on settle should be released to fail on its own
	// assertions rather than hang until its deadline on a process that is going down.
	defer e.settleCnt.Add(1)

	// Surface the supersession event to the owner (when installed) so it can
	// reclaim the superseded constituents' L2 disk files. Runs AFTER the publish,
	// holding NO engine lock (only the lock-free CAS above touched e.set), on the
	// background merger goroutine — so it cannot re-enter or deadlock the merge
	// tick. The merged blob mirrors the Export encode-shape (distribution.go:11):
	// Generation is 0 here (newEntry never stamps it), which is harmless because
	// the L2 cache keys by id + raw bytes and ignores Generation (cache.go). On an
	// Encode error we simply do NOT fire — a Remove without a durable Put of the
	// merged blob would be a fresh false-prune, so an empty/partial Merged is worse
	// than no callback (the next ship/reconcile still bounds the server set).
	if e.opts.OnMerge != nil && len(remove) > 0 {
		envelope, payload, err := entry.blobParts()
		if err != nil {
			return
		}
		e.opts.OnMerge(MergeResult{
			Removed: removed,
			Merged: SegmentBlob{
				ID:         entry.meta.ID,
				Format:     e.format.Name(),
				Generation: entry.meta.Generation,
				DocCount:   entry.meta.DocCount,
				Bytes:      payload,
				Envelope:   envelope,
				// Bytes come from a resident entry's payload, which on a mapped
				// segment IS the mapping. The entry is reachable from the
				// published set for this hook's duration, but only incidentally
				// and not by anything the compiler enforces — pinning it states
				// the guarantee instead of relying on the coincidence.
				keepAlive: entry,
			},
		})
	}
}

// Close stops the background merge goroutine and BLOCKS until it has exited.
// Idempotent.
//
// THE WAIT IS THE POINT, and it is a caller-visible contract rather than an
// implementation detail. Close used to only signal, so a merge already in flight
// ran on afterwards — and a completed merge fires OnMerge, which in production is
// segmentdist's reclaimMerged: a cache.Put that WRITES A FILE. An owner that
// closed the engine and then removed its cache directory could have a blob written
// underneath it. Because Close now joins, "Close returned" means "no further
// OnMerge can fire", which is what an owner tearing down that directory needs.
//
// IT IS UNCONDITIONAL — there is deliberately no timeout that abandons the
// goroutine. A join that gives up reintroduces exactly the race it exists to
// remove, just less often; a caller that cannot wait forever bounds its OWN call
// (the daemon wraps this in a bounded shutdown stage), which leaves the engine's
// guarantee intact for everyone else.
//
// THE BOUND ON HOW LONG IT WAITS IS STRUCTURAL, not a deadline: doMerge checks
// e.stop at its coarse boundaries, so a merge in progress abandons before the
// expensive format.Merge or before publishing, and the wait is at most one
// in-flight merge step rather than a whole merge chain.
//
// REENTRANCY WARNING FOR FUTURE HOOK AUTHORS: OnMerge runs ON the merge goroutine,
// so an OnMerge that called Close would deadlock — Close would wait for the
// goroutine that is waiting on Close. The only production hook
// (segmentdist/manager_factory.go wiring reclaimMerged) does not, and a new one
// must not either.
func (e *SegmentedIndex[Q, S]) Close() {
	e.stopOnce.Do(func() { close(e.stop) })
	<-e.done
}

// MergeCount reports how many background merges have completed.
func (e *SegmentedIndex[Q, S]) MergeCount() uint64 {
	return e.mergeCnt.Load()
}

// HasMergeHook reports whether a merge-completion callback is installed on this
// engine. It is pure observability over construction-time Options, in the same
// vein as MergeCount and Metrics: an owner that installs a hook conditionally
// can confirm which variant it built, and a policy change that disarms the
// automatic trigger can be distinguished from one that drops the callback.
func (e *SegmentedIndex[Q, S]) HasMergeHook() bool { return e.opts.OnMerge != nil }

// SettledMergeCount reports how many background merges have RUN TO COMPLETION,
// where completion means doMerge returned — publish, plus the OnMerge hook on the
// arms that fire it, plus the arms that legitimately skip it.
//
// IT IS THE OTHER HALF OF MergeCount, AND THE HALF AN OBSERVER USUALLY WANTS.
// MergeCount increments at the CAS publish, BEFORE the hook that reclaims the
// superseded segments' disk files runs, so a reader that treats a stable MergeCount
// as "the merge is done" is reading a counter that moved one step too early: the
// consolidated segment is live while its owner has not yet written it. When
// SettledMergeCount equals MergeCount there is no merge whose completion work is
// still in flight.
//
// IT IS NOT A TERMINAL-STATE PREDICATE ON ITS OWN. The merger returns to its select
// between merges, so the two counters are equal in every lull of a chain that has
// more merges to do. An observer that needs "nothing more will happen" pairs this
// with MergeEligible, which answers whether another merge is about to start.
//
// Pure observability, in the same vein as MergeCount, Metrics and HasMergeHook: the
// merge path never reads it.
func (e *SegmentedIndex[Q, S]) SettledMergeCount() uint64 { return e.settleCnt.Load() }

// MergeEligible reports whether the trigger policy would select something to merge
// against the currently published set — that is, whether the background merger
// would start a merge on its next tick if the corpus stayed exactly as it is.
//
// IT ASKS THE POLICY RATHER THAN RESTATING IT. The answer is pickMergeTargets over
// the published set, the same call maybeMerge makes, so the predicate cannot drift
// away from the trigger it claims to describe. The cost of that is one slice
// allocation on the dead-ratio arm (pickMergeTargets appends the dirty entries);
// the alternative, a second copy of the trigger conditions that answers without
// allocating, buys that allocation back at the price of two definitions of "merge
// eligible" that a future policy change would have to keep in step by hand.
//
// IT IS A SNAPSHOT, NOT A GUARANTEE, and a caller that needs "nothing more will
// happen" also has to know that nothing is writing to the engine: any Add or Delete
// can make an ineligible corpus eligible again the moment after this returns.
//
// Pure observability: the merge path never reads it.
func (e *SegmentedIndex[Q, S]) MergeEligible() bool {
	return len(e.pickMergeTargets(e.set.Load())) > 0
}

// Metrics returns a point-in-time health snapshot. DeadRatio is the
// corpus-wide dead/total fraction.
func (e *SegmentedIndex[Q, S]) Metrics() Metrics {
	set := e.set.Load()
	var total, dead int
	for _, entry := range set.entries {
		total += entry.meta.DocCount
		dead += entry.live.DeadCount()
	}
	ratio := 0.0
	if total > 0 {
		ratio = float64(dead) / float64(total)
	}
	return Metrics{
		SegmentCount: len(set.entries),
		DeadRatio:    ratio,
		MergeCount:   e.mergeCnt.Load(),
	}
}
