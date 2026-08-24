// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// WriteResult mints a per-collection epoch, byte-packs the nodes and the edges
// into SEPARATE CollectChunk requests, sends each chunk, then a single Finalize
// with the same epoch. Nodes AND edges both pack at DefaultBatchBytes (4 MiB) —
// nodes carry NO edges, edge chunks carry nil Nodes — so no single request body
// exceeds the bite-sized 4 MiB frame. Edges previously packed at
// kgwire.MaxCloudRequestBytes (64 MiB), but the fronting proxy on the cloud
// ingest path rejects bodies FAR below 64 MiB: a single ~14 MiB edge chunk
// (≈110k ID-addressed CALLS edges from a large repo) drew a raw HTTP 400 from the
// proxy while the 4 MiB node chunks all passed. An intermediary only sees body
// size, so edges now share the node cap.
// Spreading edges across multiple chunks is SAFE: collector edges are
// ID-addressed (FromIdx/ToIdx == -1, FromID/ToID set — see kgwire.BatchEdge
// build sites), so they resolve regardless of which chunk a referenced node
// arrived in, and the server dedups on the FOUR-PART edge identity
// (From, To, Type, Evidence) across chunks and the resident graph bundles — the
// group key is part of that identity, so two memberships of one triple both
// land. Per-chunk retry lives in sink_retry.go, covering both
// transport failures and the ambiguous-intermediary class on separate budgets,
// and is content-idempotent for BOTH nodes and edges: a node
// re-lands identically through the server's carry-forward upsert + epoch GC, and
// an edge re-lands at most once because the server filters duplicates on that
// same four-part identity. A retry of any edge chunk therefore does not double
// its edges.
// collectChunkRequests assembles the ordered CollectChunk request list: every
// node-chunk (no edges) first, then every edge-chunk (nil nodes), all under the
// shared epoch + graph identity.
//
// ALWAYS AT LEAST ONE REQUEST, even for an empty node AND edge set: a
// deletion-only re-collect must still reach the server so Finalize has an epoch
// the server has seen.
//
// Split out of WriteResult rather than inlined so that function stays under the
// package's length ceiling; the assembly has no dependency on anything else in
// WriteResult's frame beyond its arguments.
func collectChunkRequests(
	epoch uint64, result *collectorwire.CollectResult,
	nodeChunks [][]*knowledgev1.Node, edgeChunks [][]*knowledgev1.BatchEdge, mode diffMode,
	hashes chunkHashFields,
) []*knowledgev1.CollectChunkRequest {
	// ONE RESOLVED MODE PER COLLECT, stamped identically on every chunk: the mode
	// is a property of the collect, not of the chunk, so it is computed once by the
	// caller and never re-derived in here. Only diffModeOn sends true — shadow
	// uploads the full set, so its chunks are as resident as a full collect's and
	// must keep the valve loud.
	diff := mode == diffModeOn
	build := func(
		nodes []*knowledgev1.Node, edges []*knowledgev1.BatchEdge,
		nodeHashes [][]byte, files []*knowledgev1.ManifestEntry,
	) *knowledgev1.CollectChunkRequest {
		return &knowledgev1.CollectChunkRequest{
			Epoch:                  epoch,
			GraphType:              string(result.GraphType),
			GraphName:              result.GraphName,
			CurrentBranch:          result.CurrentBranch,
			Promote:                result.Promote,
			SyncCommit:             result.SyncCommit,
			SyncTime:               result.SyncTime,
			Nodes:                  nodes,
			Edges:                  edges,
			DiffMode:               diff,
			ManifestId:             hashes.manifestID,
			NodeContributionHashes: nodeHashes,
			FileContributions:      files,
		}
	}
	var reqs []*knowledgev1.CollectChunkRequest
	// THE OFFSET IS CARRIED, NEVER RE-DERIVED. BatchNodes packs whole file groups
	// under a byte budget, so the chunk boundaries are irregular; walking the offset
	// alongside the loop is the only way to keep each chunk's hashes aligned with
	// its own nodes, and it holds only because the caller permuted the digests into
	// the same file-grouped order the chunker packs in. A slip here
	// is refused server-side with InvalidArgument naming both lengths, so it fails
	// loudly rather than declining files against another chunk's digests.
	offset := 0
	for _, nc := range nodeChunks {
		reqs = append(reqs, build(nc, nil,
			hashes.nodeHashesFor(offset, len(nc)), hashes.entriesForNodes(nc)))
		offset += len(nc)
	}
	for _, ec := range edgeChunks {
		reqs = append(reqs, build(nil, ec, nil, hashes.entriesForEdges(ec)))
	}
	if len(reqs) == 0 {
		reqs = append(reqs, build(nil, nil, nil, nil))
	}
	return reqs
}

