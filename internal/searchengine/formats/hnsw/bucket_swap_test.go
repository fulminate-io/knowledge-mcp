// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// bucketDoc builds a doc whose vector is derived from fill, so two docs sharing an
// id but built with different fills carry genuinely different vectors — the
// distinction a stale-versus-fresh assertion rests on.
func bucketDoc(id string, fill int) searchengine.Document {
	v := make([]byte, defaultVecBytes)
	for i := range v {
		v[i] = byte((fill*31 + i*7) % 251)
	}
	return searchengine.Document{ID: id, Vector: v}
}

// swapBucketCount is the partition width every test in this file assigns ids
// with, so a doc's bucket is stable across them.
const swapBucketCount = 4

// idsInBucket returns want ids that hash into the given bucket, drawn from a
// deterministic id stream.
func idsInBucket(t *testing.T, bucket, want int) []string {
	t.Helper()
	var out []string
	for i := 0; len(out) < want; i++ {
		if i > 100000 {
			t.Fatalf("could not find %d ids in bucket %d of %d", want, bucket, swapBucketCount)
		}
		id := fmt.Sprintf("doc-%05d", i)
		if searchengine.BucketOf(id, swapBucketCount) == bucket {
			out = append(out, id)
		}
	}
	return out
}

// bucketEngine builds an engine over the real HNSW format that seals one segment
// per Add and never background-merges, so the test controls the segment layout.
func bucketEngine() *searchengine.SegmentedIndex[[]byte, struct{}] {
	return searchengine.New[[]byte, struct{}](Format{}, searchengine.Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
	})
}

// TestReplaceBucketNoDuplicateWindow drives ReplaceBucket over the REAL HNSW
// format and pins the four properties the one-CAS swap exists to provide: a reader
// running concurrently across the swap never sees an id twice, exactly one segment
// holds the bucket afterwards, the rewritten member resolves to its FRESH vector,
// and the members nobody touched keep the vectors they were built with — which is
// what proves the merge recovered them from the sealed segment rather than
// requiring them to be supplied again.
//
// It also supersedes an id living in a segment that is NOT a constituent and
// asserts that copy stopped being returned. That is the global-kill contract: a
// caller re-emitting one bucket must be able to retire copies that sit outside it.
func TestReplaceBucketNoDuplicateWindow(t *testing.T) {
	const (
		target = 1
		// Enough members that the merge is real work rather than an instant, so the
		// concurrent reader genuinely spans the swap.
		bucketMembers = 32
	)

	members := idsInBucket(t, target, bucketMembers)
	updated, untouched := members[0], members[1:]
	tailID := idsInBucket(t, 2, 1)[0]
	controlID := idsInBucket(t, 3, 1)[0]

	eng := bucketEngine()
	defer eng.Close()

	// The bucket's own segment.
	bucketDocs := []searchengine.Document{bucketDoc(updated, 1)}
	for i, id := range untouched {
		bucketDocs = append(bucketDocs, bucketDoc(id, 10+i))
	}
	if err := eng.Add(bucketDocs); err != nil {
		t.Fatalf("Add(bucket): %v", err)
	}
	// An unaligned segment holding a doc from a different bucket, and a control
	// segment nothing in this call refers to.
	if err := eng.Add([]searchengine.Document{bucketDoc(tailID, 50)}); err != nil {
		t.Fatalf("Add(tail): %v", err)
	}
	if err := eng.Add([]searchengine.Document{bucketDoc(controlID, 60)}); err != nil {
		t.Fatalf("Add(control): %v", err)
	}

	constituents := eng.BucketConstituents(target, swapBucketCount)
	if len(constituents) != 1 {
		t.Fatalf("BucketConstituents before = %d ids, want 1", len(constituents))
	}

	fresh := bucketDoc(updated, 99)
	if bytes.Equal(fresh.Vector, bucketDocs[0].Vector) {
		t.Fatalf("the fresh and stale vectors for %s must differ or the test proves nothing", updated)
	}

	// A reader querying across the swap must never see one id twice.
	stop := make(chan struct{})
	var sawDuplicate atomic.Bool
	var reads atomic.Int64
	var wg sync.WaitGroup
	wg.Go(func() {
		query := bucketDocs[1].Vector
		for {
			select {
			case <-stop:
				return
			default:
			}
			seen := make(map[searchengine.ExternalID]struct{}, 16)
			for _, h := range eng.Search(query, 32) {
				if _, dup := seen[h.ID]; dup {
					sawDuplicate.Store(true)
				}
				seen[h.ID] = struct{}{}
			}
			reads.Add(1)
		}
	})

	// Do not start the swap until the reader is genuinely running, and do not stop
	// it until the reader has queried the post-swap set — otherwise the whole check
	// could pass without a single read overlapping the change.
	for reads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	spanning := reads.Load()

	_, err := eng.ReplaceBucket(target, swapBucketCount, constituents,
		[]searchengine.ExternalID{updated, tailID},
		[]searchengine.Document{fresh})

	for reads.Load() < spanning+2 {
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatalf("ReplaceBucket: %v", err)
	}
	if sawDuplicate.Load() {
		t.Fatal("a concurrent Search returned the same id twice across the swap")
	}

	// Exactly one segment holds the bucket, and it is a new one.
	after := eng.BucketConstituents(target, swapBucketCount)
	if len(after) != 1 {
		t.Fatalf("BucketConstituents after = %d ids, want 1 consolidated segment", len(after))
	}
	if after[0] == constituents[0] {
		t.Fatalf("the bucket segment id did not change: still %s", after[0])
	}

	// The rewritten member resolves to the FRESH vector.
	got, ok := eng.VectorByID(updated)
	if !ok {
		t.Fatalf("VectorByID(%s) not found after the swap", updated)
	}
	if !bytes.Equal(got, fresh.Vector) {
		t.Fatalf("VectorByID(%s) returned the stale vector, want the fresh one", updated)
	}

	// The untouched members kept the vectors the merge carried across.
	for i, id := range untouched {
		got, ok := eng.VectorByID(id)
		if !ok {
			t.Fatalf("untouched member %s was lost by the swap", id)
		}
		if want := bucketDoc(id, 10+i).Vector; !bytes.Equal(got, want) {
			t.Fatalf("untouched member %s lost its vector across the merge", id)
		}
	}

	// The superseded id living OUTSIDE the constituents stopped being returned,
	// while the control doc in another segment is untouched.
	if containsID(eng.Search(bucketDoc(tailID, 50).Vector, 32), tailID) {
		t.Fatalf("superseded id %s in a non-constituent segment is still searchable", tailID)
	}
	if !containsID(eng.Search(bucketDoc(controlID, 60).Vector, 32), controlID) {
		t.Fatalf("control id %s in an unrelated segment must be unaffected", controlID)
	}
}

