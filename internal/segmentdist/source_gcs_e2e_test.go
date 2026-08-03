// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestGCSCloudPathE2E is the full cloud round-trip against a fake agent control
// plane + fake GCS: a REAL producer Manager (logged-in, GCS transport) ships a
// corpus as full GCS objects and publishes HEAD-verified manifests carrying real
// doc_counts; a fresh cold-L2 consumer Manager loads the corpus from the manifests
// (manifest/read → presign-GET → OpenPushObject) and Search returns the expected
// doc; no per-blob confirm call is made (the fake has no confirm route, so any
// confirm attempt would error the round-trip); and a synthetic missing-blob publish
// is rejected 409.
func TestGCSCloudPathE2E(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	backend := newFakeSegmentBackend(t)
	transport := WithSegmentTransport(func() (SegmentControlTransport, error) { return backend, nil })

	const (
		gt   = kgtypes.GraphKnowledge
		name = "kg"
	)
	docs, targetID, targetVec, uniqueTerm := searchCorpus(7)

	// --- Producer: ship both formats as full GCS objects + publish manifests. ---
	producer := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, transport)
	require.NoError(t, producer.AddAndMarkDirty(ctx, gt, name, docs))       // HNSW
	require.NoError(t, producer.AddAndMarkDirtyFields(ctx, gt, name, docs)) // BM25
	require.NoError(t, producer.Flush(ctx, gt, name))                       // seal any sub-threshold tail

	// Full objects landed in fake GCS (one object per content hash), a presign-batch
	// was issued, and NO confirm call was made (the fake would have errored on it).
	require.Positive(t, backend.objectCount(), "the producer PUT full segment objects into GCS")
	require.Positive(t, backend.presignBatchCalls, "the producer presigned via presign-batch")
	require.Positive(t, backend.publishCalls, "the producer published HEAD-verified manifests")

	// The published HNSW manifest carries real doc_counts (not 0).
	hnswSrc := newGCSSegmentSource(backend, string(gt), name, "hnsw")
	hnswDigests, found, err := hnswSrc.readManifest(ctx)
	require.NoError(t, err)
	require.True(t, found, "the HNSW manifest was published")
	var hnswDocs int
	for _, d := range hnswDigests {
		hnswDocs += d.DocCount
	}
	require.Equal(t, searchCorpusN, hnswDocs, "the HNSW manifest carries the real summed doc_count")

	// --- Consumer: a fresh cold-L2 Manager loads from the manifests and searches. ---
	consumer := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, transport)
	fused, err := consumer.Search(ctx, gt, name, uniqueTerm, targetVec, 10)
	require.NoError(t, err)
	require.NotEmpty(t, fused, "the consumer loaded the corpus from GCS and search returns hits")
	require.Equal(t, targetID, fused[0].ID,
		"the cold-L2 consumer loaded via manifest/read + presign-GET + OpenPushObject and ranks the target #1")

	// The consumer fetched via fetch-batch (presign-GET), never a per-blob confirm.
	require.Positive(t, backend.fetchBatchCalls, "the consumer fetched blobs via fetch-batch")

	// The coverage read over the consumer returns the manifest doc counts.
	snap, err := consumer.ShippedManifestSnapshot(ctx, gt, name, "hnsw")
	require.NoError(t, err)
	covered, anyUnknown := consumer.ShippedDocCountFromSnapshot(snap, "hnsw")
	require.Equal(t, searchCorpusN, covered, "ShippedManifestSnapshot returns the manifest HNSW doc count")
	require.False(t, anyUnknown)

	// --- 409: a manifest publish referencing an un-shipped blob is rejected. ---
	_, err = hnswSrc.PublishManifest("hnsw", []segmentDigest{{ID: "never-shipped-hash", DocCount: 5}})
	var incomplete *manifestIncompleteError
	require.ErrorAs(t, err, &incomplete, "a missing-blob publish is rejected with a typed incomplete error")
	require.Contains(t, incomplete.Missing, "never-shipped-hash")
}
