// SPDX-License-Identifier: Apache-2.0

// layer_swap.go — the WHOLE-LAYER swap: build a complete replacement layer from
// documents alone, then publish it as the entire resident set in one CAS.
//
// It sits beside the group swap rather than inside it because the two are opposites
// in the one respect that matters. ReplaceBucketGroup CONSOLIDATES partitions FROM
// their resident constituents — it resolves them, harvests their live members, and
// derives its removal set from that same resolved list. This one REPLACES the layer
// while IGNORING what is resident: it reads no members, harvests nothing, and takes
// its removal set from a snapshot captured before the build. That is the from-scratch
// property a reset depends on, and it is why the group swap cannot express it —
// passing no constituents removes nothing and appends the new layer beside the old,
// while passing every resident id harvests them all forward.

package searchengine

import "errors"

// BuiltLayer is a complete replacement layer that has been BUILT but not yet
// published. It is produced by BuildLayer and consumed by ReplaceLayer, and the split
// exists so a caller can act on the built blobs — ship them, and judge them against a
// degeneracy gate — while the engine is still serving the old layer untouched.
//
// THE HANDLE CARRIES THE PRE-BUILD SNAPSHOT, AND THAT IS ITS MAIN JOB. The set of ids
// this swap retires must be captured ONCE, before the build, and never recomputed —
// see ReplaceLayer for why recomputing it is destructive. Holding it in the handle
// means there is no code path that could recompute it: the value is fixed at
// construction and the fields are unexported, so the rule is structural rather than a
// discipline someone has to remember.
type BuiltLayer[Q, S any] struct {
	// engine is the SegmentedIndex this layer was built by. ReplaceLayer refuses a
	// handle from a different engine: this is a destructive primitive and a foreign
	// layer would replace one engine's corpus with another's.
	engine *SegmentedIndex[Q, S]

	// entries are the built partitions, in the order the work list supplied them.
	entries []*segmentEntry[Q, S]
	// blobs mirrors entries as shippable payloads, encoded once at build time so the
	// caller's ship does not re-encode.
	blobs []SegmentBlob

	// capturedOldIDs / capturedOldSet are the resident ids at the instant BEFORE the
	// build, in slice and set form. Both are derived from the same snapshot read and
	// are never re-derived.
	capturedOldIDs []SegmentID
	capturedOldSet map[SegmentID]bool

	// unreleased names the partitions whose payload is STILL the encoder's heap
	// output when the build finishes: the persist hook declined to map them, or the
	// bytes it handed back would not decode. It is empty when no hook was supplied,
	// because a build with no hook releases nothing and owes nothing.
	unreleased []SegmentID
	// releaseErr joins the decode failures behind unreleased. It is deliberately NOT
	// returned from the build: a partition that could not be re-backed by its mapping
	// is CORRECT and durable and only costs memory, so it must not fail a rebuild —
	// the same disposition releaseHeapBackedResident takes for the seal path.
	releaseErr error
}

// Blobs returns the built layer's shippable blobs. The slice is a copy, so a caller
// cannot reorder or truncate what ReplaceLayer will publish; the Bytes are shared,
// which is safe because nothing mutates an encoded segment.
func (b *BuiltLayer[Q, S]) Blobs() []SegmentBlob {
	out := make([]SegmentBlob, len(b.blobs))
	copy(out, b.blobs)
	return out
}

// Len reports how many partitions the layer holds — enough for a caller to refuse an
// empty build without reaching into the handle.
func (b *BuiltLayer[Q, S]) Len() int { return len(b.entries) }

// BuildLayer builds a complete replacement layer from the supplied work, reading NO
// resident state, and returns it unpublished. The engine is untouched: the layer it is
// serving now is the layer it is still serving when this returns, whether or not the
// build succeeded.
//
// FROM-SCRATCH BY CONSTRUCTION. Each partition is built from its documents alone —
// no snapshot resolve, no Merge, no accept predicate. That is precisely the property
// a reset needs and the one a consolidating swap cannot have: a rebuild driven through
// the group swap would carry forward resident members the caller's scan never
// returned, which is exactly what a reset exists to purge.
//
// w.Superseded IS NOT CONSULTED, deliberately. A layer built from nothing has nothing
// to supersede — every id it does not carry is retired by the swap regardless — and
// reading it would reintroduce the resident coupling this primitive exists to avoid.
//
// AN ERROR LEAVES NOTHING BEHIND. Build and encode failures return before any handle
// escapes, so a partial layer cannot be published later by mistake.
func (e *SegmentedIndex[Q, S]) BuildLayer(work []BucketWork) (*BuiltLayer[Q, S], error) {
	return e.BuildLayerReleasing(work, nil)
}