// TestReplaceBucketPureDeleteConsolidates covers the delete-only shape: superseded
// ids with NO incoming documents. The constituents still consolidate into a single
// segment, the superseded members are gone from it, and its DocCount equals the
// survivor count.
func TestReplaceBucketPureDeleteConsolidates(t *testing.T) {
	const (
		target     = 0
		perSegment = 4
	)

	members := idsInBucket(t, target, 2*perSegment)
	eng := bucketEngine()
	defer eng.Close()

	for _, half := range [][]string{members[:perSegment], members[perSegment:]} {
		batch := make([]searchengine.Document, 0, perSegment)
		for i, id := range half {
			batch = append(batch, bucketDoc(id, i+1))
		}
		if err := eng.Add(batch); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	constituents := eng.BucketConstituents(target, swapBucketCount)
	if len(constituents) != 2 {
		t.Fatalf("BucketConstituents before = %d ids, want 2 (the bucket is split)", len(constituents))
	}

	dead := []searchengine.ExternalID{members[0], members[1], members[perSegment+1]}
	if _, err := eng.ReplaceBucket(target, swapBucketCount, constituents, dead, nil); err != nil {
		t.Fatalf("ReplaceBucket(pure delete): %v", err)
	}

	after := eng.BucketConstituents(target, swapBucketCount)
	if len(after) != 1 {
		t.Fatalf("BucketConstituents after = %d ids, want 1 consolidated segment", len(after))
	}

	wantDocs := len(members) - len(dead)
	var found bool
	for _, blob := range eng.Export() {
		if blob.ID != after[0] {
			continue
		}
		found = true
		if blob.DocCount != wantDocs {
			t.Fatalf("consolidated DocCount = %d, want %d survivors", blob.DocCount, wantDocs)
		}
	}
	if !found {
		t.Fatalf("the consolidated segment %s is not in Export()", after[0])
	}

	// The deleted members are gone; every survivor is still resolvable.
	for _, id := range dead {
		if _, ok := eng.VectorByID(id); ok {
			t.Fatalf("superseded id %s survived the consolidation", id)
		}
	}
	for i, id := range members {
		if i == 0 || i == 1 || i == perSegment+1 {
			continue
		}
		if _, ok := eng.VectorByID(id); !ok {
			t.Fatalf("survivor %s was lost by the consolidation", id)
		}
	}
}
