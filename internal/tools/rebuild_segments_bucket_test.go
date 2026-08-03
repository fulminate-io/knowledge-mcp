// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// groupRecordingShipper records the MEMBERSHIP of every partition the driver stages, so
// a test can reason about which segments a rebuild would emit rather than only how
// many. Each StageRebuildPartition call is one group; its member ids are captured in
// order of arrival.
type groupRecordingShipper struct {
	mu         sync.Mutex
	groups     [][]string
	fieldGroup int

	// Delta-path recording. deltaGroups holds each ReEmitRebuiltDelta call's document
	// ids as one group (so churn is measured the same way for a re-emitted partition
	// set as for a built one); finalizes counts the single serial finalize a from-scratch
	// run performs; deltaBucketCount scripts the partition count the re-emit reports
	// having run at.
	deltaGroups      [][]string
	finalizes        int
	deltaBucketCount int

	fakeRebuildState
}

// StageRebuildPartition captures one partition's vector membership, and counts the
// field share alongside it. Both formats arrive in ONE call, so a test asserting the
// two corpora agree reads a count rather than a correlation across two entry points.
func (s *groupRecordingShipper) StageRebuildPartition(
	_ context.Context, _ kgtypes.GraphType, _ string, hnswDocs, bm25Docs []searchengine.Document,
) error {
	ids := make([]string, 0, len(hnswDocs))
	for _, d := range hnswDocs {
		ids = append(ids, d.ID)
	}
	s.mu.Lock()
	s.groups = append(s.groups, ids)
	if len(bm25Docs) > 0 {
		s.fieldGroup++
	}
	s.mu.Unlock()
	return nil
}

// FinalizeRebuild records the single serial finalize. It stages nothing and ships
// nothing, so this fake still has no ship surface at all.
func (s *groupRecordingShipper) FinalizeRebuild(
	context.Context, kgtypes.GraphType, string,
) (RebuildFinalizeResult, error) {
	s.mu.Lock()
	s.finalizes++
	s.mu.Unlock()
	return RebuildFinalizeResult{Swapped: true}, nil
}

func (s *groupRecordingShipper) InvalidateLocal(kgtypes.GraphType, string, []searchengine.SegmentID) {
}

// ReEmitRebuiltDelta records the delta path's documents as ONE group, keyed the same
// way an Added group is, so the churn measure counts a re-emitted partition set
// exactly as it counts a built one. It reports the scripted partition count.
func (s *groupRecordingShipper) ReEmitRebuiltDelta(
	_ context.Context, _ kgtypes.GraphType, _ string, hnswDocs, _ []searchengine.Document,
) (RebuildDeltaResult, error) {
	ids := make([]string, 0, len(hnswDocs))
	for _, d := range hnswDocs {
		ids = append(ids, d.ID)
	}
	s.mu.Lock()
	s.deltaGroups = append(s.deltaGroups, ids)
	count := s.deltaBucketCount
	s.mu.Unlock()
	if count == 0 {
		count = 1
	}
	return RebuildDeltaResult{Swapped: true, Applicable: true, DerivedBucketCount: count}, nil
}

// PublishedManifestCount: this double records GROUP MEMBERSHIP, never a published
// manifest, so it has no cardinality to report and says so. The driver skips its
// read-back check on the error, which is the correct outcome for a fixture that
// models no server.
func (s *groupRecordingShipper) PublishedManifestCount(context.Context, kgtypes.GraphType, string, string) (int, error) {
	return 0, errors.New("groupRecordingShipper: no published manifest modelled")
}

// identities returns one stable identity per recorded group: the sorted member
// ids joined. Two runs that emit the same membership produce the same identity,
// which is what makes "how many segments changed" measurable without building
// real blobs.
func (s *groupRecordingShipper) identities() map[string]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]struct{}, len(s.groups))
	for _, g := range s.groups {
		members := append([]string(nil), g...)
		sort.Strings(members)
		out[strings.Join(members, ",")] = struct{}{}
	}
	return out
}

