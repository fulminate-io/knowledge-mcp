// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// EmbedItem is the input the pipeline embed worker composes per-id text
// into before calling the embedder. Client-local carrier — the embed
// pipeline lives entirely client-side, so the shape lives here rather
// than in the (server-side) store package. Mirrors the historical
// store.EmbedItem shape verbatim.
type EmbedItem struct {
	ID   string
	Text string
}

// SummaryWork is a single summarization work item flowing through the
// pipeline. Carries only (GraphType, GraphName, NodeID); the worker
// re-Retrieves the node at write time so concurrent edits don't get
// clobbered with stale data and so the collector goroutine doesn't pin a
// hydrated Node in memory while the channel queues it.
//
// Release is the per-collector channel the worker writes NodeID into when
// it has finished processing this item (success, transient, or terminal).
// The collector uses these releases to drop IDs from its in-flight set so
// the next discovery cycle can re-queue transient-failed items. May be nil
// for callers (e.g. tests) that don't track in-flight state.
type SummaryWork struct {
	GraphType kgtypes.GraphType
	GraphName string
	NodeID    string
	// SummarizeText is the server-composed chunkInput JSON envelope
	// (item.GetSummarizeText()) the worker feeds straight to the
	// summarizer — it no longer re-fetches the node or composes the envelope
	// client-side. The summary axis drops empty composed text server-side, so
	// in steady state this is non-empty.
	SummarizeText string
	Release       chan<- string
	// Backend is the CONCRETE backend the originating collector scanned this
	// item from (login-routed: cloud when logged in, local otherwise). The
	// worker writes the summary result back through THIS backend — not the
	// shared p.client — so a survivor graphKey's items always land on the
	// backend that produced them, even across a mid-session login flip. The
	// writeback grouping keys on (graphKey, Backend) to keep each group
	// backend-homogeneous. Constant per collector (one collector scans one
	// backend). May be nil for tests that drive the worker without a collector;
	// the worker falls back to p.client in that case.
	Backend WireClient
}

// EmbedWork mirrors SummaryWork for the embed system. The worker reads the
// SERVER-COMPOSED EmbedText straight into the embedder input — it no
// longer re-fetches the node or composes EmbedText client-side. EmbedText may
// be whitespace-only (the server EMITS empty-embed items so the client's
// markStuckEmbedItems path can stamp the durable failure marker). Release
// semantics match SummaryWork.
type EmbedWork struct {
	GraphType kgtypes.GraphType
	GraphName string
	NodeID    string
	EmbedText string // server-composed embed input (item.GetEmbedText()); may be empty
	// Bm25Fields is the server-composed per-field BM25 text (item.GetBm25Fields()),
	// populated only on the EMBED axis (Option A). The client builds BM25
	// segments from these fields at the embed writeback seam (alongside the HNSW
	// vector ship). May be nil — the server leaves it nil for nodes with no
	// indexable field, and a code-leaf embedded via Content before its summary/
	// keywords land carries a thin map (acceptable: transient, self-heals when
	// re-summarization bumps the embed dirty-gen and re-ships — it only
	// under-indexes that one node for the brief window until it re-ships).
	Bm25Fields map[string]string
	Release    chan<- string
	// Backend is the CONCRETE backend the originating collector scanned this
	// item from. See SummaryWork.Backend — same login-routed-writeback contract
	// on the embed axis. Constant per collector; may be nil for collector-less
	// tests (worker falls back to p.client).
	Backend WireClient
}

// Metrics is a Snapshot of the pipeline's observable counters. Returned by
// Pipeline.Metrics; fed to manage(operation="status") so operators see live
// channel/worker state instead of metadata-state aggregates.
//
// Counter semantics:
//   - *Queued: depth of the corresponding work channel at snapshot time.
//   - *Running: count of workers actively processing a batch (mid-call).
//   - *Succeeded: cumulative successes since process start.
//   - *Failed: cumulative terminal failures (markers written) since process
//     start. Transient failures (429/5xx) DO NOT contribute — they are
//     retried on the next tick, not counted as failures.
type Metrics struct {
	SummaryQueued    int64
	SummaryRunning   int64
	SummarySucceeded int64
	SummaryFailed    int64
	EmbedQueued      int64
	EmbedRunning     int64
	EmbedSucceeded   int64
	EmbedFailed      int64
}
