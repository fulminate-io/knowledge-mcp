package searchengine

import "time"

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
// most one merge per wake. Stopped by Close (closing e.stop). Lifecycle: one
// goroutine per engine, lives until Close.
func (e *SegmentedIndex[Q, S]) startMerger() {
	go func() {
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

// doMerge consolidates the chosen entries via format.Merge, which reads the LIVE
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

	merged, err := e.format.Merge(segs, accept)
	if err != nil {
		return
	}
	entry, err := e.newEntry(merged, nil)
	if err != nil {
		return
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
		bytes, err := entry.payload.Encode()
		if err != nil {
			return
		}
		removed := make([]SegmentID, 0, len(remove))
		for id := range remove {
			removed = append(removed, id)
		}
		e.opts.OnMerge(MergeResult{
			Removed: removed,
			Merged: SegmentBlob{
				ID:         entry.meta.ID,
				Format:     e.format.Name(),
				Generation: entry.meta.Generation,
				DocCount:   entry.meta.DocCount,
				Bytes:      bytes,
			},
		})
	}
}

// Close stops the background merge goroutine. Idempotent.
func (e *SegmentedIndex[Q, S]) Close() {
	e.stopOnce.Do(func() { close(e.stop) })
}

// MergeCount reports how many background merges have completed.
func (e *SegmentedIndex[Q, S]) MergeCount() uint64 {
	return e.mergeCnt.Load()
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