// bucketScanPage builds one scan page from explicit ids.
func bucketScanPage(ids []string) []*knowledgev1.PipelineScanItem {
	page := make([]*knowledgev1.PipelineScanItem, 0, len(ids))
	for i, id := range ids {
		vec := make([]byte, 32)
		vec[0] = byte(i)
		page = append(page, &knowledgev1.PipelineScanItem{
			NodeId:       id,
			GraphName:    "myrepo",
			BinaryVector: vec,
			Bm25Fields:   &knowledgev1.Bm25Fields{SymbolName: id},
		})
	}
	return page
}

// runRebuildOver drives the rebuild core over exactly these ids and returns the
// recorded groups plus the reported counts.
func runRebuildOver(t *testing.T, ids []string) (*groupRecordingShipper, int, int) {
	t.Helper()
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{bucketScanPage(ids)}}
	shipper := &groupRecordingShipper{}
	out, err := RebuildSegments(
		context.Background(), scanner, shipper, kgtypes.GraphCode, "myrepo", false)
	require.NoError(t, err)
	require.True(t, out.Ran)
	return shipper, out.Built, out.Partial
}

// baseNodeIDs builds n ascending node ids.
func baseNodeIDs(n int) []string {
	ids := make([]string, 0, n)
	for i := range n {
		ids = append(ids, fmt.Sprintf("node-%08d", i))
	}
	return ids
}

// TestRebuildEmitsOneSegmentPerBucket asserts the rebuild emits exactly one
// segment per hash bucket, and that EVERY id in a segment belongs to that
// segment's bucket — the one-node-one-bucket invariant, checked over the whole
// corpus rather than a sample. A driver that groups by position instead of by
// hash fails on the very first group.
func TestRebuildEmitsOneSegmentPerBucket(t *testing.T) {
	const corpus = 4096
	wantBuckets := searchengine.BucketCountFor(corpus)
	require.Equal(t, 4, wantBuckets, "the fixture is sized so the bucket count is 4")

	shipper, built, partial := runRebuildOver(t, baseNodeIDs(corpus))

	require.Len(t, shipper.groups, wantBuckets, "one Add per bucket")
	require.Equal(t, wantBuckets, built, "built counts the buckets emitted")
	require.Equal(t, 0, partial, "bucketing has no sub-threshold tail concept")

	// Every group is bucket-pure, and together the groups cover the corpus exactly.
	seen := make(map[string]struct{}, corpus)
	for _, group := range shipper.groups {
		require.NotEmpty(t, group)
		bucket := searchengine.BucketOf(group[0], wantBuckets)
		for _, id := range group {
			require.Equal(t, bucket, searchengine.BucketOf(id, wantBuckets),
				"id %s landed in a segment holding bucket %d", id, bucket)
			require.NotContains(t, seen, id, "id %s was emitted twice", id)
			seen[id] = struct{}{}
		}
	}
	require.Len(t, seen, corpus, "every scanned node reached exactly one segment")
}

