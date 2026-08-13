// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// IngestClientPicker resolves the IngestService client to use for one call.
// It is invoked PER CALL (WriteResult, collectChunkWithRetry, FetchCloudSubgraph)
// so a mid-session `knowledge login` flip re-routes the next chunk to the cloud
// backend without a process restart. Router.IngestClient(ctx) satisfies this
// shape (router.go); NewUploadSink wraps a fixed client into a constant picker.
type IngestClientPicker func(ctx context.Context) (knowledgev1connect.IngestServiceClient, error)

// UploadSink implements collector.Sink by driving the unary IngestService
// CollectChunk + Finalize flow. Used by cmd/knowledge (the MCP stdio client) so
// collection runs client-side while indexing runs server-side. Stateless on the
// wire: each chunk's nodes ride INLINE, so any server replica can land any
// chunk (no per-process arena).
//
// The IngestService client is resolved PER CALL via picker so login-aware
// routing (local vs cloud) honors a mid-session login flip; the sink never
// caches a resolved client across calls.
type UploadSink struct {
	picker IngestClientPicker
	// epoch is the per-collection identifier minted client-side by mintEpoch.
	// It holds the LAST minted value so the mint stays monotonic within this
	// process; every chunk of one collection AND its Finalize share one value.
	// Zero-value valid (the first mint reads the wall clock, never 0).
	//
	// It is NOT a counter. A plain Add(1) from zero was authoritative only under
	// the assumption that one process is the sole writer of a graph — which is
	// false: every stdio client is its own process with its own sink, they all
	// write the same shared graphs, and the value resets on restart. Distinct
	// collections then REUSE a value, and the collect GC keys on it, so reuse
	// silently corrupts: the base sweep tombstones "collect_epoch <> $1", so a
	// reused value leaves another collection's nodes alive forever, and a reused
	// value merges a crashed run's rows into this run's presence set, hiding
	// deletions the GC exists to make. Both need no concurrency — only a
	// restarted client landing on a value it used before.
	epoch atomic.Uint64
	// epochSalt is this sink's slot in the low bits of every epoch it mints.
	// 0 means "not yet drawn" — see salt(). Zero-value valid.
	epochSalt atomic.Uint64
}

// epochSaltBits is how many low bits of the epoch carry the per-process salt
// instead of the clock. It trades timestamp precision for cross-process
// separation: 20 bits leaves ~1ms of dating resolution (irrelevant against the
// server's hours-long leak-reclaim window) and gives 2^20 process slots.
const epochSaltBits = 20

// newEpochSalt draws a salt in [1, 2^epochSaltBits). Never 0 — the sink treats a
// zero salt as "not yet drawn", and a fixed salt would put every minter in one
// slot and reinstate the collision the salt exists to prevent.
func newEpochSalt() uint64 {
	const mask = 1<<epochSaltBits - 1
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Degrade to the clock rather than to a constant.
		if v := uint64(time.Now().UnixNano()) & mask; v != 0 {
			return v
		}
		return 1
	}
	if v := binary.LittleEndian.Uint64(b[:]) & mask; v != 0 {
		return v
	}
	return 1
}

// salt returns this sink's epoch salt, drawing it on first use so the zero-value
// UploadSink stays valid.
//
// The salt is what disambiguates one minter from another at the same instant. Two
// minters reading time.Now() inside the same tick compute the SAME nanoseconds and
// share no atomic to break the tie, so a clock-only mint would reissue one value
// to two collections — the exact reuse this mechanism exists to prevent. A
// monotonic CAS cannot help: it is per-sink state, and each sink's CAS succeeds
// independently of the other's.
//
// Scoped per SINK rather than per process deliberately. Production constructs one
// sink per client process, so the two are equivalent there; making it per-sink
// costs nothing and additionally separates any two sinks that ever coexist.
func (s *UploadSink) salt() uint64 {
	if v := s.epochSalt.Load(); v != 0 {
		return v
	}
	if s.epochSalt.CompareAndSwap(0, newEpochSalt()) {
		return s.epochSalt.Load()
	}
	return s.epochSalt.Load() // lost the race; the winner's salt is authoritative
}

