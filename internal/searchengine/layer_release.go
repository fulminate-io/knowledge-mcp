// SPDX-License-Identifier: Apache-2.0

// layer_release.go — the PER-PARTITION release that rides a layer build: each partition
// is handed to a caller-supplied hook the moment it is encoded, and the mapping the hook
// returns replaces the encoder output the partition would otherwise hold until the whole
// layer was published.
//
// IT SITS BESIDE layer_swap.go RATHER THAN INSIDE IT, on the precedent mapping_lifetime.go
// sets for the resident case: the SWAP is about which segments the engine serves, and
// this is about where a segment's bytes live. The two are independent — a layer swap is
// correct with no release at all, and this file's whole contribution is a memory
// property — so keeping them apart keeps the swap's safety argument readable.
//
// WHY A HOOK AND NOT A CALL AFTERWARDS. BuildLayer accumulates every partition's entry
// AND its encoded payload before it returns, so a corpus deriving 128 partitions holds
// 128 encoded segments on the heap at the instant the process is at its largest —
// measured at 38.5 % of the client's live heap at a corpus-scale rebuild's crest. Every
// one of those bytes is already allocated by the time any post-build call could run.

package searchengine

import (
	"errors"
	"fmt"
)

// Unreleased reports the partitions still holding their encoder output on the Go heap,
// and the decode failures behind them. A build given no persist hook returns nil, nil:
// nothing was asked for and nothing is owed.
//
// IT IS THE CALLER'S OPERAND FOR CONVERGENCE. The publisher records these ids on the
// pending-remap machinery so a later consumer touch repairs them, exactly as the seal
// path does for a release its own pass could not complete.
func (b *BuiltLayer[Q, S]) Unreleased() ([]SegmentID, error) { return b.unreleased, b.releaseErr }

// LayerPartitionPersist is called with each partition's encoded blob the MOMENT that
// partition is built, before the next one is. It makes those bytes durable and returns
// them back as a MAPPING of the stored copy; BuildLayerReleasing then re-backs the
// partition's entry with that mapping, so the encoder's output becomes unreachable one
// partition at a time instead of at the end of the layer.
//
// WHY THE HOOK IS THE ONLY SHAPE THAT SERVES THE MEMORY PROPERTY. Without it this
// function accumulates every partition's entry AND its encoded payload before
// returning, so a corpus deriving 128 partitions holds 128 encoded segments on the heap
// at the instant the process is at its largest — measured at 38.5 % of the client's
// live heap at a corpus-scale rebuild's crest. No call placed AFTER the build can move
// that, because the bytes are all already allocated by the time it could run.
//
// ok=false LEAVES THE PARTITION HEAP-BACKED AND IS NOT A FAILURE: the mapping could not
// be made (the blob is not cached, the mmap is unavailable), the segment stays correct
// and durable, and the id is reported through Unreleased for the caller's own
// convergence. An ERROR, by contrast, is the DURABILITY write failing, and it aborts
// the build — a layer whose bytes are not on disk must never reach the swap.
//
// THE BLOB IT RECEIVES carries the partition's envelope and payload exactly as
// blobParts produced them, which is what the stored file holds; the blob it returns
// must carry the payload alone in Bytes, its envelope in Envelope, and the mapping's
// Release. Ownership of that Release passes to this function on return, on the same
// terms RemapResidentBatch takes it: it is attached to the entry that serves it, or
// freed here.
type LayerPartitionPersist func(blob SegmentBlob) (mapped SegmentBlob, ok bool, err error)

// BuildLayerReleasing is BuildLayer with a PER-PARTITION persist hook: each partition
// is made durable and re-backed by a mapping of its stored copy as soon as it is built,
// so the layer under construction holds ONE encoded segment on the heap rather than all
// of them. A nil hook is exactly BuildLayer.
//
// THE WRITE-BEFORE-SWAP INVARIANT IS STRENGTHENED, NOT WEAKENED. The reset's ordering
// rule is that every blob is durable before the single CAS that publishes the layer;
// writing per partition satisfies it strictly earlier, and the swap is still one CAS
// over a complete layer. What is new is only that a BUILT, not-yet-published entry can
// already be mapping-backed — which needs no CAS of its own precisely because nothing
// can read an unpublished entry.
//
// A PARTIAL BUILD THAT FAILS LEAVES BLOBS ON DISK that no layer references. That is the
// same state a refused layer leaves (see finalizeResetLayer's refusal paragraph), it is
// harmless, and it is not new bookkeeping to unwind: the cache is content-addressed, so
// a later build of the same bytes finds them present and writes nothing.
func (e *SegmentedIndex[Q, S]) BuildLayerReleasing(
	work []BucketWork, persist LayerPartitionPersist,
) (*BuiltLayer[Q, S], error) {
	// CAPTURE BEFORE BUILDING. The removal set must name what was resident when this
	// rebuild BEGAN, not what is resident when it finishes — see ReplaceLayer.
	old := e.set.Load()
	built := &BuiltLayer[Q, S]{
		engine:         e,
		capturedOldIDs: make([]SegmentID, 0, len(old.entries)),
		capturedOldSet: make(map[SegmentID]bool, len(old.entries)),
	}
	for _, entry := range old.entries {
		built.capturedOldIDs = append(built.capturedOldIDs, entry.meta.ID)
		built.capturedOldSet[entry.meta.ID] = true
	}

	for _, w := range work {
		if err := e.buildLayerPartition(built, w, persist); err != nil {
			return nil, err
		}
	}
	return built, nil
}

