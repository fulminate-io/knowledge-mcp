// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// ReplaceBucket re-emits whatever partitions the supplied documents and superseded
// ids belong to, then ships and publishes ONCE for the whole call.
//
// Documents are grouped by partition and each partition is rebuilt in a single
// atomic swap, so the partitions nobody touched stay resident and keep their
// stored copies referenced by the published manifest. Publishing once rather than
// per partition is deliberate: a per-partition publish would issue one network
// round trip per partition.
//
// THE COUNT CAN CHANGE, AND REALIGNMENT IS WRITE-DRIVEN. The partition count is
// derived from the corpus size, so a corpus growing through a power of two moves
// it, and because the partition of a document is a modulo of that count, a
// partition under the old count splits into several under the new one. Only the
// partitions this call actually touches are realigned, together with whatever else
// their constituents hold; a partition no write reaches keeps its old alignment
// until a later write arrives. That is correct at every moment — every document
// stays live, in exactly one segment, and reachable — and it keeps a crossing
// proportional to the writes that cross it rather than to the whole corpus. The
// batch rebuild driver realigns everything in one pass when it runs.
func (m *Manager) ReplaceBucket(
	ctx context.Context, gt kgtypes.GraphType, name string,
	superseded []searchengine.ExternalID, docs []searchengine.Document,
) error {
	dm := m.managerFor(gt, name)
	// The incoming documents are NOT yet resident on this path, so the corpus they
	// will form is the resident set plus them.
	corpusDocs := dm.engine.DistinctResidentDocCount() + len(docs)
	return replaceBucketAndPublish(ctx, dm, superseded, docs, corpusDocs)
}

// replaceBucketAndPublish is the shared body of every partition re-emit: rebuild the
// partitions the supplied documents and superseded ids belong to, then ship and
// publish ONCE for the whole call. The three callers differ only in the two operands
// this signature makes explicit.
//
// corpusDocs IS THE CALLER'S TO SUPPLY because the callers disagree about it, and
// getting it wrong moves the derived partition count. ReplaceBucket's documents are
// NOT yet resident, so its corpus is the resident set PLUS them; the rebuild's delta
// documents ARE already resident (the scan reads nodes whose vectors the embed
// writeback already sealed into the engine), so adding them again would derive a count
// for twice the corpus that exists.
//
// THE PUBLISHED MANIFEST IS THIS ENGINE'S OWN RESIDENT SET. It used to take a set of
// sibling digests to reference alongside it, because the HNSW rebuild wrote a second
// engine keyed to the same manifest; with one engine per format there is no sibling and
// the two formats no longer disagree about anything here.
//
// Generic over [Q, S] because distManager is, and the two live instantiations carry
// different type arguments (HNSW is [[]byte, struct{}], BM25 is
// [bm25.Query, *bm25.CorpusStats]); a non-generic helper cannot take both.
func replaceBucketAndPublish[Q, S any](
	ctx context.Context, dm *distManager[Q, S],
	superseded []searchengine.ExternalID, docs []searchengine.Document,
	corpusDocs int,
) error {
	if _, err := replaceBucketGroups(dm, superseded, docs, nil, corpusDocs); err != nil {
		return err
	}
	// The reconcile diffs against the locally-shipped set so a fresh process can only
	// ever retire its own tail, never the prior corpus it merely re-imported.
	_, err := dm.shipAndPublish(ctx, dm.locallyShipped)
	return err
}

// ReplaceBucketFields is the field-engine counterpart of ReplaceBucket. That engine
// has no deterministic sibling, so its manifest is exactly its own resident set.
func (m *Manager) ReplaceBucketFields(
	ctx context.Context, gt kgtypes.GraphType, name string,
	superseded []searchengine.ExternalID, docs []searchengine.Document,
) error {
	dm := m.bm25ManagerFor(gt, name)
	corpusDocs := dm.engine.DistinctResidentDocCount() + len(docs)
	return replaceBucketAndPublish(ctx, dm, superseded, docs, corpusDocs)
}

// DeleteFromBuckets makes a client-originated delete DURABLE: it kills the named
// ids in the partitions that hold them and re-emits those partitions through BOTH
// formats, so the removal survives in the shipped blobs rather than living only as
// a cleared bit in this process's memory.
//
// BOTH FORMATS ARE REQUIRED. A node is indexed in the vector corpus and in the
// field corpus, and the two carry SEPARATE manifests, so re-emitting one leaves the
// node in the other — still occupying rank slots there. The first error wins;
// neither leg is skipped because the other failed.
//
// WHAT A STALE COPY COSTS, stated precisely because it is easy to overstate: a
// removed node is NOT shown to anyone. The read path drops any ranked id that is
// missing from its tombstone-excluding hydrate, so the user never sees the node
// itself. What they see is a SHORTER result set, because the dead vector still
// competed for a top-k slot and that slot is discarded after ranking. The blob also
// keeps carrying the document, inflating every ship, cache file and load of that
// partition.
//
// It does NOT close the import window. A blob shipped BEFORE the delete and
// re-imported afterwards starts all-live again, because both load paths import with
// no tombstones; sealing that is the sibling step's job. Every test here deletes
// through the LIVE engine, where the bit is already clear, so they cannot stand in
// for it.
//
// THE WRITE BACKLOG IS PURGED FIRST, and that ordering is deliberate. The re-emit
// below removes these ids from their partitions, but the reconcile pass drains the
// write backlog immediately afterwards and would rebuild those same partitions FROM
// it — putting the documents straight back into both corpora and into the next
// shipped blob. Purging before the re-emit rather than after means a drain
// interleaving between the two finds nothing to resurrect, and if the re-emit itself
// fails the documents are queued nowhere, which is the direction matching the
// caller's intent.
//
// THE PURGE IS A WINDOW, NOT A BARRIER. It closes the resurrection window for every
// drain that snapshots AFTER it. A drain that had ALREADY taken its snapshot builds
// from that private copy and can still re-emit these ids one more time; that residual
// is bounded by a single reconcile interval, because the next tombstone-delta pass
// sees the ids as fresh and re-deletes them. The drain's own tombstone filter does not
// close that window either — this path sets no tombstones at all — so the same
// one-interval bound covers both.
func (m *Manager) DeleteFromBuckets(
	ctx context.Context, gt kgtypes.GraphType, name string, ids []searchengine.ExternalID,
) error {
	if len(ids) == 0 {
		return nil
	}
	m.purgeDirty(gt, name, ids)
	// The pure-delete shape of the partition re-emit: ids to supersede, no incoming
	// documents.
	err := m.ReplaceBucket(ctx, gt, name, ids, nil)
	if fieldsErr := m.ReplaceBucketFields(ctx, gt, name, ids, nil); err == nil {
		err = fieldsErr
	}
	return err
}
