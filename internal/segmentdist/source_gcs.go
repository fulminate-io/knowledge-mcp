// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/syncgcs"
)

// =============================================================================
// gcsSegmentSource — the cloud (logged-in) segment source. It ships each
// content-hash segment as a full-object presigned PUT to GCS (envelope-sealed via
// syncgcs), publishes completeness through the agent manifest endpoint (HEAD-verify
// + CAS), and fetches via agent presign-GET → GetObject → push-shape decrypt. The
// agent control plane is reached over the segmentControlTransport seam
// (/v1/segments/<path>, defined in source_gcs_wire.go with the hand-mirrored DTOs);
// the bulk (encrypted) blob bytes go straight to/from GCS off-band.
// =============================================================================

const (
	// segmentMaxBatchChunks is the per-batch object ceiling, MIRRORING the agent's
	// maxBatchChunks (segment_registry.go). A presign-batch / fetch-batch over this
	// is 400'd by the agent, so Ship/Fetch chunk larger id-sets into successive
	// batches rather than sending an over-cap request that would strand blobs.
	segmentMaxBatchChunks = 512

	// shipSegmentContentType is the content type each presigned PUT URL is signed
	// with; it MUST match what syncgcs.PutObject sends or GCS rejects the V4
	// signature. Same octet-stream the agent's segment presign signs
	// (mintSegmentBlobPath uses octetStream).
	shipSegmentContentType = "application/octet-stream"
)

// gcsSegmentSource is the cloud (logged-in) segmentSource: it ships each
// content-hash blob as a full-object presigned PUT to GCS, publishes completeness
// through the agent manifest endpoint, and fetches via agent presign-GET →
// GetObject → push-shape decrypt. It holds NO L2 cache: it is a REMOTE source, and
// the distManager owns the L2 cache separately (load() is L2-first, so the source's
// Fetch is only reached on an L2 miss). List/Fetch derive wholly from the agent
// manifest + GCS objects.
type gcsSegmentSource struct {
	transport segmentControlTransport
	// graphType is the canonical wire string (string(gt)) — stable across all four
	// agent calls. name+format scope the graph+index the source ships/reads.
	graphType string
	name      string
	format    string
	logger    *slog.Logger
}

var _ segmentSource = (*gcsSegmentSource)(nil)

// newGCSSegmentSource builds the cloud source for one graph+format. transport is the
// agent control-plane seam (the production *auth.Transport satisfies it via
// SegmentControlJSON). graphType is the canonical wire string (pass string(gt), as
// graphCacheDirFor does). No cache is threaded: the GCS source is remote and never
// reads L2 (the distManager owns the cache).
func newGCSSegmentSource(transport segmentControlTransport, graphType, name, format string) *gcsSegmentSource {
	return &gcsSegmentSource{
		transport: transport,
		graphType: graphType,
		name:      name,
		format:    format,
		logger:    slog.Default(),
	}
}

// shipConcurrency bounds the seal+PUT pool. Seal+PUT is I/O-bound, so NumCPU
// overlaps the crypto + upload latency (mirrors transcriptsync's NumCPU default).
func shipConcurrency() int {
	if n := runtime.NumCPU(); n > 0 {
		return n
	}
	return 1
}

// Ship uploads each blob as a full GCS object: one presign-batch per sub-batch
// (chunked to the agent ceiling) → bounded-parallel seal+PUT of the in-memory
// blobs → one stamped meta per SUCCESSFULLY-PUT blob. There is NO confirm call (the
// T1 segment contract has no confirm endpoint — completeness is verified later at
// PublishManifest's HEAD-verify). A presign transport / decode / length-mismatch
// error fails the WHOLE Ship (returns an error, no partial state claimed). A
// per-blob seal or PUT failure is fail-safe but NOT silent: it is logged and the id
// is OMITTED from the returned metas (the batch is not aborted), so the blob is
// absent from the resident→published set and the downstream PublishManifest
// HEAD-verify 409s on it rather than the id silently vanishing.
func (s *gcsSegmentSource) Ship(ctx context.Context, blobs []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error) {
	if len(blobs) == 0 {
		return nil, nil
	}
	metas := make([]*knowledgev1.SegmentMetaProto, 0, len(blobs))
	for start := 0; start < len(blobs); start += segmentMaxBatchChunks {
		end := min(start+segmentMaxBatchChunks, len(blobs))
		sub, err := s.shipBatch(ctx, blobs[start:end])
		if err != nil {
			return nil, err
		}
		metas = append(metas, sub...)
	}
	return metas, nil
}

