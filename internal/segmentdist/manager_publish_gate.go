// SPDX-License-Identifier: Apache-2.0

package segmentdist

// This file holds the lifecycle-aware gate predicate the embed write points
// (ReEmitDirtyBuckets/Flush) consult before running an L2-write pass. The gate skips
// a no-progress pass for sub-threshold unsealed batches: the write runs iff a SEALED
// resident segment is missing from the L2 cache.
//
// IT USED TO BE TWO QUESTIONS OR'D TOGETHER — is anything unshipped, OR did a prior
// publish fail and need retrying. The retry half went with the publish: there is no
// remote step left that can fail after a successful local write, so a pass either
// makes something durable or has nothing to do.

// hasUnwrittenExport reports whether a SEALED unwritten export exists: there is at
// least one resident segment whose id is not in the L2 cache index. It is a read-only
// projection of the write diff in persistResident — same cache-membership test,
// returning a bool instead of building the diff.
//
// IT ASKS ResidentSegmentIDs, NOT Export, AND THAT IS THE POINT OF THIS PREDICATE
// BEING CHEAP. This is a BOOLEAN consulted on every embed write point, and Export
// re-serializes every resident payload to answer it — tens of megabytes of Encode on
// a full corpus to decide a set-membership question. ResidentSegmentIDs is the
// id-only counterpart its own doc prescribes for exactly this caller: one atomic
// snapshot load and a walk of the entry metas, no per-segment Encode and no blob
// slice allocated and thrown away.
//
// THE TWO SNAPSHOTS DIFFER IN ONE CASE, and it is named rather than glossed: Export
// SKIPS any entry whose payload fails to Encode, while ResidentSegmentIDs lists every
// resident entry. So a segment that cannot encode is visible here — this gate can
// return true for it, and the write pass that follows finds nothing to write because
// persistResident still diffs over Export. The cost of that case is one no-progress
// pass; the cost of the OPPOSITE reading would be a genuinely unwritten segment the
// gate never reports, which is a corpus that silently stops converging. An
// unencodable segment is a defect either way, and this is the direction to be wrong
// in.
//
// sizeOf is the right membership probe rather than Get: it reads the in-memory index
// only, so it is recency-neutral and costs no disk read — asking whether a blob is
// present must not perturb the LRU ordering the residency budget sorts on.
func (m *distManager[Q, S]) hasUnwrittenExport() bool {
	resident := m.engine.ResidentSegmentIDs()
	if len(resident) == 0 {
		return false
	}
	for _, id := range resident {
		if _, present := m.cache.sizeOf(id); !present {
			return true
		}
	}
	return false
}
