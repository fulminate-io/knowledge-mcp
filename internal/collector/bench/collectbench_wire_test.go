// SPDX-License-Identifier: Apache-2.0

//go:build collectbench

package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
)

// collectbench_wire_test.go — the wire observer, and the reader half of the
// conductor's psql samples.
//
// WHY A RECORDING CLIENT AT ALL. Two of this bench's assertions are about what
// the client PUT ON THE WIRE, not about what landed: how many CollectManifest
// RPCs an arm issued, and whether the FinalizeRequest carried a deletion set. A
// server-side reading cannot answer either — a manifest render and a Finalize
// with an empty DeletedFiles both look like ordinary traffic in the census — so
// the observation has to happen at the client's own transport. This wrapper is
// the whole mechanism, and it is a pass-through: it counts and captures, and
// changes nothing about the request or the response.

// recordingIngestClient decorates the real IngestService client, counting
// CollectManifest calls and capturing what each Finalize carried.
type recordingIngestClient struct {
	inner knowledgev1connect.IngestServiceClient

	mu sync.Mutex
	// manifestRPCs is how many CollectManifest calls this sink issued. The
	// full-path arm's own gate asserts it is ZERO.
	manifestRPCs int
	// deletedFiles is the DeletedFiles set carried by the LAST Finalize. One
	// collect issues exactly one Finalize, so for a single run "last" is "the".
	deletedFiles []string
	// finalizes counts Finalize calls, so a zero-length deletion set read off a
	// run that never finalized cannot pass as an empty one.
	finalizes int
}

func (c *recordingIngestClient) CollectChunk(
	ctx context.Context, req *connect.Request[knowledgev1.CollectChunkRequest],
) (*connect.Response[knowledgev1.CollectChunkResponse], error) {
	return c.inner.CollectChunk(ctx, req)
}

func (c *recordingIngestClient) Finalize(
	ctx context.Context, req *connect.Request[knowledgev1.FinalizeRequest],
) (*connect.Response[knowledgev1.FinalizeResponse], error) {
	c.mu.Lock()
	c.finalizes++
	c.deletedFiles = append([]string(nil), req.Msg.GetDeletedFiles()...)
	c.mu.Unlock()
	return c.inner.Finalize(ctx, req)
}

func (c *recordingIngestClient) FinalizeStatus(
	ctx context.Context, req *connect.Request[knowledgev1.FinalizeStatusRequest],
) (*connect.Response[knowledgev1.FinalizeStatusResponse], error) {
	return c.inner.FinalizeStatus(ctx, req)
}

func (c *recordingIngestClient) CollectManifest(
	ctx context.Context, req *connect.Request[knowledgev1.CollectManifestRequest],
) (*connect.Response[knowledgev1.CollectManifestResponse], error) {
	c.mu.Lock()
	c.manifestRPCs++
	c.mu.Unlock()
	return c.inner.CollectManifest(ctx, req)
}

func (c *recordingIngestClient) FetchCloudSubgraph(
	ctx context.Context, req *connect.Request[knowledgev1.FetchCloudSubgraphRequest],
) (*connect.Response[knowledgev1.FetchCloudSubgraphResponse], error) {
	return c.inner.FetchCloudSubgraph(ctx, req)
}

// observed returns what this client saw. Copied under the lock so a caller
// cannot race the transport.
func (c *recordingIngestClient) observed() (manifestRPCs, finalizes int, deleted []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.manifestRPCs, c.finalizes, append([]string(nil), c.deletedFiles...)
}

// Compile-time assertion: a method added to the service must be added here too,
// rather than silently leaving this decorator behind.
var _ knowledgev1connect.IngestServiceClient = (*recordingIngestClient)(nil)