// shipBatch presigns, then seal+PUTs, one sub-batch (already clamped to
// segmentMaxBatchChunks). The response length is guarded BEFORE any positional
// pairing so a mismatched reply never indexes a blob against the wrong presigned
// URL.
func (s *gcsSegmentSource) shipBatch(ctx context.Context, blobs []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error) {
	chunks := make([]segmentPresignChunk, len(blobs))
	for i, b := range blobs {
		chunks[i] = segmentPresignChunk{ContentHash: b.GetId()}
	}
	body, err := json.Marshal(segmentPresignBatchRequest{
		GraphType: s.graphType,
		Name:      s.name,
		Format:    s.format,
		Chunks:    chunks,
	})
	if err != nil {
		return nil, fmt.Errorf("segmentdist: marshal presign-batch: %w", err)
	}
	raw, err := s.transport.SegmentControlJSON(ctx, "presign-batch", body)
	if err != nil {
		return nil, fmt.Errorf("segmentdist: presign-batch: %w", err)
	}
	var presign segmentPresignBatchResponse
	if err := json.Unmarshal(raw, &presign); err != nil {
		return nil, fmt.Errorf("segmentdist: decode presign-batch response: %w", err)
	}
	if len(presign.Chunks) != len(blobs) {
		return nil, fmt.Errorf("segmentdist: presign-batch returned %d results for %d blobs", len(presign.Chunks), len(blobs))
	}

	putOK := make([]bool, len(blobs))
	putErr := make([]error, len(blobs))
	var wg sync.WaitGroup
	pool := make(chan struct{}, shipConcurrency())
	for i := range blobs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pool <- struct{}{}
			defer func() { <-pool }()
			elem := presign.Chunks[i]
			envelope, serr := syncgcs.SealEnvelope(blobs[i].GetBytes(), elem.AgentPublicKey, elem.ObjectPath)
			if serr != nil {
				putErr[i] = serr
				return
			}
			if perr := syncgcs.PutObject(ctx, elem.UploadURL, envelope, shipSegmentContentType); perr != nil {
				putErr[i] = perr
				return
			}
			putOK[i] = true
		}(i)
	}
	wg.Wait()

	metas := make([]*knowledgev1.SegmentMetaProto, 0, len(blobs))
	for i, b := range blobs {
		if !putOK[i] {
			// Fail-safe but NOT silent: log the failure with the content hash +
			// object path so a persistently-failing PUT produces an operator signal,
			// and OMIT the id (the downstream publish HEAD-verify 409s on it).
			s.logger.Warn("segment ship: seal/PUT failed — omitting blob from shipped set",
				"content_hash", b.GetId(),
				"object_path", presign.Chunks[i].ObjectPath,
				"error", putErr[i])
			continue
		}
		metas = append(metas, &knowledgev1.SegmentMetaProto{
			Id:         b.GetId(),
			Format:     b.GetFormat(),
			DocCount:   b.GetDocCount(),
			Generation: 0,
		})
	}
	return metas, nil
}

// readManifest fetches the current published manifest for this graph+format via one
// manifest/read control call. A {found:false} reply (never published) returns
// (nil, false, nil) — the caller treats that as an empty live set, not an error.
func (s *gcsSegmentSource) readManifest(ctx context.Context) ([]manifestReadDigest, bool, error) {
	body, err := json.Marshal(manifestReadRequest{GraphType: s.graphType, Name: s.name, Format: s.format})
	if err != nil {
		return nil, false, fmt.Errorf("segmentdist: marshal manifest/read: %w", err)
	}
	raw, err := s.transport.SegmentControlJSON(ctx, "manifest/read", body)
	if err != nil {
		return nil, false, fmt.Errorf("segmentdist: manifest/read: %w", err)
	}
	var resp manifestReadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, false, fmt.Errorf("segmentdist: decode manifest/read response: %w", err)
	}
	if !resp.Found {
		return nil, false, nil
	}
	return resp.Digests, true, nil
}

