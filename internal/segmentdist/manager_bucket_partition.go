// SPDX-License-Identifier: Apache-2.0

// manager_bucket_partition.go — the PARTITION DERIVATION every re-emit runs
// through: grouping the work by partition, closing that set under shared
// constituency, and deciding which of the window's own segments are safe to
// retire afterwards. Relocated verbatim from manager_bucket.go.
//
// The closure is what makes the partition predicate safe, and it is the reason
// these travel together: rebuilding a partition REMOVES its constituents, so a
// constituent also holding members of a partition nobody is rebuilding would lose
// them. The walk is over member SPANS rather than partition arithmetic, and it
// repeats to a fixpoint because pulling in one segment's partitions can pull in
// further segments again.

package segmentdist

import (
	"slices"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// replaceBucketGroups partitions the work and rebuilds each affected partition.
// Partitions are independent — each swap removes and adds only its own segments —
// so they are rebuilt concurrently, bounded by the CPU count; the engine's publish
// is a compare-and-swap loop that retries when another partition lands first.
//
// A superseded id whose partition receives no document still forms a group, which
// is the delete-only shape. A partition with no resident segments and incoming
// documents is simply new.
//
// exclude names segments that must never be offered as constituents. The caller
// passes the write window's freshly sealed segments: those hold documents spread
// across the WHOLE corpus rather than one partition, so offering one would make
// every partition it touches part of this call's rebuild set and turn a delta into
// a corpus-wide consolidation. Nothing is lost by excluding them: each of their
// documents is supplied directly to its own partition's call, and the copies they
// still hold are killed by the supersession.
//
// corpusDocs is the size of the corpus the partition count is derived FROM, and it
// is the caller's to state because only the caller knows whether the incoming
// documents are already resident. A drain's documents are — the write path seals
// them before the backlog is drained — so counting them again would derive a count
// for twice the corpus that exists. The one-shot paths pass resident plus incoming,
// because there the documents genuinely are not resident yet.
//
// THE REBUILD SET IS CLOSED UNDER CONSTITUENCY, and that closure is what makes the
// partition predicate safe. Rebuilding a partition REMOVES its constituents, so a
// constituent also holding members of a partition nobody is rebuilding would lose
// them. Starting from the dirty partitions, every partition held by any segment
// this call will consume is pulled in too, repeating until nothing new appears.
//
// THE CLOSURE WALKS MEMBER SPANS, NEVER PARTITION ARITHMETIC. A segment aligned to
// an older count spans one partition per doubling it is behind, so a segment two
// counts stale spans four. Deriving siblings as bucket+oldCount finds one of them
// and silently drops the members in the rest. Staleness is not bounded here:
// realignment is write-driven, so a partition no write touches keeps its old
// alignment for as long as that lasts.
//
// COST. On a stable count every constituent is already aligned, spans one
// partition, and the closure adds nothing — the delta stays a delta. Across a
// crossing the set grows by the partitions the touched segments span; when every
// segment is one count behind that is at most double the dirty set, which is the
// common case in steady traffic rather than a guarantee this code enforces.
//
// It returns the ids the partitions published, so the caller can tell a segment it
// just published apart from one it needs to retire.
func replaceBucketGroups[Q, S any](
	dm *distManager[Q, S], superseded []searchengine.ExternalID, docs []searchengine.Document,
	exclude []searchengine.SegmentID, corpusDocs int,
) ([]searchengine.SegmentID, error) {
	if len(docs) == 0 && len(superseded) == 0 {
		return nil, nil
	}
	excluded := make(map[searchengine.SegmentID]bool, len(exclude))
	for _, id := range exclude {
		excluded[id] = true
	}
	bucketCount := searchengine.BucketCountFor(corpusDocs)

	docsByBucket := make(map[int][]searchengine.Document)
	for _, d := range docs {
		b := searchengine.BucketOf(d.ID, bucketCount)
		docsByBucket[b] = append(docsByBucket[b], d)
	}
	supByBucket := make(map[int][]searchengine.ExternalID)
	for _, id := range superseded {
		b := searchengine.BucketOf(id, bucketCount)
		supByBucket[b] = append(supByBucket[b], id)
	}

	// ONE pass over the resident members yields every segment's span, which gives
	// the constituent list for every partition at once. Asking per partition instead
	// would walk the resident set once per partition.
	spans := dm.engine.SegmentSpans(bucketCount)
	constituentsByBucket := make(map[int][]searchengine.SegmentID)
	for id, held := range spans {
		if excluded[id] {
			continue
		}
		for _, b := range held {
			constituentsByBucket[b] = append(constituentsByBucket[b], id)
		}
	}
	// ORDER THE CONSTITUENTS, because the walk above ranges a MAP and Go randomizes
	// that. The merge keeps the LAST copy of a repeated id, so constituent order is
	// what decides which copy survives when two resident layers both carry it — and an
	// order that changes run to run makes the surviving vector change run to run, in a
	// path whose whole contract is byte reproducibility. Sorting by segment id makes
	// the choice arbitrary but STABLE. (The freshly built segment is appended after
	// these by harvestPartition, so a fresh write still wins.)
	for b := range constituentsByBucket {
		slices.Sort(constituentsByBucket[b])
	}

	// Sized from one of the two sources rather than their sum: a map size is a
	// hint the runtime grows past for free, while the sum is an addition the
	// allocator sees and could overflow int for pathologically large inputs
	// (CWE-190). The two key sets overlap heavily in practice, so the sum was
	// over-hinting anyway.
	dirty := make(map[int]bool, len(docsByBucket))
	for b := range docsByBucket {
		dirty[b] = true
	}
	for b := range supByBucket {
		dirty[b] = true
	}
	buckets := closeOverConstituency(dirty, constituentsByBucket, spans)
	sort.Ints(buckets)

	// THE GROUP SWAPS AS ONE. buckets is closed under shared constituency, so its
	// partitions read the SAME segments; handing them to the engine as one group
	// makes their reads atomic with respect to each other. Driving them as separate
	// swaps — concurrently OR serially — lets whichever lands first carry away the
	// members the others had not yet harvested, and serial is the WORST case rather
	// than the safe one.
	//
	// The union of the group's constituents is what the engine resolves once. Each
	// segment appears in constituentsByBucket under every partition it spans, so the
	// union is deduplicated here.
	seen := make(map[searchengine.SegmentID]bool)
	union := make([]searchengine.SegmentID, 0, len(spans))
	work := make([]searchengine.BucketWork, 0, len(buckets))
	for _, b := range buckets {
		for _, id := range constituentsByBucket[b] {
			if seen[id] {
				continue
			}
			seen[id] = true
			union = append(union, id)
		}
		work = append(work, searchengine.BucketWork{
			Bucket:     b,
			Superseded: supByBucket[b],
			Docs:       docsByBucket[b],
		})
	}

	publishedBy, err := dm.engine.ReplaceBucketGroup(bucketCount, union, work)
	if err != nil {
		return nil, err
	}
	// Report in ascending partition order so the caller's retirement diff is stable.
	published := make([]searchengine.SegmentID, 0, len(publishedBy))
	for _, b := range buckets {
		if id, ok := publishedBy[b]; ok {
			published = append(published, id)
		}
	}
	return published, nil
}

// closeOverConstituency grows the dirty partition set until it holds every
// partition reachable through a segment the rebuild will consume, and returns it.
//
// WHY A FIXPOINT AND NOT ONE HOP. Pulling in a segment's partitions can pull in
// further segments, which span further partitions again. One hop suffices only
// when every segment is exactly one count behind; a segment several counts behind
// chains further, and nothing here bounds staleness because realignment is
// write-driven. The loop is the honest form and costs nothing when there is
// nothing to add, which is the stable-count case.
//
// It terminates because the partition set only grows and is bounded by the count.
func closeOverConstituency(
	dirty map[int]bool,
	constituentsByBucket map[int][]searchengine.SegmentID,
	spans map[searchengine.SegmentID][]int,
) []int {
	pending := make([]int, 0, len(dirty))
	for b := range dirty {
		pending = append(pending, b)
	}
	for len(pending) > 0 {
		b := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, id := range constituentsByBucket[b] {
			for _, held := range spans[id] {
				if dirty[held] {
					continue
				}
				dirty[held] = true
				pending = append(pending, held)
			}
		}
	}
	out := make([]int, 0, len(dirty))
	for b := range dirty {
		out = append(out, b)
	}
	return out
}

// retiring splits the window's sealed segments into the ones safe to DROP and the
// ones that must stay RESIDENT. A tail is retired only when the rebuild did not
// republish it AND it carries no live member the rebuild left uncovered; everything
// else is retained, and the caller reports that.
//
// THE RULE IS LOCAL, and it replaces a global one that could not be checked here. A
// tail is handed to replaceBucketGroups as the EXCLUDE set, so it is never offered
// as a constituent and its spans are never walked by the constituency closure —
// which means a member of a tail survives the rebuild only if the rebuild was handed
// that document. "Its partition was rebuilt" is not enough and never was. Dropping a
// tail holding a member nobody rebuilt destroys that member, which is exactly the
// caller obligation ReplaceBucket states: close a rebuilt partition under its
// constituency, or duplication turns into loss.
//
// A RETAINED TAIL IS LEFT RESIDENT DELIBERATELY. Keeping a segment costs a duplicate
// copy at worst, and duplication is recoverable — the next drain that touches one of
// its partitions resolves it as an ordinary constituent and absorbs it. Loss is not
// recoverable, so the asymmetry decides the default.
//
// A segment id is a content hash, so consolidating a partition can publish exactly
// the id one of the window's own segments already carried — the whole partition came
// from that segment. Dropping such an id by name would remove the segment the
// rebuild just published and take the corpus with it, which is why a republished
// tail is never retired regardless of what it holds.
//
// retire comes FIRST and retained SECOND. Both are []searchengine.SegmentID, so a
// transposed call compiles silently and unloads precisely the set that had to be
// kept. retained is deduplicated because the tail list is a multiset — two
// byte-identical batches record one id twice — so the caller's diagnostic counts
// segments rather than backlog entries.
func retiring(tails, published []searchengine.SegmentID, uncovered map[searchengine.SegmentID]int) (retire, retained []searchengine.SegmentID) {
	keep := make(map[searchengine.SegmentID]bool, len(published))
	for _, id := range published {
		keep[id] = true
	}
	retire = make([]searchengine.SegmentID, 0, len(tails))
	seen := make(map[searchengine.SegmentID]bool, len(tails))
	for _, id := range tails {
		if !keep[id] && uncovered[id] == 0 {
			retire = append(retire, id)
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		retained = append(retained, id)
	}
	return retire, retained
}

// coveredSet builds the membership lookup the engine's coverage probe takes: the ids
// a drain is NOT obliged to keep searchable.
//
// TWO SOURCES, and both are needed. The documents the drain SUPPLIED are provably
// carried by the rebuild — the group swap kills every superseded id across the whole
// resident set before harvesting, and the fresh per-partition build re-adds each
// supplied document — so a tail is not their last home. The graph's TOMBSTONED ids
// are covered for the opposite reason: the drain drops them from the build on
// purpose, and they must not be searchable at all.
//
// Omitting the tombstoned half resurrects deleted documents. A tombstone is recorded
// on the manager and does not clear a segment's live bit, and a re-queued write for a
// tombstoned id seals a fresh tail and makes the id live in it; the drain then
// filters that document out of the build, sees a live member it did not rebuild, and
// keeps the tail — putting a deleted node back into search.
//
// An id in neither set was offered to no partition and is still meant to be found, so
// the tail holding it is its last searchable home.
func coveredSet(ids, tombstoned []searchengine.ExternalID) map[searchengine.ExternalID]bool {
	// Sized from one of the two inputs rather than their sum, for the same reason
	// the dirty set above is: a map size is a hint the runtime grows past for
	// free, while the sum is an addition the allocator sees and could overflow
	// int (CWE-190).
	covered := make(map[searchengine.ExternalID]bool, len(ids))
	for _, id := range ids {
		covered[id] = true
	}
	for _, id := range tombstoned {
		covered[id] = true
	}
	return covered
}

// docIDs lists the ids of the supplied documents — the ids whose tail copies the
// re-emit supersedes.
func docIDs(docs []searchengine.Document) []searchengine.ExternalID {
	ids := make([]searchengine.ExternalID, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	return ids
}