// mintEpoch returns the identifier for one collection. It is:
//
//   - UNIQUE across processes and restarts — the high bits come from the wall
//     clock and the low bits from this process's salt, so two clients collide
//     only by drawing the same 1-in-2^20 salt AND minting within the same ~1ms.
//     Contrast the old bare Add(1) from zero, where two clients collided on
//     EVERY collect with certainty. Uniqueness is the property the collect GC
//     depends on; ordering is not — every consumer compares for equality.
//   - MONOTONIC within this process — the CAS floor advances by a whole salt
//     slot, so a coarse or backwards clock cannot repeat or reverse a value we
//     already minted, and the advance preserves the salt in the low bits.
//   - SELF-DATING to ~1ms — the value IS approximately its own creation time,
//     which is what lets the server reclaim presence rows leaked by collections
//     that died before their cleanup ran, using an age predicate on the epoch
//     itself. Do not replace this with a pure counter or a pure random value
//     without giving __collect_seen its own timestamp column; the reclaim in
//     runOverlayDeletionGC depends on this property.
//
// This is a PROBABILISTIC uniqueness guarantee. The only way to make it absolute
// is to allocate the epoch server-side, once per collection, which needs a
// collection delimiter the wire does not currently have.
//
// Nanoseconds fit the signed BIGINT the epoch persists into (~1.8e18 today vs a
// 9.2e18 ceiling, good past 2262) — note every SQL call site casts int64, so a
// value above MaxInt64 would persist NEGATIVE.
func (s *UploadSink) mintEpoch() uint64 {
	const saltMask = 1<<epochSaltBits - 1
	now := uint64(time.Now().UnixNano())&^saltMask | s.salt()
	for {
		prev := s.epoch.Load()
		next := now
		if next <= prev {
			next = prev + 1<<epochSaltBits // whole slot, so the salt survives
		}
		if s.epoch.CompareAndSwap(prev, next) {
			return next
		}
	}
}

// NewUploadSink constructs an UploadSink wired to a FIXED IngestService client.
// Retained as the constant-picker convenience for callers (and tests) that
// route to a single backend; it wraps client into a picker that always returns
// it. Login-aware callers use NewUploadSinkFunc instead.
func NewUploadSink(client knowledgev1connect.IngestServiceClient) *UploadSink {
	return &UploadSink{picker: func(context.Context) (knowledgev1connect.IngestServiceClient, error) {
		return client, nil
	}}
}

// NewUploadSinkFunc constructs an UploadSink whose IngestService client is
// resolved per call via picker — the login-aware path (Router.IngestClient).
func NewUploadSinkFunc(picker IngestClientPicker) *UploadSink {
	return &UploadSink{picker: picker}
}

