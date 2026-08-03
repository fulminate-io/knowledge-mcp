// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// segBlob builds an HNSW-format SegmentBlobProto for the Ship/Fetch tests.
func segBlob(id string, docCount int, payload string) *knowledgev1.SegmentBlobProto {
	return &knowledgev1.SegmentBlobProto{
		Id:       id,
		Format:   "hnsw",
		DocCount: int32(docCount),
		Bytes:    []byte(payload),
	}
}

// metaIDs extracts + sorts the ids of stamped metas for order-independent assertions.
func metaIDs(metas []*knowledgev1.SegmentMetaProto) []string {
	out := make([]string, 0, len(metas))
	for _, m := range metas {
		out = append(out, m.GetId())
	}
	sort.Strings(out)
	return out
}

// TestSegmentDTOWireContract pins the hand-mirrored client DTOs to the agent's T1
// wire contract: it (1) unmarshals literals matching the agent handler output into
// the response DTOs and asserts the decoded fields, and (2) marshals each request
// DTO and asserts the exact snake_case tags the agent handlers read. A drift on
// either side (a renamed tag, a dropped field) fails here rather than silently
// mis-wiring the agent round-trip.
func TestSegmentDTOWireContract(t *testing.T) {
	t.Parallel()

	// (1) Response DTOs decode the agent's on-the-wire JSON.
	t.Run("presign_batch_response_decodes", func(t *testing.T) {
		const in = `{"chunks":[{"upload_url":"https://gcs/put","object_path":"segments/a/knowledge/default/hnsw/h1.seg","agent_public_key":"-----PEM-----","expiry":"2026-01-01T00:00:00Z"}]}`
		var got segmentPresignBatchResponse
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got.Chunks) != 1 {
			t.Fatalf("chunks len = %d, want 1", len(got.Chunks))
		}
		c := got.Chunks[0]
		if c.UploadURL != "https://gcs/put" || c.ObjectPath != "segments/a/knowledge/default/hnsw/h1.seg" ||
			c.AgentPublicKey != "-----PEM-----" || c.Expiry != "2026-01-01T00:00:00Z" {
			t.Fatalf("presignResponse decoded wrong: %+v", c)
		}
	})

	t.Run("fetch_batch_response_decodes", func(t *testing.T) {
		const in = `{"results":[{"ok":true,"download_url":"https://gcs/get","dek":"YmFzZTY0","object_path":"segments/a/knowledge/default/hnsw/h1.seg"},{"ok":false,"error":"not_found"}]}`
		var got segmentFetchBatchResponse
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got.Results) != 2 {
			t.Fatalf("results len = %d, want 2", len(got.Results))
		}
		if !got.Results[0].OK || got.Results[0].DownloadURL != "https://gcs/get" ||
			got.Results[0].DEK != "YmFzZTY0" || got.Results[0].ObjectPath != "segments/a/knowledge/default/hnsw/h1.seg" {
			t.Fatalf("ok result decoded wrong: %+v", got.Results[0])
		}
		if got.Results[1].OK || got.Results[1].Error != "not_found" {
			t.Fatalf("err result decoded wrong: %+v", got.Results[1])
		}
	})

	t.Run("manifest_read_response_decodes", func(t *testing.T) {
		const in = `{"found":true,"format":"hnsw","digests":[{"content_hash":"h1","doc_count":42,"object_path":"segments/a/knowledge/default/hnsw/h1.seg"}]}`
		var got manifestReadResponse
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !got.Found || got.Format != "hnsw" || len(got.Digests) != 1 {
			t.Fatalf("manifestReadResponse decoded wrong: %+v", got)
		}
		d := got.Digests[0]
		if d.ContentHash != "h1" || d.DocCount != 42 || d.ObjectPath != "segments/a/knowledge/default/hnsw/h1.seg" {
			t.Fatalf("manifestReadDigest decoded wrong: %+v", d)
		}
	})

	t.Run("manifest_read_not_found_decodes", func(t *testing.T) {
		var got manifestReadResponse
		if err := json.Unmarshal([]byte(`{"found":false}`), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Found || len(got.Digests) != 0 {
			t.Fatalf("found:false decoded wrong: %+v", got)
		}
	})

	t.Run("manifest_publish_response_decodes", func(t *testing.T) {
		var ok manifestPublishResponse
		if err := json.Unmarshal([]byte(`{"ok":true}`), &ok); err != nil || !ok.OK || len(ok.Missing) != 0 {
			t.Fatalf("ok publish decoded wrong: %+v err=%v", ok, err)
		}
		var conflict manifestPublishResponse
		if err := json.Unmarshal([]byte(`{"ok":false,"missing":["h1","h2"]}`), &conflict); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if conflict.OK || len(conflict.Missing) != 2 || conflict.Missing[0] != "h1" || conflict.Missing[1] != "h2" {
			t.Fatalf("conflict publish decoded wrong: %+v", conflict)
		}
	})

	// (2) Request DTOs emit the exact snake_case tags the agent handlers read.
	t.Run("request_dtos_marshal_snake_case", func(t *testing.T) {
		cases := []struct {
			name string
			v    any
			want string
		}{
			{
				name: "presign_batch_request",
				v: segmentPresignBatchRequest{
					GraphType: "knowledge", Name: "default", Format: "hnsw",
					Chunks: []segmentPresignChunk{{ContentHash: "h1"}},
				},
				want: `{"graph_type":"knowledge","name":"default","format":"hnsw","chunks":[{"content_hash":"h1"}]}`,
			},
			{
				name: "fetch_batch_request",
				v: segmentFetchBatchRequest{
					Chunks: []segmentFetchChunk{{ObjectPath: "segments/a/knowledge/default/hnsw/h1.seg"}},
				},
				want: `{"chunks":[{"object_path":"segments/a/knowledge/default/hnsw/h1.seg"}]}`,
			},
			{
				name: "manifest_publish_request",
				v: manifestPublishRequest{
					GraphType: "knowledge", Name: "default", Format: "hnsw",
					Digests: []manifestDigest{{ContentHash: "h1", DocCount: 42}},
				},
				want: `{"graph_type":"knowledge","name":"default","format":"hnsw","digests":[{"content_hash":"h1","doc_count":42}]}`,
			},
			{
				name: "manifest_read_request",
				v:    manifestReadRequest{GraphType: "knowledge", Name: "default", Format: "hnsw"},
				want: `{"graph_type":"knowledge","name":"default","format":"hnsw"}`,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				b, err := json.Marshal(tc.v)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				if string(b) != tc.want {
					t.Fatalf("marshal =\n  %s\nwant\n  %s", b, tc.want)
				}
			})
		}
	})
}

