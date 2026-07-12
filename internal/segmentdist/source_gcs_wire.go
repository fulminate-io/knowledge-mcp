// SPDX-License-Identifier: Apache-2.0

package segmentdist

import "context"

// =============================================================================
// GCS-agent segment control channel — the hand-mirrored T1 wire contract.
//
// The gcsSegmentSource (source_gcs.go) ships each content-hash segment as a
// full-object presigned PUT to GCS (envelope-sealed via syncgcs), publishes
// completeness through the agent manifest endpoint (HEAD-verify + CAS), and fetches
// via agent presign-GET → GetObject → push-shape decrypt. The agent control plane is
// reached over the segmentControlTransport seam (/v1/segments/<path>); the bulk
// (encrypted) blob bytes go straight to/from GCS off-band.
//
// CROSS-MODULE MIRROR + NO knowledge/gen for the agent path: the JSON DTOs below are
// hand-mirrored field-for-field and json-tag-for-json-tag against the T1 agent
// segment endpoints (cmd/agent/internal/http/server/segment_registry.go,
// segment_fetch.go, segment_manifest.go). The two repos cannot share a Go package —
// the only sanctioned cross-module contract is generated protobuf (see AGENTS.md) —
// so each side owns its own DTO copy and the wire JSON is the shared contract. Any
// change here is a wire-format change requiring a coordinated agent update. This
// mirrors the convention transcriptsync/wire.go and tools/intercept_sync.go already
// use for the graph-sync DTOs.
// =============================================================================

// segmentControlTransport is the small-control-request seam the GCS source drives
// for the /v1/segments/ POSTs (presign-batch / fetch-batch / manifest publish +
// read). It is the exact method the production *auth.Transport already exposes
// (SegmentControlJSON), so the transport satisfies it with no adapter — every call
// is a Bearer-authenticated POST to /v1/segments/<path> with 401-refresh-retry
// handled inside the transport. Keeping the source to this interface (rather than
// the concrete transport) decouples it from the concrete transport and makes it
// fake-testable. Mirrors transcriptsync.ControlTransport (seams.go). PublishManifest
// classifies the transport's *auth.SyncHTTPError to detect the manifest 409 (an
// incomplete publish) — the one place the source reads a transport-typed error.
type segmentControlTransport interface {
	SegmentControlJSON(ctx context.Context, path string, body []byte) ([]byte, error)
}

// SegmentControlTransport is the EXPORTED alias of the segmentControlTransport seam,
// so external callers (bootstrap) can type the WithSegmentTransport builder and
// in-package tests can inject a fake. The production *auth.Transport satisfies it via
// SegmentControlJSON.
type SegmentControlTransport = segmentControlTransport

// --- presign (Ship) --------------------------------------------------------

// segmentPresignChunk is one blob's content hash in a batch presign request. The
// batch carries the (graph_type, name, format) tuple once at the top level.
// Mirrors the agent's segmentPresignChunk (segment_registry.go).
type segmentPresignChunk struct {
	ContentHash string `json:"content_hash"`
}

// segmentPresignBatchRequest is the body of POST /v1/segments/presign-batch: a
// batch-level (graph_type, name, format) plus an ordered array of chunk content
// hashes. The response pairs to Chunks BY INDEX, so order is significant. Mirrors
// the agent's segmentPresignBatchRequest.
type segmentPresignBatchRequest struct {
	GraphType string                `json:"graph_type"`
	Name      string                `json:"name"`
	Format    string                `json:"format"`
	Chunks    []segmentPresignChunk `json:"chunks"`
}

// presignResponse is one presign result: a presigned GCS PUT URL, the agent-minted
// object path (bound into the envelope AAD), the agent's RSA public key the DEK is
// wrapped to, and an expiry. Mirrors the agent's presignResponse (shared by the
// single + batch presign handlers).
type presignResponse struct {
	UploadURL      string `json:"upload_url"`
	ObjectPath     string `json:"object_path"`
	AgentPublicKey string `json:"agent_public_key"`
	Expiry         string `json:"expiry"`
}

