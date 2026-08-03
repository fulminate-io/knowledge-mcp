// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestBlobProtoRoundTripsDocCount pins the doc_count plumbing: blobToProto carries
// SegmentBlob.DocCount into the wire carrier and blobFromProto reads it back
// unchanged, so the per-segment live doc count survives the ship → store → list
// round-trip the coverage levers depend on. (Relocated from the deleted source_test.go
// — blobToProto/blobFromProto survive the SegmentService deletion.)
func TestBlobProtoRoundTripsDocCount(t *testing.T) {
	t.Parallel()

	orig := searchengine.SegmentBlob{
		ID:         "seg-dc",
		Format:     "hnsw",
		Generation: 7,
		DocCount:   1024,
		Bytes:      []byte("payload"),
	}
	p := blobToProto(orig)
	require.Equal(t, int32(1024), p.GetDocCount(), "blobToProto carries DocCount into the proto")
	back := blobFromProto(p)
	require.Equal(t, orig.DocCount, back.DocCount, "blobFromProto reads DocCount back unchanged")
	require.Equal(t, orig.ID, back.ID)
	require.Equal(t, orig.Format, back.Format)
	require.Equal(t, orig.Generation, back.Generation)
}