// planDiffUpload fetches the manifest, refuses the conditions a full collect
// cannot repair, and returns the collect's resolved mode, its upload plan and the
// manifest identity the Finalize echoes.
//
// FOUR CONDITIONS ABORT RATHER THAN DEGRADE, because a rebuild fixes none of
// them: the next collect meets the same condition and pays O(repo) again,
// forever. Each abort precedes THE FIRST CHUNK, so no partial upload exists to
// reconcile and no server state has been touched. (The epoch is minted before
// this runs, which is harmless — minting is a wall-clock read and a CAS on a
// client-side counter, so an unused epoch is a discarded number, not stranded
// state.)
//
// Split out of WriteResult so that function stays inside the package's length and
// nesting ceilings; the per-file hashes are computed by the caller because their
// ORDER relative to the sanitize pass is a property of WriteResult's own frame.
func (s *UploadSink) planDiffUpload(
	ctx context.Context, result *collectorwire.CollectResult,
	mode diffMode, lever diffLever, perFileHashes map[string][32]byte,
) (diffMode, uploadDecision, string, []baselineCommit, error) {
	// The fingerprint check runs BEFORE the fetch: it names OUR OWN producer
	// regressing, so it must cost no round trip.
	if result.DiscoveryFingerprint == "" {
		return "", uploadDecision{}, "", nil, fmt.Errorf("remote sink: empty discovery fingerprint on a code collect: " +
			"the discovery producer did not stamp CollectResult.DiscoveryFingerprint")
	}
	manifest, mErr := s.fetchManifest(ctx, result)
	// The SAME failure class WriteResult's picker error already hard-errors on —
	// fetchManifest resolves through that same picker — so degrading here while
	// erroring there gave one class of failure two treatments.
	if mErr != nil {
		return "", uploadDecision{}, "", nil, fmt.Errorf("remote sink: collect manifest: %w", mErr)
	}
	// The identity is minted and persisted by the RENDER, never by an upload, so no
	// collect this client runs can supply one that is missing.
	if manifest.GetManifestId() == "" {
		return "", uploadDecision{}, "", nil, fmt.Errorf(
			"remote sink: server returned no manifest identity (missing_manifest_id): the render did not mint one")
	}
	// A manifest that disagrees with its own contract came from the server's render
	// logic, and the next render comes from the same logic — so this re-fires
	// forever rather than converging.
	if !manifestSelfConsistent(manifest) {
		return "", uploadDecision{}, "", nil, fmt.Errorf(
			"remote sink: served manifest violates its own contract: %s", manifestDefect(manifest))
	}
	// A discovery-store failure leaves here as an error rather than as a silently
	// degraded lane: a store that cannot be read or written keeps no baseline, so
	// the lane it used to take would fire on every collect forever.
	var outcome collectDiffOutcome
	resolvedMode, decision, dErr := s.applyCollectDiff(result, mode, lever, perFileHashes, manifest, &outcome)
	if dErr != nil {
		return "", uploadDecision{}, "", nil, dErr
	}
	// ONE SOURCE FOR THE IDENTITY. Both consumers — the chunks' hash fields and the
	// FinalizeRequest — read the value returned here, so blanking it once disables
	// the server's decline on both rather than leaving the two to be blanked
	// independently and disagree.
	manifestID := manifest.GetManifestId()
	if outcome.suppressManifestEcho {
		manifestID = ""
	}
	return resolvedMode, decision, manifestID, outcome.baselines, nil
}

