// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestEncodeGraphV3ErrorsAtBlobCeilingPerDtype drives the u32 blob ceiling at
// BOTH dtypes and checks the derived per-width node bound against the figures
// the width-scaling research published.
//
// THE EXPECTATIONS ARE EXTERNAL. The three node counts below are not recomputed
// from the same formula the code uses — that would be an identity check, true no
// matter what the formula said. They are the values research recorded from real
// builds, so a change to the per-node model or the ceiling breaks this test.
func TestEncodeGraphV3ErrorsAtBlobCeilingPerDtype(t *testing.T) {
	// SHIPPED VALUE ASSERTED FIRST, before anything lowers it — the protocol
	// TestEncodeV3RejectsOversizeBlob established, so a ceiling left permanently
	// lowered by a careless edit cannot hide behind this test.
	require.Equal(t, int64(math.MaxUint32), maxBlobBytes,
		"shipped maxBlobBytes must be math.MaxUint32 — the ceiling has been left lowered")

	t.Run("derived bound matches the published per-width ceilings", func(t *testing.T) {
		// Published by the width-scaling research: 13.25M nodes at w=32, 979k at w=4096 (1024-dim
		// float32), 506k at w=8192 (2048-dim float32). maxNodesPerSegment applies
		// the safety divisor on top, so the hard ceiling is the value times it.
		for _, tc := range []struct {
			vecBytes  int
			published int
		}{
			{32, 13_250_000},
			{4096, 979_000},
			{8192, 506_000},
		} {
			hard := maxNodesPerSegment(tc.vecBytes) * v3SealSafetyDivisor
			// Within 0.5% of the published figure, which is rounded to three or
			// four significant digits in the research node.
			delta := math.Abs(float64(hard-tc.published)) / float64(tc.published)
			require.Less(t, delta, 0.005,
				"derived hard ceiling %d at width %d must match the published %d", hard, tc.vecBytes, tc.published)
		}

		// The bound FALLS as the vector widens — the property that makes wide
		// float32 segments seal smaller. Asserted as an ordering so it cannot be
		// satisfied by a constant.
		require.Greater(t, maxNodesPerSegment(32), maxNodesPerSegment(4096))
		require.Greater(t, maxNodesPerSegment(4096), maxNodesPerSegment(8192))
		require.Equal(t, maxNodesPerSegment(8192), MaxSegmentDocsForWidth(8192),
			"the exported mirror must be the same value owners clamp with")
	})

	// The encoder refuses at the boundary for each dtype, driven through the real
	// encode path by lowering the seam — a blob that genuinely crosses 4 GiB is
	// not constructible in a test.
	for _, tc := range []struct {
		name  string
		dtype byte
		dim   int
	}{
		{"ubinary", dtypeUbinary, defaultVecBytes / 4},
		{"float32", dtypeFloat32, 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := maxBlobBytes
			t.Cleanup(func() { maxBlobBytes = restore })

			items := float32Items(24, tc.dim)
			g := buildBinaryHNSWSerialDeterministic(items, tc.dim*4, tc.dtype, defaultM, defaultEfConstruction)

			// KNOWN-POSITIVE CONTROL, in this same subtest: at the shipped ceiling
			// this graph encodes cleanly. Without it an encoder that errored
			// unconditionally would satisfy the assertion below for the wrong reason.
			blob, err := encodeGraphV3(g)
			require.NoError(t, err, "control: the fixture must encode cleanly at the shipped ceiling")
			require.Equal(t, tc.dtype, blob[v3HdrDtype], "control: and it must carry this dtype")

			// Lower the ceiling to just under what this graph actually needs, so the
			// refusal is a genuine boundary crossing rather than an absurd value.
			maxBlobBytes = int64(len(blob)) - 1
			_, err = encodeGraphV3(g)
			require.Error(t, err, "encodeGraphV3 accepted a blob past the ceiling; the size guard is not wired")
			require.Contains(t, err.Error(), "ceiling", "the error must name the ceiling it tripped")

			// And exactly AT the requirement it succeeds — the boundary is where the
			// test says it is, not one of an unbounded set of failing values.
			maxBlobBytes = int64(len(blob))
			_, err = encodeGraphV3(g)
			require.NoError(t, err, "a blob exactly at the ceiling must be accepted")
		})
	}
}