// TestRebuildIsChurnProportionalUnderInsert is the REPRODUCTION of the defect this
// phase fixes: inserting ONE node must not rewrite the whole corpus. With
// positional chunking, membership is a function of a node's ordinal position, so a
// node that sorts first shifts every downstream chunk and every segment changes.
// With hash bucketing, membership is a function of the node, so only the bucket
// receiving the new node changes.
//
// The corpus size sits comfortably inside one bucket-count band (both runs derive
// the same count), so a legitimate re-bucketing at a power-of-two boundary cannot
// be mistaken for the defect.
//
// ITS SCOPE IS FULL-REBUILD MEMBERSHIP, AND ONLY THAT. runRebuildOver builds a FRESH
// shipper per call and scans the whole id list (fakeRebuildState reports a zero
// watermark), so this compares two INDEPENDENT FULL rebuilds. It is structurally
// incapable of observing either delta-path defect: the layer accumulation needs
// engine state carried ACROSS runs, and the thin append needs a WATERMARK-SCOPED scan.
// Neither condition exists here. That is stated rather than left implicit because a
// reader who believes this test covers delta behaviour will not write the one that
// does — which is exactly how both defects reached production under a green suite.
// The delta-scoped shape is TestRebuildChurnUnderADeltaScopedScan below.
func TestRebuildIsChurnProportionalUnderInsert(t *testing.T) {
	const corpus = 5000
	base := baseNodeIDs(corpus)
	// Sorts ahead of every base id, so under positional chunking it shifts all of them.
	withInsert := append([]string{"aaa-inserted"}, base...)

	require.Equal(t, searchengine.BucketCountFor(len(base)), searchengine.BucketCountFor(len(withInsert)),
		"the fixture must not straddle a bucket-count doubling, or a full re-emit is correct behavior")

	beforeShipper, _, _ := runRebuildOver(t, base)
	afterShipper, _, _ := runRebuildOver(t, withInsert)

	before, after := beforeShipper.identities(), afterShipper.identities()
	var changed []string
	for id := range before {
		if _, survived := after[id]; !survived {
			changed = append(changed, id)
		}
	}
	require.LessOrEqual(t, len(changed), 2,
		"inserting one node rewrote %d of %d segments — churn must be proportional to the change, not to the corpus",
		len(changed), len(before))
}

// TestRebuildChurnUnderADeltaScopedScan is the shape the fixture above cannot reach:
// a run whose scan was SCOPED BY A WATERMARK to the single node that changed.
//
// It is the driver-side half of the claim, and naming the other half here is
// deliberate. This package's doubles model no engine — tools does not import
// segmentdist — so what is provable here is which finalize the driver chose and what
// it handed over. The SEGMENT-STATE half (manifest cardinality unchanged, exactly one
// blob name different, no single-document segment) is
// TestRebuildDeltaReEmitsOwningBucketOnly in segmentdist. Neither package can carry
// the whole claim, and a reader who finds only one will assume the other is missing.
//
// WHAT IT WOULD HAVE CAUGHT. Before the fix a delta-scoped run went through the same
// Add/Seal/Flush sequence a full rebuild does, so the one changed node was sealed into
// a segment of its own and published beside every untouched bucket blob. That is
// invisible to a fixture that always scans the full corpus — which is why this defect
// shipped under a green suite.
func TestRebuildChurnUnderADeltaScopedScan(t *testing.T) {
	const changedNode = "node-00000042"
	shipper := &groupRecordingShipper{}
	// A NON-ZERO persisted watermark is what makes the run window-scoped. Without it the
	// driver treats the run as corpus-complete — correctly, since a graph with no record
	// has never been rebuilt — and this test would silently become another full-rebuild
	// case.
	shipper.watermark = 1_600_000_000_000_000_000
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{bucketScanPage([]string{changedNode})}}

	out, err := RebuildSegments(
		context.Background(), scanner, shipper, kgtypes.GraphCode, "myrepo", false)
	require.NoError(t, err)
	require.True(t, out.Ran)

	// The finalize went through the partition re-emit, carrying exactly the changed
	// node — for BOTH formats, since they hold separate manifests over the same node.
	require.Len(t, shipper.deltaGroups, 1, "a delta-scoped run finalizes through ONE partition re-emit")
	require.Equal(t, []string{changedNode}, shipper.deltaGroups[0],
		"the re-emit receives exactly the changed node, ungrouped")

	// And it never touched the from-scratch path. Each of these would be a distinct
	// regression: a staged partition means the window was laid out as a layer of its
	// own, and a reset finalize means the run replaced the corpus it was supposed to
	// amend.
	require.Empty(t, shipper.groups, "a delta must not stage the window as a reset partition")
	require.Zero(t, shipper.fieldGroup, "nor stage its field share")
	require.Zero(t, shipper.finalizes, "nor run the reset finalize — a delta amends the corpus in place")

	// Built describes what CHANGED in the corpus, not how many items were scanned.
	require.Equal(t, 1, out.Scanned)
	require.Positive(t, out.Built, "a delta that re-emitted a partition reports it")
}
