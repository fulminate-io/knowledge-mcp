// SPDX-License-Identifier: Apache-2.0

// rebuild_segments_tombstones.go — the two set operations over the durable
// tombstone record the rebuild driver carries: the union that folds a scan's newly
// reported deletes into what earlier passes retained, and the trim that drops an id
// once the partition routing it has been re-emitted without it. Relocated verbatim
// from intercept_manage_rebuild_segments.go.
//
// They are a pair, and separating them from the driver keeps the reason visible:
// both decide MEMBERSHIP OF A PERSISTED SET whose only correctness rule is that an
// id may leave only when no durable blob can still resurrect it. The trim keys on
// which partitions were re-emitted rather than on the watermark for exactly that
// reason — a watermark advances on any landed publish, including one that touched
// no partition holding the id.

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/searchengine"

// unionTombstones merges the retained ids with the ones a scan just reported,
// dropping duplicates and preserving the retained order so the persisted record is
// stable across passes that learn nothing new.
func unionTombstones(retained []searchengine.ExternalID, scanned []string) []searchengine.ExternalID {
	seen := make(map[searchengine.ExternalID]struct{}, len(retained)+len(scanned))
	out := make([]searchengine.ExternalID, 0, len(retained)+len(scanned))
	add := func(id searchengine.ExternalID) {
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range retained {
		add(id)
	}
	for _, id := range scanned {
		add(searchengine.ExternalID(id))
	}
	return out
}

// dropTombstones is unionTombstones' inverse: it removes the named ids from the
// retained set, preserving survivor order so the persisted record stays stable across
// passes that remove nothing.
//
// It satisfies this file's membership rule differently from retainTombstones. That one
// drops an id because the partition routing it was re-emitted without it; this one
// drops an id because a LIVE DOCUMENT for it exists again — the node was re-created
// under the same external id, so seeding it dead would suppress the new document.
func dropTombstones(retained, drop []searchengine.ExternalID) []searchengine.ExternalID {
	if len(retained) == 0 || len(drop) == 0 {
		return retained
	}
	dead := make(map[searchengine.ExternalID]struct{}, len(drop))
	for _, id := range drop {
		dead[id] = struct{}{}
	}
	kept := make([]searchengine.ExternalID, 0, len(retained))
	for _, id := range retained {
		if _, remove := dead[id]; remove {
			continue
		}
		kept = append(kept, id)
	}
	return kept
}

// retainTombstones keeps the ids whose hash bucket this run did NOT re-emit.
//
// An id may leave the record once the partition routing it has been rebuilt
// without it, because from then on no durable blob holds the node and no import
// can bring it back. The bucket count is derived from the items THIS run emitted,
// which is the same count buildAndAddRebuildSegments grouped them under, so the
// two agree on which partitions were rebuilt.
//
// TRIMMING ON THE WATERMARK INSTEAD WOULD BE WRONG: the watermark advances on any
// landed publish, including one that re-emitted no partition holding the id.
func retainTombstones(ids []searchengine.ExternalID, items []rebuildSegItem) []searchengine.ExternalID {
	if len(ids) == 0 {
		return nil
	}
	bucketCount := searchengine.BucketCountFor(len(items))
	emitted := make(map[int]struct{}, bucketCount)
	for _, it := range items {
		emitted[searchengine.BucketOf(it.nodeID, bucketCount)] = struct{}{}
	}
	var keep []searchengine.ExternalID
	for _, id := range ids {
		if _, rebuilt := emitted[searchengine.BucketOf(id, bucketCount)]; !rebuilt {
			keep = append(keep, id)
		}
	}
	return keep
}