// segmentPresignBatchResponse is the reply to presign-batch: an ordered array of
// per-chunk presign results, index-parallel to the request Chunks (every element
// carries the SAME agent_public_key). Mirrors the agent's
// segmentPresignBatchResponse.
type segmentPresignBatchResponse struct {
	Chunks []presignResponse `json:"chunks"`
}

// --- fetch (Fetch) ---------------------------------------------------------

// segmentFetchChunk is one object path in a batch fetch request. Mirrors the
// agent's segmentFetchRequest (the batch element type).
type segmentFetchChunk struct {
	ObjectPath string `json:"object_path"`
}

// segmentFetchBatchRequest is the body of POST /v1/segments/fetch-batch: an ordered
// array of object paths. The response Results array is index-parallel. Mirrors the
// agent's segmentFetchBatchRequest.
type segmentFetchBatchRequest struct {
	Chunks []segmentFetchChunk `json:"chunks"`
}

// segmentFetchElementResult is the per-element outcome in a batch fetch response.
// OK=true carries download_url + dek (base64-StdEncoded); OK=false carries the
// per-element error code. Mirrors the agent's segmentFetchElementResult.
type segmentFetchElementResult struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	DEK         string `json:"dek,omitempty"`
	ObjectPath  string `json:"object_path,omitempty"`
}

// segmentFetchBatchResponse is the reply to fetch-batch: a request-order-parallel
// array of per-element results. Mirrors the agent's segmentFetchBatchResponse.
type segmentFetchBatchResponse struct {
	Results []segmentFetchElementResult `json:"results"`
}

// --- manifest (PublishManifest / List / Fetch resolution) ------------------

// manifestDigest is one blob's identity in a publish request: its content hash plus
// a per-digest doc_count annotation. Mirrors the agent's manifestDigest (the shape
// PERSISTED in the manifest).
type manifestDigest struct {
	ContentHash string `json:"content_hash"`
	DocCount    int    `json:"doc_count"`
}

// manifestPublishRequest is the body of POST /v1/segments/manifest/publish: the
// graph + format identity plus the full set of blob digests to seal into one
// manifest. Mirrors the agent's manifestPublishRequest.
type manifestPublishRequest struct {
	GraphType string           `json:"graph_type"`
	Name      string           `json:"name"`
	Format    string           `json:"format"`
	Digests   []manifestDigest `json:"digests"`
}

// manifestPublishResponse is the reply to manifest/publish. On the genuine-absence
// path OK=false and Missing carries the content hashes that were not present (the
// 409 case); on success OK=true. Mirrors the agent's manifestPublishResponse.
type manifestPublishResponse struct {
	OK      bool     `json:"ok"`
	Missing []string `json:"missing,omitempty"`
}

// manifestReadRequest is the body of POST /v1/segments/manifest/read. Mirrors the
// agent's manifestReadRequest.
type manifestReadRequest struct {
	GraphType string `json:"graph_type"`
	Name      string `json:"name"`
	Format    string `json:"format"`
}

// manifestReadDigest is a READ-RESPONSE digest: the stored {content_hash,
// doc_count} enriched with the server-minted object_path (a fresh reader holds no
// accountID and cannot reconstruct the account-scoped blob key). Mirrors the
// agent's manifestReadDigest.
type manifestReadDigest struct {
	ContentHash string `json:"content_hash"`
	DocCount    int    `json:"doc_count"`
	ObjectPath  string `json:"object_path"`
}

// manifestReadResponse is the reply to manifest/read: Found + Format + fully
// resolved digests (each with a minted object_path). An absent manifest returns
// {found:false} (HTTP 200). Mirrors the agent's manifestReadResponse.
type manifestReadResponse struct {
	Found   bool                 `json:"found"`
	Format  string               `json:"format"`
	Digests []manifestReadDigest `json:"digests,omitempty"`
}