// psqlSample is the server half of one run's record, written by the conductor
// around each collect.
//
// EVERY FIELD IS A MEASUREMENT THE CLIENT MODULE CANNOT TAKE. Landed rows, xmin
// movement and the statement census are only readable over psql, and this module
// must not grow a Postgres driver — the same promise that keeps testcontainers
// out of it, because cmd/knowledge is the OSS-shipped binary.
type psqlSample struct {
	RunLabel string `json:"run_label"`
	// FileOwnedXminChurn counts file-owned node rows whose xmin MOVED across the
	// run — the quiescence instrument. A hard-deleted row is gone rather than
	// rewritten, so its xmin cannot move, which is what makes xmin immune to the
	// prune hazard that can fool a row count.
	FileOwnedXminChurn int64 `json:"file_owned_xmin_churn"`
	// FileOwnedRows is the population that churn is out of — the known-positive
	// control that stops a zero churn over an EMPTY population reading as
	// quiescence.
	FileOwnedRows int64 `json:"file_owned_rows"`
	// FilelessRelands counts fileless node rows whose xmin moved, and
	// FilelessRows is their population. Reported here, gated elsewhere:
	// TestCollectBench_FilelessResidueIsZero asserts the zero, in the work whose
	// server-side skip clause is what makes it true.
	FilelessRelands int64 `json:"fileless_relands"`
	FilelessRows    int64 `json:"fileless_rows"`
	// NodeRows/EdgeRows are the landed totals after the run.
	NodeRows int64 `json:"node_rows"`
	EdgeRows int64 `json:"edge_rows"`
	// NodeWriteRows/EdgeWriteRows are the same xmin-movement count taken PER RAIL,
	// and WriteRows is their sum. The rails are kept apart because a run where
	// nodes collapse but edges do not is precisely the pre-enforcement state, and
	// edges were 176689 of the 210646 rows landed at RUN 1 — the majority of the
	// write volume. A single total would report that state as bounded.
	NodeWriteRows int64 `json:"node_write_rows"`
	EdgeWriteRows int64 `json:"edge_write_rows"`
	// WriteRows is how many node+edge rows the run actually WROTE, counted as
	// xmin movement across both tables rather than as a delta of totals: a count
	// delta reads ZERO when a run rewrites every row in place, which is exactly
	// the regression these bounds exist to catch.
	WriteRows int64 `json:"write_rows"`
	// ReadSideBuffers and WriteSideBuffers split the census by whether the
	// statement mutates. A blended ratio is forbidden: the manifest render is a
	// deliberate O(repo) READ floor while the write path must collapse to O(K),
	// so one number mixing them false-passes a regressed write path hiding
	// behind a constant read floor.
	ReadSideBuffers  int64 `json:"read_side_buffers"`
	WriteSideBuffers int64 `json:"write_side_buffers"`
	// ServerExecMS is the COLLECT-PATH total_exec_time sum over the SAME
	// top-15-by-buffers census RUN 1 used. A top-N sum, never a complete total —
	// the artifact says so beside it rather than relabelling it as one.
	ServerExecMS int64 `json:"server_exec_ms"`
}

// readSample loads one run's psql sample by file name.
//
// AN ABSENT SAMPLE IS A LOUD FAILURE, NEVER A ZERO. Every bound below compares
// against a measured number, and a zero-valued struct would satisfy most of them
// silently — a conductor phase that never ran would then read as a pass.
func readSample(t *testing.T, name string) psqlSample {
	t.Helper()
	var s psqlSample
	readJSON(t, name, &s)
	require.NotEmpty(t, s.RunLabel, "%s carries no run_label: the conductor wrote a malformed sample", name)
	return s
}

// readFacts loads one run's client-side record, with the same absent-is-loud
// rule as readSample.
func readFacts(t *testing.T, name string) benchRunFacts {
	t.Helper()
	var f benchRunFacts
	readJSON(t, name, &f)
	require.NotEmpty(t, f.Graph, "%s carries no graph: the conductor wrote a malformed record", name)
	return f
}

// readJSON is the shared loader; it exists so the absent-is-loud message is
// worded once rather than drifting between the two readers.
func readJSON(t *testing.T, name string, into any) {
	t.Helper()
	dir := os.Getenv(envSamplesDir)
	require.NotEmpty(t, dir, "%s is unset — the conductor exports it", envSamplesDir)
	path := filepath.Join(dir, name)
	blob, err := os.ReadFile(path) //nolint:gosec // a conductor-supplied workdir path
	require.NoError(t, err,
		"%s is missing: the conductor did not run the phase that writes it, so there is nothing to assert against", path)
	require.NoError(t, json.Unmarshal(blob, into), "parse %s", path)
}