// TestGCSShipHappyPath: Ship of 3 blobs issues ONE presign-batch, PUTs 3 distinct
// full objects, makes NO fetch/publish call, and returns 3 stamped metas whose ids
// equal the inputs; each stored object decrypts to the original bytes under Push AAD.
func TestGCSShipHappyPath(t *testing.T) {
	t.Parallel()

	b := newFakeSegmentBackend(t)
	src := newGCSSegmentSource(b, "knowledge", "default", "hnsw")

	blobs := []*knowledgev1.SegmentBlobProto{
		segBlob("h1", 10, "KGV4 segment one payload"),
		segBlob("h2", 20, "KGV4 segment two payload"),
		segBlob("h3", 30, "KGV4 segment three payload"),
	}
	metas, err := src.Ship(context.Background(), blobs)
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if got, want := metaIDs(metas), []string{"h1", "h2", "h3"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("stamped ids = %v, want %v", got, want)
	}
	if b.presignBatchCalls != 1 {
		t.Errorf("presign-batch calls = %d, want 1", b.presignBatchCalls)
	}
	if b.objectCount() != 3 {
		t.Errorf("stored objects = %d, want 3", b.objectCount())
	}
	if b.fetchBatchCalls != 0 || b.publishCalls != 0 {
		t.Errorf("Ship must make no fetch/publish call, got fetch=%d publish=%d", b.fetchBatchCalls, b.publishCalls)
	}
	// DocCount + Format stamped through.
	for _, m := range metas {
		if m.GetFormat() != "hnsw" || m.GetGeneration() != 0 {
			t.Errorf("meta %s: format=%q gen=%d", m.GetId(), m.GetFormat(), m.GetGeneration())
		}
	}
	// Each stored object decrypts to the original bytes under the Push AAD.
	for _, blob := range blobs {
		got := b.storedPlaintextForHash(t, "knowledge", "default", "hnsw", blob.GetId())
		if !bytes.Equal(got, blob.GetBytes()) {
			t.Errorf("stored object for %s decrypted to %q, want %q", blob.GetId(), got, blob.GetBytes())
		}
	}
}

