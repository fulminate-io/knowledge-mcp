// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// residueCohort is one half of the published set under the split that actually
// separates a pool's provenance: whether a blob carries a supersession record.
//
// THE SPLIT IS THE RECORD, NOT THE TIMESTAMP, and that choice is what makes this
// readable on a copied directory. A consolidating swap stamps its outputs with what
// they replaced; an ordinary seal and every blob written before the durable record
// existed carry nothing. So "record-carrying" names the outputs of the most recent
// consolidations and "record-less" names everything that predates them or was never
// a consolidation — which is exactly the cohort question a residue investigation
// asks, answered from the bytes rather than from file ages a copy destroys.
type residueCohort struct {
	name string
	// segments is how many blobs fall in this half; docs is the sum of their
	// per-segment distinct counts; union is how many DISTINCT documents the half
	// holds between them, so docs-union is the duplication INTERNAL to the half.
	segments int
	docs     int
	union    map[searchengine.ExternalID]struct{}
	// parts is every partition any segment in the half holds a member of. For the
	// record-less half it is the REACHABILITY fact: a write landing in one of these
	// partitions resolves the residue segment spanning it as an ordinary constituent
	// and absorbs it, so the breadth of this set is how much of the corpus's write
	// traffic can reclaim the residue lazily.
	parts map[int]bool
}

// splitByRecord partitions the published segments into the record-carrying and
// record-less halves and accumulates each half's membership.
func splitByRecord(segs []*residueSegment) (withRecord, without residueCohort) {
	withRecord = residueCohort{
		name: "record-carrying", union: map[searchengine.ExternalID]struct{}{}, parts: map[int]bool{},
	}
	without = residueCohort{
		name: "record-less", union: map[searchengine.ExternalID]struct{}{}, parts: map[int]bool{},
	}
	for _, s := range segs {
		if !s.published {
			continue
		}
		half := &without
		if len(s.superseded) > 0 {
			half = &withRecord
		}
		half.segments++
		half.docs += s.distinct
		for id := range distinctIDs(s.members) {
			half.union[id] = struct{}{}
		}
		for _, b := range s.spans {
			half.parts[b] = true
		}
	}
	return withRecord, without
}

// reportResidueCohorts prints what each half holds and how much of the record-less
// half the record-carrying half already covers.
//
// THE LAST NUMBER IS THE ONE THAT DECIDES THE INVESTIGATION. A record-less segment
// whose every document is also held by the record-carrying half is a resident copy
// of documents a later consolidation already re-published — duplication whose
// removal would cost nothing. One holding documents the other half lacks is not
// residue at all: it is the only copy of those documents, and retiring it would
// remove them from the searchable set.
func reportResidueCohorts(t *testing.T, segs []*residueSegment) {
	t.Helper()
	withRecord, without := splitByRecord(segs)
	t.Logf("COHORTS BY SUPERSESSION RECORD")
	for _, half := range []residueCohort{withRecord, without} {
		t.Logf("  %-16s segments=%3d  summed distinct=%6d  union=%6d  duplication within the half=%d  "+
			"partitions spanned=%d",
			half.name, half.segments, half.docs, len(half.union), half.docs-len(half.union), len(half.parts))
	}
	shared, only := 0, 0
	for id := range without.union {
		if _, ok := withRecord.union[id]; ok {
			shared++
			continue
		}
		only++
	}
	t.Logf("  documents the record-less half shares with the record-carrying half: %d", shared)
	t.Logf("  documents held ONLY by the record-less half (lost if it were dropped): %d", only)
}