// uploadChunks sends every CollectChunk request in order and reports the first
// failure, with the timing and socket-meter instrumentation the collect's
// foreground latency is diagnosed from.
//
// Split out of WriteResult so that function stays inside the package's length
// ceiling. It keeps the whole loop — including the stall flag — rather than just
// the send, because the measurement and the send are one story: the meter delta
// is read BEFORE the error branch on purpose, so a chunk that exhausts its retry
// budget is never the one event with no reading.
func (s *UploadSink) uploadChunks(
	ctx context.Context, collectorName string, result *collectorwire.CollectResult,
	reqs []*knowledgev1.CollectChunkRequest, epoch uint64, nodeChunks, edgeChunks int,
) error {
	// Timing instrumentation: the CollectChunk upload loop + Finalize are the
	// foreground of the collect tool call (the client blocks here until the server
	// acks each RPC). Logged at debug so a slow collect can be traced to the exact
	// chunk or the Finalize, not an unattributed silent gap.
	uploadStart := time.Now()
	slog.Debug("remote sink: upload start", "collector", collectorName,
		"graph_type", result.GraphType, "graph", result.GraphName, "branch", result.CurrentBranch,
		"epoch", epoch, "chunks", len(reqs), "node_chunks", nodeChunks, "edge_chunks", edgeChunks,
		"nodes", len(result.Nodes), "edges", len(result.Edges))
	for i, req := range reqs {
		chunkStart := time.Now()
		reqBytes := proto.Size(req)
		meterBefore := graphclient.SocketWriteSnapshot()
		err := s.collectChunkWithRetry(ctx, req)
		d := meterDelta(meterBefore)
		elapsed := time.Since(chunkStart)
		if graphclient.ShouldFlagClientSideStall(elapsed, d.InWrite) {
			logClientSideStall(i+1, len(reqs), reqBytes, elapsed, d.InWrite, d.Writes)
		}
		if err != nil {
			slog.Info("remote sink: chunk failed", "i", i+1, "of", len(reqs), "bytes", reqBytes,
				"dur", elapsed.Round(time.Millisecond), "socket_writes", d.Writes,
				"socket_bytes", d.Bytes, "in_write_ms", millis(d.InWrite))
			return fmt.Errorf("remote sink: CollectChunk %d/%d (%d bytes): %w", i+1, len(reqs), reqBytes, err)
		}
		slog.Debug("remote sink: chunk sent", "i", i+1, "of", len(reqs),
			"nodes", len(req.Nodes), "edges", len(req.Edges), "bytes", reqBytes,
			"dur", elapsed.Round(time.Millisecond),
			"socket_writes", d.Writes, "socket_bytes", d.Bytes, "in_write_ms", millis(d.InWrite))
	}
	slog.Debug("remote sink: all chunks uploaded", "graph", result.GraphName, "branch", result.CurrentBranch,
		"epoch", epoch, "chunks", len(reqs), "dur", time.Since(uploadStart).Round(time.Millisecond))
	return nil
}