// TestGCSShipPUTFailureIsFailSafeButNotSilent: with exactly one of N PUTs returning
// a non-2xx, Ship returns metas for the N-1 succeeding blobs (the failing id is
// ABSENT), the failure is logged, and the other blobs' objects are stored intact —
// the batch is not aborted and prior state is untouched.
func TestGCSShipPUTFailureIsFailSafeButNotSilent(t *testing.T) {
	t.Parallel()

	b := newFakeSegmentBackend(t)
	b.failPUTForHash("knowledge", "default", "hnsw", "h2")

	var logBuf bytes.Buffer
	src := newGCSSegmentSource(b, "knowledge", "default", "hnsw")
	src.logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	blobs := []*knowledgev1.SegmentBlobProto{
		segBlob("h1", 10, "payload one"),
		segBlob("h2", 20, "payload two — its PUT fails"),
		segBlob("h3", 30, "payload three"),
	}
	metas, err := src.Ship(context.Background(), blobs)
	if err != nil {
		t.Fatalf("Ship must not abort on a single PUT failure: %v", err)
	}
	if got, want := metaIDs(metas), []string{"h1", "h3"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("stamped ids = %v, want %v (h2 must be absent)", got, want)
	}
	// The two succeeding objects are stored intact; the failing one is not.
	if b.objectCount() != 2 {
		t.Errorf("stored objects = %d, want 2", b.objectCount())
	}
	// The failure is logged (not silent) and names the failing content hash.
	logs := logBuf.String()
	if !strings.Contains(logs, "h2") || !strings.Contains(strings.ToLower(logs), "seal/put failed") {
		t.Errorf("expected a warn log naming h2, got:\n%s", logs)
	}
	// The two succeeding objects decrypt intact (prior state untouched).
	for _, id := range []string{"h1", "h3"} {
		_ = b.storedPlaintextForHash(t, "knowledge", "default", "hnsw", id)
	}
}