// ReplaceLayer publishes a built layer as the engine's ENTIRE resident set in one CAS
// and reports what it published and what it retired.
//
// THE REMOVAL SET IS THE HANDLE'S CAPTURED SNAPSHOT AND IS NEVER RECOMPUTED. This is
// the one place this primitive diverges from ReplaceBucketGroup's template, and the
// divergence is the whole safety argument. That method also hoists its removal set
// outside the CAS loop, but its set is an ENUMERATION of the ids that call consumed,
// so re-applying it against a snapshot another writer changed underneath is still
// correct — its own comment says exactly that. "Every id in the old snapshot" is not
// an enumeration, it is a PREDICATE over resident state, and a predicate silently
// changes meaning between retry iterations. Recomputing it after a lost race would
// sweep in a segment a concurrent drain published during the build, swap it straight
// back out, drop it from the next manifest and let it be reaped — the other writer's
// work discarded with no error raised anywhere.
//
// SO WHATEVER LANDS DURING THE BUILD SURVIVES. The captured set names only what was
// resident before this rebuild began, so a concurrently published segment is not in
// it, is carried through by withReplacedGroup (which preserves every entry it does not
// name), and is named by the next publish — while the whole prior layer is still
// replaced. That contention is engineered rather than hypothetical: the live
// acceptance for this work deliberately drives an embed drain into this window.
//
// RETIREMENT IS ALIAS-SAFE. A rebuilt partition whose bytes are unchanged mints the id
// a resident segment already carries, so retiring purely by name would tell the owner
// to reclaim the stored blob of a segment this very call just published. Every
// published id is excluded from the retired set for that reason.
//
// AN EMPTY LAYER IS REFUSED rather than published. Replacing the resident set with
// nothing empties the corpus, and no caller can legitimately mean that; the degeneracy
// gate should have refused it earlier, and this is the structural backstop.
func (e *SegmentedIndex[Q, S]) ReplaceLayer(built *BuiltLayer[Q, S]) (published, retired []SegmentID, err error) {
	if built == nil {
		return nil, nil, errors.New("searchengine: ReplaceLayer needs a built layer, got nil")
	}
	if built.engine != e {
		return nil, nil, errors.New(
			"searchengine: ReplaceLayer refuses a layer built by a DIFFERENT engine — " +
				"publishing it would replace this engine's corpus with another's")
	}
	if len(built.entries) == 0 {
		return nil, nil, errors.New(
			"searchengine: ReplaceLayer refuses an EMPTY layer — replacing the resident set with nothing empties the corpus")
	}

	publishedIDs := make(map[SegmentID]bool, len(built.entries))
	published = make([]SegmentID, 0, len(built.entries))
	for _, entry := range built.entries {
		publishedIDs[entry.meta.ID] = true
		published = append(published, entry.meta.ID)
	}
	retired = excluding(built.capturedOldIDs, publishedIDs)

	// NO DURABLE SUPERSESSION RECORD IS STAMPED HERE, and the omission is a decision
	// rather than an oversight. A record has to be IN the stored bytes to be worth
	// anything, and this layer's stored bytes were encoded by BuildLayer — before this
	// swap existed and before `retired` could be known — so a record stamped now would
	// live on the resident entries while the files on disk said nothing, which is worse
	// than no record at all: two sources that disagree. Re-encoding the whole layer
	// here to fix that would undo the one thing BuildLayer's split exists for, which is
	// that the caller has already shipped these exact bytes.
	//
	// AND THE SEMANTICS WOULD BE THE DANGEROUS ONES ANYWAY. A layer replaces the corpus
	// as a SET; no single partition carries the retired segments' members, so a record
	// here would only ever be honorable when every partition of the replacement is
	// present — the condition a half-written layer fails. What retires the old layer
	// stays what retired it before: the owner's own reclaim, driven from `retired`.

	// ONE CAS. Only the snapshot is re-read on a lost race; the removal set is the
	// captured one, untouched.
	for {
		cur := e.set.Load()
		next := cur.withReplacedGroup(e.format, built.capturedOldSet, built.entries)
		if e.set.CompareAndSwap(cur, next) {
			break
		}
	}

	// Surface the supersession once for the whole layer, exactly as the group swap
	// does: the first output carries the retired set and the rest report none, so the
	// owner learns every new blob without being told to reclaim anything twice.
	survivors := retired
	for _, entry := range built.entries {
		e.fireMergeHook(entry, survivors)
		survivors = nil
	}
	return published, retired, nil
}