// Compile-time assertion.
var _ collector.Sink = (*UploadSink)(nil)

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
// arrived in, and the server dedups (From,Type,To) tuples across chunks and the
// resident graph bundles. Per-chunk retry lives in sink_retry.go, covering both
// transport failures and the ambiguous-intermediary class on separate budgets,
// and is content-idempotent for BOTH nodes and edges: a node
// re-lands identically through the server's carry-forward upsert + epoch GC, and
// an edge re-lands at most once because the server filters duplicate (From,Type,
// To) tuples. A retry of any edge chunk therefore does not double its edges.
func (s *UploadSink) WriteResult(ctx context.Context, collectorName string, result *collectorwire.CollectResult) error {
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
	nodeChunks := BatchNodes(result.Nodes, DefaultBatchBytes)
	protoEdges := kgwire.BatchEdgesToProto(result.Edges)
	// Edges ride their OWN chunks at the SAME 4 MiB cap as nodes, NEVER on a
	// node-chunk: both node- and edge-chunks stay ≤4 MiB, so every emitted
	// CollectChunk request body stays bite-sized regardless of how large the edge
	// tail grows. The server dedups (From,Type,To) tuples across chunks, so any N
	// edge chunks land the full edge set exactly once.
	edgeChunks := BatchEdgesProto(protoEdges, DefaultBatchBytes)

	// build assembles a CollectChunkRequest carrying one node group and/or one
	// edge group under the shared epoch + graph identity.
	build := func(nodes []*knowledgev1.Node, edges []*knowledgev1.BatchEdge) *knowledgev1.CollectChunkRequest {
		return &knowledgev1.CollectChunkRequest{
			Epoch:         epoch,
			GraphType:     string(result.GraphType),
			GraphName:     result.GraphName,
			CurrentBranch: result.CurrentBranch,
			Promote:       result.Promote,
			SyncCommit:    result.SyncCommit,
			SyncTime:      result.SyncTime,
			Nodes:         nodes,
			Edges:         edges,
		}
	}

	// Assemble the ordered request list: every node-chunk (no edges) first, then
	// every edge-chunk (nil nodes).
	var reqs []*knowledgev1.CollectChunkRequest
	for _, nc := range nodeChunks {
		reqs = append(reqs, build(nc, nil))
	}
	for _, ec := range edgeChunks {
		reqs = append(reqs, build(nil, ec))
	}
	// Always send at least one CollectChunk so an empty-node + empty-edge
	// collection (deletion-only recollect) still reaches the server and Finalize
	// has an epoch the server has seen.
	if len(reqs) == 0 {
		reqs = append(reqs, build(nil, nil))
	}
	// Timing instrumentation: the CollectChunk upload loop + Finalize are the
	// foreground of the collect tool call (the client blocks here until the
	// server acks each RPC). Logged at debug so a slow collect can be traced to
	// the exact chunk or the Finalize, not an unattributed silent gap.
	uploadStart := time.Now()
	slog.Debug("remote sink: upload start", "collector", collectorName,
		"graph_type", result.GraphType, "graph", result.GraphName, "branch", result.CurrentBranch,
		"epoch", epoch, "chunks", len(reqs), "node_chunks", len(nodeChunks), "edge_chunks", len(edgeChunks),
		"nodes", len(result.Nodes), "edges", len(result.Edges))
	for i, req := range reqs {
		chunkStart := time.Now()
		reqBytes := proto.Size(req)
		meterBefore := graphclient.SocketWriteSnapshot()
		err := s.collectChunkWithRetry(ctx, req)
		// The meter delta is read BEFORE the error branch on purpose: a chunk
		// that exhausts its retry budget is exactly the event this reading
		// exists to explain, so the failure path must never be the one case
		// with no measurement.
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

	finReq := connect.NewRequest(&knowledgev1.FinalizeRequest{
		Epoch:         epoch,
		GraphType:     string(result.GraphType),
		GraphName:     result.GraphName,
		CurrentBranch: result.CurrentBranch,
		Promote:       result.Promote,
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
	return awaitFinalizeTail(ctx, client, finResp.Msg.GetFinalizeId(), result.GraphName, finStart)
}

// meterDelta returns how far the process-wide socket-write counters advanced
// since before. Only the difference is meaningful — the absolute counters carry
// whatever this process accumulated since start.
func meterDelta(before graphclient.SocketWriteStats) graphclient.SocketWriteStats {
	now := graphclient.SocketWriteSnapshot()
	return graphclient.SocketWriteStats{
		Writes:  now.Writes - before.Writes,
		Bytes:   now.Bytes - before.Bytes,
		InWrite: now.InWrite - before.InWrite,
	}
}

// millis renders a duration as fractional milliseconds, keeping sub-millisecond
// readings legible — the client-side-stall signature is precisely a reading of a
// few milliseconds against an elapsed measured in seconds, and truncating that
// to an integer would print the most interesting value as 0.
func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

// logClientSideStall emits the one loud line for a chunk carrying the
// client-side-stall signature. It is INFO, not WARN: nothing is broken when it
// fires — the collect may well have succeeded — and a WARN would train the
// operator to ignore the one instrument armed for a rare event.
//
// Extracted rather than written inline because the emission IS the deliverable:
// inline, a name-grep for the predicate passes even on a discarded call with no
// logging at all, and an instrument that never fires is indistinguishable from
// one that was never wired. As a helper it is directly drivable from a test.
// Keep it a formatter over the record below; it must not grow logic.
func logClientSideStall(i, of, bytes int, elapsed, inWrite time.Duration, writes int64) {
	slog.Info("remote sink: chunk was slow while almost no time was spent writing to the socket — "+
		"the bytes were held CLIENT-SIDE, not by the network path",
		"i", i, "of", of, "bytes", bytes,
		"dur", elapsed.Round(time.Millisecond),
		"in_write_ms", millis(inWrite),
		"socket_writes", writes,
		"next", "re-run the collect with GODEBUG=http2debug=2 to capture h2 frame detail on the next occurrence")
}

// finalizeTailPoll is the interval between FinalizeStatus polls, and
// finalizeTailWait the total budget before the collect stops waiting.
//
// The budget is deliberately LONGER than the edge timeout the detached tail
// exists to escape: the point of moving the tail off the response path was never
// to make the work faster, only to stop a slow tail from failing the REQUEST. A
// budget shorter than the tail's realistic duration would reintroduce exactly the
// impatience that was moved out of the transport.
const (
	finalizeTailPoll = 2 * time.Second
	finalizeTailWait = 10 * time.Minute
)

// awaitFinalizeTail polls FinalizeStatus until the server's detached finalize
// tail settles, so a collect reports what actually happened rather than assuming
// the acknowledgement was the whole story.
//
// NOTHING HERE FAILS THE COLLECT. By the time Finalize returned, the durable half
// — the epoch-tombstone sweep recording this collection's deletions — was already
// committed; the tail is follow-up over that committed state. A failed or
// unfinished tail means some staleness marking or tombstone pruning is owed, which
// the next collect redoes, so turning it into a collect failure would report a
// loss that did not happen. The outcome is logged at a severity matching what it
// means and the collect succeeds.
func awaitFinalizeTail(
	ctx context.Context, client knowledgev1connect.IngestServiceClient,
	finalizeID, graphName string, finStart time.Time,
) error {
	if finalizeID == "" {
		// A server too old to return an id. Nothing to poll; the acknowledgement is
		// all the completion signal that exists against it.
		slog.Debug("remote sink: finalize done (server returned no finalize id)", "graph", graphName)
		return nil
	}
	deadline := time.Now().Add(finalizeTailWait)
	for {
		resp, err := client.FinalizeStatus(graphclient.WithOperation(ctx, graphclient.OpCollectFinalize),
			connect.NewRequest(&knowledgev1.FinalizeStatusRequest{FinalizeId: finalizeID}))
		if err != nil {
			slog.Warn("remote sink: finalize status poll failed — collect stands, tail completion unconfirmed",
				"graph", graphName, "finalize_id", finalizeID, "error", err)
			return nil
		}
		switch resp.Msg.GetState() {
		case knowledgev1.FinalizeState_FINALIZE_STATE_DONE:
			slog.Debug("remote sink: finalize done", "graph", graphName,
				"dur", time.Since(finStart).Round(time.Millisecond))
			return nil
		case knowledgev1.FinalizeState_FINALIZE_STATE_FAILED:
			slog.Error("remote sink: finalize tail FAILED — the collection landed, but its follow-up "+
				"(container staleness marks, tombstone prune) did not complete",
				"graph", graphName, "finalize_id", finalizeID, "error", resp.Msg.GetError())
			return nil
		case knowledgev1.FinalizeState_FINALIZE_STATE_UNKNOWN:
			// Finalize ids are process-local, so a poll routed to a different replica
			// than served the Finalize legitimately lands here. Not an error, and not
			// worth retrying — the replica that knows will never be asked again.
			slog.Debug("remote sink: finalize tail status unknown to the serving replica — "+
				"completion unconfirmed", "graph", graphName, "finalize_id", finalizeID)
			return nil
		case knowledgev1.FinalizeState_FINALIZE_STATE_RUNNING,
			knowledgev1.FinalizeState_FINALIZE_STATE_UNSPECIFIED:
			// Keep waiting.
		}
		if time.Now().After(deadline) {
			slog.Warn("remote sink: finalize tail still running after the wait budget — collect stands, "+
				"tail completion unconfirmed",
				"graph", graphName, "finalize_id", finalizeID, "waited", finalizeTailWait)
			return nil
		}
		select {
		case <-ctx.Done():
			slog.Debug("remote sink: finalize tail wait cancelled — collect stands, completion unconfirmed",
				"graph", graphName, "finalize_id", finalizeID)
			return nil
		case <-time.After(finalizeTailPoll):
		}
	}
}

// edgesFromProto converts the typed proto Edge carrier into []knowledgev1.Edge —
// the remote-package decode for the FetchCloudSubgraph slice edges (the value
// shape cloudresolver.GraphSlice.Edges expects). Mirrors the engine package's
// EdgesFromProto (kept local so the collector/remote package does not depend on
// the engine decode package). Empty carrier → nil.
func edgesFromProto(in []*knowledgev1.Edge) []knowledgev1.Edge {
	if len(in) == 0 {
		return nil
	}
	out := make([]knowledgev1.Edge, len(in))
	for i, e := range in {
		out[i] = knowledgev1.Edge{
			FromId:        e.GetFromId(),
			ToId:          e.GetToId(),
			Type:          e.GetType(),
			Weight:        e.GetWeight(),
			Confidence:    e.GetConfidence(),
			Method:        e.GetMethod(),
			Evidence:      e.GetEvidence(),
			LastValidated: e.GetLastValidated(),
		}
	}
	return out
}