// TestMergeRespectsPerDtypeSealTarget proves the merge path checks the per-dtype
// node bound BEFORE building, rather than doing the whole insertion and letting
// the encoder refuse afterwards.
func TestMergeRespectsPerDtypeSealTarget(t *testing.T) {
	restore := maxBlobBytes
	t.Cleanup(func() { maxBlobBytes = restore })

	const dim = 16
	vecBytes := dim * 4

	// THE BOUND IS PER-WIDTH, NOT A FIXED NUMBER — asserted at the SHIPPED
	// ceiling, before anything is lowered. It cannot be checked after the
	// lowering below: at a ceiling small enough to admit four nodes, halving the
	// width moves the result by less than one node and both sides floor to the
	// same integer, so the comparison would be too coarse to discriminate rather
	// than false. Without this guard a hardcoded bound would satisfy every
	// assertion in this test.
	require.Greater(t, maxNodesPerSegment(vecBytes/2), maxNodesPerSegment(vecBytes),
		"a narrower vector must admit more nodes per segment at the same ceiling")

	mkSeg := func(t *testing.T, ids []string) searchengine.Segment[[]byte, struct{}] {
		t.Helper()
		items := float32Items(len(ids), dim)
		docs := make([]searchengine.Document, len(ids))
		for i, id := range ids {
			docs[i] = searchengine.Document{ID: id, Vector: items[i].vec}
		}
		seg, err := Format{}.Build(docs)
		require.NoError(t, err)
		return seg
	}

	a := mkSeg(t, []string{"a1", "a2", "a3", "a4"})
	b := mkSeg(t, []string{"b1", "b2", "b3", "b4"})
	accept := []func(searchengine.ExternalID) bool{nil, nil}

	// KNOWN-POSITIVE FIRST: at the shipped ceiling these consolidate normally, so
	// the refusal below is attributable to the bound and not to these segments.
	merged, err := mergeSegments(t, []searchengine.Segment[[]byte, struct{}]{a, b}, accept)
	require.NoError(t, err, "control: a small merge must succeed at the shipped ceiling")
	require.Len(t, merged.IDs(), 8)

	// Lower the ceiling until the per-segment bound is 4 nodes, so 8 survivors
	// exceed it. Solving maxNodesPerSegment(vecBytes) == 4 for the ceiling:
	// ceiling = 4 * divisor * (overhead + vecBytes), rounded UP — truncating the
	// float here lands one whole node short, because maxNodesPerSegment floors
	// the division before applying the divisor. The require below is what caught
	// that, which is why it is an equality on the derived bound rather than a
	// comment asserting the arithmetic.
	maxBlobBytes = int64(math.Ceil(4 * v3SealSafetyDivisor * (v3PerNodeOverheadBytes + float64(vecBytes))))
	require.Equal(t, 4, maxNodesPerSegment(vecBytes),
		"the lowered ceiling must put the per-segment bound exactly at 4 for this test to mean what it says")

	_, err = mergeSegments(t, []searchengine.Segment[[]byte, struct{}]{a, b}, accept)
	require.Error(t, err, "a merge whose survivors exceed the per-dtype bound must be refused")
	msg := err.Error()
	require.Contains(t, msg, "8 survivors", "the error names how many survivors there were")
	require.Contains(t, msg, "4-node", "the error names the per-segment ceiling this width admits")
	require.Contains(t, msg, "per-segment ceiling",
		"the error explains WHAT was exceeded, so the operator can act on it")
}