func (s *UploadSink) WriteResult(ctx context.Context, collectorName string, result *collectorwire.CollectResult) error {
	// THE LEVER RESOLVES FIRST, ahead of everything else this function does. A
	// value that is present and meaningless ERRORS the collect HERE, where the
	// refusal costs nothing: no O(repo) contribution-hash pass has run and no
	// CollectManifest RPC has been sent. That RPC is NOT inert — the server mints
	// and persists a fresh manifest identity on every render — so a typo resolved
	// any later would rotate this graph's manifest id and, through the identity
	// echo, invalidate a CONCURRENT collect's deletions. A typo must not have side
	// effects on somebody else's collect. The resolved pair is carried down to
	// applyCollectDiff as parameters rather than re-read there.
	mode, lever, err := collectDiffMode()
	if err != nil {
		return err
	}
	client, err := s.picker(ctx)
	if err != nil {
		return fmt.Errorf("remote sink: resolve ingest client: %w", err)
	}
	epoch := s.mintEpoch()
	// Sanitize node text BEFORE the proto marshal: the inline-Node wire marshals
	// typed proto Node messages, and proto3 string fields reject invalid UTF-8 at
	// marshal time. (The server re-sanitizes on write; double-sanitize is safe.)
	for _, n := range result.Nodes {
		sanitizeNodeText(n)
	}
	// COLLAPSE THE EMITTED EDGE MULTISET ONTO THE ROW SET THE SERVER CAN HOLD,
	// once, here, and for the same reason the sanitize loop runs before the
	// hashes: the client must digest and ship the bytes the store actually keeps.
	// The emitters produce several rows per stored identity — the same (spec,
	// impl) method pair once per satisfying interface, and one CALLS row per
	// callee SPELLING where two spellings bind to one target — and the store
	// resolves those to ONE row, last-op-wins. A client hashing the multiset can
	// therefore never agree with a server holding the set, and the file
	// re-uploads on every collect forever.
	//
	// ONE REASSIGNMENT SERVES ALL FOUR READERS BELOW, which is why there is no
	// second merge at the upload seam: RowContributionHashes, the per-file
	// FileContributionHashes inside the diff gate, the fileless signature via
	// planDiffUpload, and BatchEdgesToProto all read result.Edges after this line.
	// A second application would DOUBLE a summed call count, so exactly one call
	// is gated.
	//
	// IT MUST PRECEDE RowContributionHashes. Those digests are index-aligned with
	// result.Edges and are stamped onto the carriers by index on the very next
	// lines; merging afterwards would stamp each survivor with a dropped
	// neighbour's digest, computed over the pre-sum weight, and nothing would fail.
	result.Edges = contribhash.MergeEdgesByIdentity(result.Edges)
	// PER-ROW hashes, for EVERY graph family and therefore OUTSIDE the
	// diff-eligibility gate below. contribution_hash is a column on every graph's
	// node and edge tables; a web or pdf collect that sent none would leave those
	// columns NULL, which under client-supplied values is a stranded file rather
	// than a free server-side default. The per-FILE map stays inside the gate.
	//
	// AFTER THE SANITIZE LOOP, for the same reason the file hashes are: the loop
	// rewrites ten of the fourteen hashed node fields in place, and the server
	// stores the SANITIZED bytes. Hashing first would digest bytes nobody stores.
	//
	// The edge digests are stamped onto the carriers so they survive
	// BatchEdgesToProto and the byte-split chunker; the node digests stay a
	// parallel array kept index-aligned with result.Nodes, including across the
	// diff filter below AND across the file grouping that follows it — the two
	// arrays are narrowed by one predicate and permuted by one order, never
	// separately.
	nodeHashes, edgeHashes := contribhash.RowContributionHashes(result.Nodes, result.Edges)
	for i := range result.Edges {
		result.Edges[i].ContributionHash = edgeHashes[i]
	}
	// INCREMENTAL COLLECT. The order here is load-bearing and gated:
	// the GRAPH-FAMILY gate runs BEFORE both the hash and the fetch, because a
	// fetch-first shape would leave every non-code collect rotating the manifest
	// identity of a graph it has no business touching (the server mints and
	// persists a fresh id on every render). The hash runs AFTER the sanitize loop
	// above, because sanitizeNodeText rewrites ten of the fourteen hashed fields
	// in place and the server stores the SANITIZED bytes.
	// A graph outside the diff-eligible family never resolves to diff mode at all,
	// so the flag stays false whatever the lever asked for — the lever is resolved
	// above for every collect, but only an eligible graph consults the result.
	//
	// benchForceFullNoDiff is tested BEFORE the family gate, so the bench's
	// non-diff arm never fetches a manifest and never computes a diff — it is the
	// pre-incremental upload, not a diff that happens to select everything. Only
	// the collectbench-tagged constructor can set it; production leaves it false
	// and this conjunct short-circuits to the ordinary path.
	resolvedMode := diffModeOff
	decision := uploadDecision{uploadAll: true}
	var manifestID string
	// perFileHashes escapes the gate as a VALUE, not as a second call: the chunk
	// builder sends these same hashes so the server can compare per file, and
	// recomputing them there would pay the O(repo) pass twice. It stays nil for a
	// non-eligible family, which is what makes those collects decline nothing.
	var perFileHashes map[string][32]byte
	// pendingBaselines is what this collect will owe the discovery store IF it
	// succeeds. It stays empty for a non-eligible family, which records nothing —
	// those collects consult no baseline either.
	var pendingBaselines []baselineCommit
	if !s.benchForceFullNoDiff && diffEligibleGraph(result.GraphType) {
		perFileHashes = contribhash.FileContributionHashes(result.Nodes, result.Edges)
		var dErr error
		resolvedMode, decision, manifestID, pendingBaselines, dErr = s.planDiffUpload(
			ctx, result, mode, lever, perFileHashes)
		if dErr != nil {
			return dErr
		}
	}
	// THE DIFF GOVERNS THE UPLOAD, AND THE FILE GROUPING GOVERNS THE ORDER. Only the
	// changed files' nodes and edges go on the wire, plus the FILELESS set, which
	// always uploads: a node belonging to no file is outside the manifest entirely,
	// so it is never diffed and its set has to be complete on every collect. Under
	// uploadAll — a non-eligible graph family, or any degraded lane — the narrowing
	// is a no-op and the full set ships. The surviving nodes are then reordered into
	// the file-grouped order BatchNodes packs in, so no file's nodes span a chunk:
	// the server's per-chunk node reclaim deletes an uploaded file's uncarried live
	// rows, which is only safe against a chunk holding that file's complete set.
	//
	// BOTH STEPS CARRY THE PER-ROW DIGESTS THROUGH WITH THE NODES, which is why they
	// share one helper: the chunker slices that array by POSITION, so narrowing or
	// permuting the nodes alone would send each chunk another node's digests, and
	// the server's length check cannot see either mistake.
	nodeHashes, err = narrowAndGroupRows(result, nodeHashes, decision)
	if err != nil {
		return err
	}
	nodeChunks := BatchNodes(result.Nodes, DefaultBatchBytes)
	protoEdges := kgwire.BatchEdgesToProto(result.Edges)
	// Edges ride their OWN chunks at the SAME 4 MiB cap as nodes, NEVER on a
	// node-chunk: both node- and edge-chunks stay ≤4 MiB, so every emitted
	// CollectChunk request body stays bite-sized regardless of how large the edge
	// tail grows. The server dedups on the four-part edge identity
	// (From, To, Type, Evidence) across chunks, so any N edge chunks land the full
	// edge set exactly once.
	edgeChunks := BatchEdgesProto(protoEdges, DefaultBatchBytes)

	// The id → owning-file map is built from the UPLOADED node set, after the diff
	// filter, because an edge chunk names its files through its FROM node and only
	// the uploaded nodes can be named.
	fileByNodeID := make(map[string]string, len(result.Nodes))
	for _, n := range result.Nodes {
		if p := n.GetFilePath(); p != "" {
			fileByNodeID[n.GetId()] = p
		}
	}
	reqs := collectChunkRequests(epoch, result, nodeChunks, edgeChunks, resolvedMode, chunkHashFields{
		manifestID:    manifestID,
		nodeHashes:    nodeHashes,
		perFileHashes: perFileHashes,
		fileByNodeID:  fileByNodeID,
	})
	if err := s.uploadChunks(ctx, collectorName, result, reqs, epoch, len(nodeChunks), len(edgeChunks)); err != nil {
		return err
	}

	// The SAME resolved mode the chunks carried. It is what tells the server this
	// collect uploaded only part of the graph, so a diff collect naming ZERO
	// deletions still reaches the deletion arms with an empty-but-non-nil set
	// rather than re-arming the legacy inference against a partial upload.
	finReq := connect.NewRequest(&knowledgev1.FinalizeRequest{
		Epoch:         epoch,
		GraphType:     string(result.GraphType),
		GraphName:     result.GraphName,
		CurrentBranch: result.CurrentBranch,
		Promote:       result.Promote,
		DiffMode:      resolvedMode == diffModeOn,
		// The deletion carrier and both guard fields ride the SAME Finalize as the
		// chunks' epoch, so there is no arrangement where a deletion phase runs
		// against a collection whose chunks were skipped.
		DeletedFiles: decision.deletions,
		ManifestId:   manifestID,
		// COMPUTED, NEVER HARDCODED. A literal true satisfies every field-presence
		// gate while disarming guard 2 — the only thing standing between a file
		// that failed to READ and being NAMED as a deletion. Its source is the code
		// collector's ChunkReport (pop.ChunkReport.Dropped() == 0), the same report
		// whose non-empty state FAILS the collect client-side, so the wire
		// assertion and the client's own refusal cannot disagree.
		WalkComplete: result.WalkComplete,
		// Rides Finalize ONLY: it decides a server-side guard, not anything about
		// what the chunks carry.
		DeletionRatioOverride: collectDeletionRatioOverride(),
	})
	finStart := time.Now()
	// Finalize does the epoch GC and promotion work, a different load shape from
	// the chunk uploads above, so it carries its own term.
	finResp, err := finalizeWithRetry(graphclient.WithOperation(ctx, graphclient.OpCollectFinalize), client, finReq)
	if err != nil {
		return fmt.Errorf("remote sink: Finalize: %w", err)
	}
	slog.Debug("remote sink: finalize accepted", "graph", result.GraphName, "branch", result.CurrentBranch,
		"epoch", epoch, "dur", time.Since(finStart).Round(time.Millisecond))
	tailState, tailErr := awaitFinalizeTail(ctx, client, finResp.Msg.GetFinalizeId(), result.GraphName, finStart)
	if tailErr != nil {
		return tailErr
	}
	return commitCollectBaselines(tailState, pendingBaselines)
}