// buildLayerPartition builds ONE partition into built, releasing it to its mapping
// through persist when one is supplied.
//
// IT IS A HELPER RATHER THAN THE LOOP BODY so the release step has a name and the two
// failure dispositions can be stated where they are taken: a build, seal or encode
// failure aborts the layer, while a failed RELEASE only records the id.
func (e *SegmentedIndex[Q, S]) buildLayerPartition(
	built *BuiltLayer[Q, S], w BucketWork, persist LayerPartitionPersist,
) error {
	docs := dedupeDocsByID(w.Docs)
	if len(docs) == 0 {
		// A partition with no documents contributes no segment. It is not an error:
		// a corpus simply may not populate every partition of its derived count.
		return nil
	}
	seg, rep, err := e.format.Build(docs)
	e.reportDegrade(rep)
	if err != nil {
		return fmt.Errorf("searchengine: building partition %d of the replacement layer: %w", w.Bucket, err)
	}
	entry, err := e.newEntry(seg, nil, payloadBuilt)
	if err != nil {
		return fmt.Errorf("searchengine: sealing partition %d of the replacement layer: %w", w.Bucket, err)
	}
	// blobParts rather than payload.Encode, so this site obeys the same rule as
	// every other place an entry becomes bytes: what is stored is the payload plus
	// whatever supersession record the entry holds. A freshly built partition holds
	// none — a from-scratch build supersedes nothing by construction, and
	// ReplaceLayer deliberately stamps none either (see the paragraph there) — so
	// today this is byte-for-byte what payload.Encode returned.
	envelope, payload, err := entry.blobParts()
	if err != nil {
		return fmt.Errorf("searchengine: encoding partition %d of the replacement layer: %w", w.Bucket, err)
	}
	blob := SegmentBlob{
		ID:       entry.meta.ID,
		Format:   e.format.Name(),
		DocCount: entry.meta.DocCount,
		Bytes:    payload,
		Envelope: envelope,
		// Bytes come from a resident entry's payload. BuiltLayer happens to
		// hold the entries alongside the blobs today, but that is a
		// coincidence of this struct's shape rather than a guarantee, and
		// Blobs() hands out a copied slice that shares these bytes.
		keepAlive: entry,
	}

	if persist != nil {
		entry, blob, err = e.releaseBuiltPartition(built, entry, blob, persist)
		if err != nil {
			return fmt.Errorf("searchengine: persisting partition %d of the replacement layer: %w", w.Bucket, err)
		}
	}

	built.entries = append(built.entries, entry)
	built.blobs = append(built.blobs, blob)
	return nil
}

// releaseBuiltPartition hands one built partition to the persist hook and, when the
// hook returns a mapping, re-backs the partition's entry with it. It returns the entry
// and blob the layer should carry.
//
// THE REPLACEMENT ENTRY IS A COPY, NOT A MUTATION, mirroring RemapResidentBatch: the
// liveness pointer, the members map, the meta and the supersession record travel
// forward unchanged, and only the payload and the provenance change. Nothing here needs
// that function's CAS loop, because an unpublished entry has no readers — but the
// LIFETIME rule is identical, so the mapping's release is attached to the entry that
// will serve it and is freed outright on any path where no entry takes it.
func (e *SegmentedIndex[Q, S]) releaseBuiltPartition(
	built *BuiltLayer[Q, S], entry *segmentEntry[Q, S], blob SegmentBlob, persist LayerPartitionPersist,
) (*segmentEntry[Q, S], SegmentBlob, error) {
	mapped, ok, err := persist(blob)
	if err != nil {
		return nil, SegmentBlob{}, err
	}
	if !ok {
		// The hook could not map it. The partition is correct and durable; only the
		// memory property is forfeited, and the caller is told which id owes one.
		// Any mapping it handed back regardless is freed here: ownership passed on
		// return and no entry will ever attach it.
		releaseUnattached(mapped.Release)
		built.unreleased = append(built.unreleased, entry.meta.ID)
		return entry, blob, nil
	}

	seg, derr := e.format.Decode(mapped.Bytes)
	if derr != nil {
		// Nothing will ever attach this mapping, so it is freed here rather than
		// stranded — the releaseUnattached half of the ownership rule.
		releaseUnattached(mapped.Release)
		built.unreleased = append(built.unreleased, entry.meta.ID)
		built.releaseErr = errors.Join(built.releaseErr,
			fmt.Errorf("searchengine: decoding the stored copy of partition %s to release its build: %w",
				entry.meta.ID, derr))
		return entry, blob, nil
	}

	next := &segmentEntry[Q, S]{
		payload: seg,
		live:    entry.live,
		members: entry.members,
		meta:    entry.meta,
		record:  entry.record,
		// ZERO, and that is the whole point of the swap: this payload reads the
		// mapping, so the encoder's output is unreachable from the layer once this
		// entry replaces the built one.
		heapPayload: 0,
		pin:         mapped.keepAlive,
	}
	attachBlobCleanup(next, mapped.Release)
	return next, SegmentBlob{
		ID:        next.meta.ID,
		Format:    blob.Format,
		DocCount:  next.meta.DocCount,
		Bytes:     mapped.Bytes,
		Envelope:  mapped.Envelope,
		keepAlive: next,
	}, nil
}