// List returns one SegmentMeta per digest in the published GCS manifest. sinceGen is
// IGNORED: the manifest is the full live set (content-hash id-set), not a generation
// delta — this realizes the ticket's "manifest set-diff replaces the importedGen
// List-delta". Every meta carries Generation 0 and Import is idempotent by id, so the
// existing loadFromServer degenerates to always-list-the-full-set + dedup with no
// edit. A never-published manifest ({found:false}) returns an empty slice + nil.
func (s *gcsSegmentSource) List(ctx context.Context, _ uint64) ([]searchengine.SegmentMeta, error) {
	digests, found, err := s.readManifest(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		return []searchengine.SegmentMeta{}, nil
	}
	metas := make([]searchengine.SegmentMeta, 0, len(digests))
	for _, d := range digests {
		metas = append(metas, searchengine.SegmentMeta{
			ID:         d.ContentHash,
			Format:     s.format,
			Generation: 0,
			DocCount:   d.DocCount,
			DeadCount:  0,
		})
	}
	return metas, nil
}

// Fetch resolves the requested ids to their agent-minted object paths via one
// manifest/read (the fetch endpoint keys on object_path, and the client cannot mint
// the account-scoped key itself — the agent holds the accountID), issues one
// fetch-batch, then GETs + push-decrypts each returned object in a bounded parallel
// pool. A per-element ok:false (e.g. not_found from the grace-race GC) is skipped —
// a short-but-OK Fetch is tolerated by the L2-first load path. Fetch is off the hot
// path (load() is L2-first), so the extra manifest/read per call is cold-path only.
func (s *gcsSegmentSource) Fetch(ctx context.Context, ids []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	digests, found, err := s.readManifest(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	// Build id -> object_path from the manifest, then filter to the requested ids
	// (preserving the requested order for deterministic batching).
	pathByID := make(map[string]string, len(digests))
	for _, d := range digests {
		pathByID[d.ContentHash] = d.ObjectPath
	}
	wanted := make([]segmentFetchChunk, 0, len(ids))
	idByPath := make(map[string]searchengine.SegmentID, len(ids))
	for _, id := range ids {
		if p, ok := pathByID[id]; ok {
			wanted = append(wanted, segmentFetchChunk{ObjectPath: p})
			idByPath[p] = id
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}

	blobs := make([]searchengine.SegmentBlob, 0, len(wanted))
	var mu sync.Mutex
	for start := 0; start < len(wanted); start += segmentMaxBatchChunks {
		end := min(start+segmentMaxBatchChunks, len(wanted))
		body, err := json.Marshal(segmentFetchBatchRequest{Chunks: wanted[start:end]})
		if err != nil {
			return nil, fmt.Errorf("segmentdist: marshal fetch-batch: %w", err)
		}
		raw, err := s.transport.SegmentControlJSON(ctx, "fetch-batch", body)
		if err != nil {
			return nil, fmt.Errorf("segmentdist: fetch-batch: %w", err)
		}
		var resp segmentFetchBatchResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("segmentdist: decode fetch-batch response: %w", err)
		}

		var wg sync.WaitGroup
		pool := make(chan struct{}, shipConcurrency())
		for _, res := range resp.Results {
			if !res.OK {
				continue // per-element miss (grace-race GC / decrypt) — tolerated, skipped.
			}
			id, ok := idByPath[res.ObjectPath]
			if !ok {
				continue
			}
			wg.Go(func() {
				pool <- struct{}{}
				defer func() { <-pool }()
				blob, ok := s.fetchAndOpen(ctx, res, id)
				if !ok {
					return
				}
				mu.Lock()
				blobs = append(blobs, blob)
				mu.Unlock()
			})
		}
		wg.Wait()
	}
	return blobs, nil
}

// fetchAndOpen resolves one OK fetch-batch element to a SegmentBlob: base64-decode
// the agent-returned DEK, GET the sealed object, and push-decrypt it under the
// object-path-bound AAD. A per-element failure (bad DEK, GET error, decrypt error)
// is logged and reported via ok=false — the caller skips it (a short-but-OK Fetch is
// tolerated by the L2-first load path).
func (s *gcsSegmentSource) fetchAndOpen(ctx context.Context, res segmentFetchElementResult, id searchengine.SegmentID) (searchengine.SegmentBlob, bool) {
	dek, err := base64.StdEncoding.DecodeString(res.DEK)
	if err != nil {
		s.logger.Warn("segment fetch: decode DEK failed — skipping blob", "object_path", res.ObjectPath, "error", err)
		return searchengine.SegmentBlob{}, false
	}
	sealed, err := syncgcs.GetObject(ctx, res.DownloadURL)
	if err != nil {
		s.logger.Warn("segment fetch: GET object failed — skipping blob", "object_path", res.ObjectPath, "error", err)
		return searchengine.SegmentBlob{}, false
	}
	plain, err := syncgcs.OpenPushObject(sealed, dek, res.ObjectPath)
	if err != nil {
		s.logger.Warn("segment fetch: push-decrypt failed — skipping blob", "object_path", res.ObjectPath, "error", err)
		return searchengine.SegmentBlob{}, false
	}
	return searchengine.SegmentBlob{ID: id, Format: s.format, Bytes: plain}, true
}

// Prune is a no-op on the GCS path (0, nil): the agent owns orphan GC inline after
// each manifest publish (sweepSegmentOrphans), so the client never deletes GCS
// objects.
func (s *gcsSegmentSource) Prune(_ []searchengine.SegmentID) (int, error) {
	return 0, nil
}

// manifestIncompleteError is the typed sentinel PublishManifest returns when the
// agent HEAD-verify reports one or more digests genuinely absent (the 409 case). It
// carries the missing content hashes for the log line, and the source-aware publish
// gate (publishResident) treats it as a logged SKIP — the prior manifest stays
// intact — rather than a hard failure.
type manifestIncompleteError struct {
	Missing []string
}

func (e *manifestIncompleteError) Error() string {
	return fmt.Sprintf("segmentdist: manifest publish incomplete — %d blob(s) missing: %v", len(e.Missing), e.Missing)
}

// PublishManifest publishes this writer's full live digest-set for one format as the
// GCS manifest: the agent HEAD-verifies every referenced blob is present and
// CAS-writes the manifest (with each digest's doc_count annotation, read back by the
// coverage reads). On {ok:true} it returns (0, nil) — the agent inline-GCs orphans
// and reports no drop count (the client's reconcile is driven by the published
// live-set diff in publishResident, not this return). On the agent 409 {ok:false,
// missing} (a genuine-absence completeness failure) it returns a
// *manifestIncompleteError so the source-aware gate treats it as a skip; the 409
// surfaces as a transport *auth.SyncHTTPError, whose body carries the missing hashes.
func (s *gcsSegmentSource) PublishManifest(format string, digests []segmentDigest) (int, error) {
	wire := make([]manifestDigest, len(digests))
	for i, d := range digests {
		wire[i] = manifestDigest{ContentHash: d.ID, DocCount: d.DocCount}
	}
	body, err := json.Marshal(manifestPublishRequest{
		GraphType: s.graphType,
		Name:      s.name,
		Format:    format,
		Digests:   wire,
	})
	if err != nil {
		return 0, fmt.Errorf("segmentdist: marshal manifest/publish: %w", err)
	}
	if _, err := s.transport.SegmentControlJSON(context.Background(), "manifest/publish", body); err != nil {
		if incomplete := asManifestIncomplete(err); incomplete != nil {
			return 0, incomplete
		}
		return 0, fmt.Errorf("segmentdist: manifest/publish: %w", err)
	}
	return 0, nil
}

// verifiesCompletenessServerSide is true for the GCS source: the agent
// manifest/publish HEAD-verifies every digest and 409s on any missing blob, so the
// agent IS the completeness authority. The client's liveSetSubsetOfList0 check is
// both redundant and WRONG here — on the GCS path List(0) is the published manifest,
// so a resident set that includes newly-shipped-but-not-yet-published blobs is never
// a subset and would deadlock the first/every add-publish.
func (s *gcsSegmentSource) verifiesCompletenessServerSide() bool { return true }

// asManifestIncomplete classifies a transport error: if it is a 409
// *auth.SyncHTTPError whose body decodes to {ok:false, missing:[...]}, it returns a
// *manifestIncompleteError carrying the missing hashes; otherwise nil (a genuine
// transport/other error the caller surfaces verbatim).
func asManifestIncomplete(err error) *manifestIncompleteError {
	he, ok := errors.AsType[*auth.SyncHTTPError](err)
	if !ok || he.StatusCode != http.StatusConflict {
		return nil
	}
	var resp manifestPublishResponse
	if uErr := json.Unmarshal([]byte(he.Body), &resp); uErr == nil {
		return &manifestIncompleteError{Missing: resp.Missing}
	}
	return nil
}
