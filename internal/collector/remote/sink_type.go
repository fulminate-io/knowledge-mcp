// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"sync/atomic"

	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// sink_type.go holds the UploadSink TYPE, its constructors and its interface
// assertion — split out of sink.go for the 500-line file cap. The methods stay
// where they are: WriteResult, planDiffUpload and uploadChunks in sink.go, the
// rest across the package's other sink_*.go files.

// IngestClientPicker resolves the IngestService client to use for one call.
// It is invoked PER CALL (WriteResult, collectChunkWithRetry, FetchCloudSubgraph)
// so a mid-session `knowledge login` flip re-routes the next chunk to the cloud
// backend without a process restart. Router.IngestClient(ctx) satisfies this
// shape (router.go); NewUploadSink wraps a fixed client into a constant picker.
type IngestClientPicker func(ctx context.Context) (knowledgev1connect.IngestServiceClient, error)

// UploadSink implements collector.Sink by driving the unary IngestService
// CollectChunk + Finalize flow. Used by cmd/knowledge (the client binary) so
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
	// false: every client invocation is its own process with its own sink, they all
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
	// benchForceFullNoDiff makes WriteResult skip the manifest fetch and the diff
	// entirely and upload every file, exactly as the pre-incremental client did.
	// Zero-value valid, and PRODUCTION NEVER SETS IT: the only thing that can is
	// NewUploadSinkForBenchFullPath, which lives behind `//go:build collectbench`
	// and therefore does not exist in a shipped binary at all.
	//
	// IT IS DECLARED HERE RATHER THAN IN THE TAGGED FILE BECAUSE GO CANNOT ADD A
	// STRUCT FIELD FROM A BUILD-TAGGED FILE. That is the disclosed cost of the
	// arrangement: one bool on the production struct that production leaves false.
	// The alternative — a second sink type duplicating WriteResult — would put the
	// bench on a code path that is not the shipped one, which defeats the point of
	// the comparison it exists to make.
	benchForceFullNoDiff bool
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