// TestGCSListFromManifest: List(0) maps the manifest digests to metas (ids +
// DocCount from the manifest, Generation 0, Format from the source); a never-published
// manifest ({found:false}) returns an empty slice + nil error. sinceGen is ignored.
func TestGCSListFromManifest(t *testing.T) {
	t.Parallel()

	b := newFakeSegmentBackend(t)
	src := newGCSSegmentSource(b, "knowledge", "default", "hnsw")

	// Never published yet → empty, nil.
	metas, err := src.List(context.Background(), 0)
	if err != nil {
		t.Fatalf("List (unpublished): %v", err)
	}
	if len(metas) != 0 {
		t.Fatalf("List before publish = %d metas, want 0", len(metas))
	}

	// Ship + publish two blobs, then List returns 2 metas with matching ids + doc counts.
	blobs := []*knowledgev1.SegmentBlobProto{
		segBlob("h1", 11, "payload one"),
		segBlob("h2", 22, "payload two"),
	}
	if _, err := src.Ship(context.Background(), blobs); err != nil {
		t.Fatalf("Ship: %v", err)
	}
	b.seedManifest("knowledge", "default", "hnsw", map[string]int{"h1": 11, "h2": 22})

	// sinceGen is ignored — a nonzero value returns the full set just the same.
	metas, err = src.List(context.Background(), 999)
	if err != nil {
		t.Fatalf("List (published): %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("List after publish = %d metas, want 2", len(metas))
	}
	byID := map[string]searchengine.SegmentMeta{}
	for _, m := range metas {
		byID[m.ID] = m
	}
	if byID["h1"].DocCount != 11 || byID["h2"].DocCount != 22 {
		t.Errorf("DocCounts wrong: %+v", byID)
	}
	for id, m := range byID {
		if m.Format != "hnsw" || m.Generation != 0 {
			t.Errorf("meta %s: format=%q gen=%d", id, m.Format, m.Generation)
		}
	}
}

// TestGCSFetchRoundTrip: Fetch resolves object_paths via manifest/read, issues one
// fetch-batch, GETs each object, push-decrypts with the returned DEK, and returns
// SegmentBlobs whose bytes equal the originals; a per-element ok:false (a missing id)
// is skipped without failing the batch.
func TestGCSFetchRoundTrip(t *testing.T) {
	t.Parallel()

	b := newFakeSegmentBackend(t)
	src := newGCSSegmentSource(b, "knowledge", "default", "hnsw")

	blobs := []*knowledgev1.SegmentBlobProto{
		segBlob("h1", 11, "KGV4 fetch payload one"),
		segBlob("h2", 22, "KGV4 fetch payload two"),
	}
	if _, err := src.Ship(context.Background(), blobs); err != nil {
		t.Fatalf("Ship: %v", err)
	}
	b.seedManifest("knowledge", "default", "hnsw", map[string]int{"h1": 11, "h2": 22})

	// Request both shipped ids plus an id absent from the manifest — the absent one is
	// silently skipped (not in the manifest, so never fetched).
	got, err := src.Fetch(context.Background(), []searchengine.SegmentID{"h1", "h2", "absent"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Fetch returned %d blobs, want 2", len(got))
	}
	want := map[string]string{"h1": "KGV4 fetch payload one", "h2": "KGV4 fetch payload two"}
	for _, blob := range got {
		if string(blob.Bytes) != want[blob.ID] {
			t.Errorf("Fetch %s = %q, want %q", blob.ID, blob.Bytes, want[blob.ID])
		}
		if blob.Format != "hnsw" {
			t.Errorf("Fetch %s format = %q, want hnsw", blob.ID, blob.Format)
		}
	}
	if b.fetchBatchCalls != 1 {
		t.Errorf("fetch-batch calls = %d, want 1", b.fetchBatchCalls)
	}
}

// TestGCSPruneNoOp: Prune is a client no-op on the GCS path (the agent inline-GCs).
func TestGCSPruneNoOp(t *testing.T) {
	t.Parallel()

	src := newGCSSegmentSource(newFakeSegmentBackend(t), "knowledge", "default", "hnsw")
	n, err := src.Prune([]searchengine.SegmentID{"h1", "h2"})
	if err != nil || n != 0 {
		t.Fatalf("Prune = (%d, %v), want (0, nil)", n, err)
	}
}

// TestGCSPublishManifestHappyAnd409: after Ship PUTs the blobs, PublishManifest
// issues one manifest/publish and returns (0, nil); when a digest is missing the
// agent 409s and PublishManifest returns a typed *manifestIncompleteError carrying
// the missing hash and writes no manifest.
func TestGCSPublishManifestHappyAnd409(t *testing.T) {
	t.Parallel()

	b := newFakeSegmentBackend(t)
	src := newGCSSegmentSource(b, "knowledge", "default", "hnsw")

	blobs := []*knowledgev1.SegmentBlobProto{
		segBlob("h1", 11, "payload one"),
		segBlob("h2", 22, "payload two"),
	}
	if _, err := src.Ship(context.Background(), blobs); err != nil {
		t.Fatalf("Ship: %v", err)
	}

	// Happy path: both blobs present → (0, nil), manifest written.
	n, err := src.PublishManifest("hnsw", []segmentDigest{{ID: "h1", DocCount: 11}, {ID: "h2", DocCount: 22}})
	if err != nil || n != 0 {
		t.Fatalf("PublishManifest happy = (%d, %v), want (0, nil)", n, err)
	}
	digests, found, err := src.readManifest(context.Background())
	if err != nil || !found || len(digests) != 2 {
		t.Fatalf("manifest not written: found=%v digests=%d err=%v", found, len(digests), err)
	}
	// The per-digest doc_count is carried onto the wire (coverage-read denominator).
	dcByHash := map[string]int{}
	for _, d := range digests {
		dcByHash[d.ContentHash] = d.DocCount
	}
	if dcByHash["h1"] != 11 || dcByHash["h2"] != 22 {
		t.Fatalf("published doc_counts = %v, want h1=11 h2=22", dcByHash)
	}

	// 409 path: publish a set including an un-shipped hash → typed incomplete error,
	// and the prior manifest is NOT overwritten (still the 2-digest happy manifest).
	_, err = src.PublishManifest("hnsw", []segmentDigest{{ID: "h1", DocCount: 11}, {ID: "h2", DocCount: 22}, {ID: "h_missing", DocCount: 5}})
	var incomplete *manifestIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("expected *manifestIncompleteError, got %v", err)
	}
	if len(incomplete.Missing) != 1 || incomplete.Missing[0] != "h_missing" {
		t.Fatalf("missing hashes = %v, want [h_missing]", incomplete.Missing)
	}
	digests, found, err = src.readManifest(context.Background())
	if err != nil || !found || len(digests) != 2 {
		t.Fatalf("409 must not overwrite the prior manifest: found=%v digests=%d err=%v", found, len(digests), err)
	}
}
