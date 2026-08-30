// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// ctxCapturingCaller is a GraphCaller that records the per-session values off the
// context it is handed, then returns an empty response. The existing test callers
// are real *graphclient.GraphClient instances pointed at an httptest server, which
// cannot observe client-side context VALUES — they do not cross the wire — so the
// leaf has to be a local fake to see them at all.
type ctxCapturingCaller struct {
	mu        sync.Mutex
	entered   int
	sessionID string
	cwd       string
	operation string
}

func (c *ctxCapturingCaller) Execute(
	ctx context.Context,
	_ *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entered++
	c.sessionID = session.SessionIDFromContext(ctx)
	c.cwd = session.WorkspaceCwdFromContext(ctx)
	op, _ := graphclient.OperationFromContext(ctx)
	c.operation = string(op)
	return &knowledgev1.ExecuteResponse{}, nil
}

// sessionPropagationDeps is a ClientDeps exposing the capturing caller as both the
// routed and the local GraphCaller. Method set copied from interceptDeps
// (intercept_search_query_dispatch_test.go:175).
type sessionPropagationDeps struct{ gc GraphCaller }

func (d sessionPropagationDeps) LocalLiveness() LocalLiveness    { return nil }
func (d sessionPropagationDeps) Sink() collector.Sink            { return nil }
func (d sessionPropagationDeps) RootDir() string                 { return "" }
func (d sessionPropagationDeps) UsageAnalyzer() UsageAnalyzerAPI { return nil }

func (d sessionPropagationDeps) PropReady() bool     { return true }
func (d sessionPropagationDeps) PipelineReady() bool { return true }

func (d sessionPropagationDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d sessionPropagationDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d sessionPropagationDeps) BackendResolver() BackendResolver             { return nil }
func (d sessionPropagationDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d sessionPropagationDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d sessionPropagationDeps) SegmentManager() SegmentSearcher              { return nil }
func (d sessionPropagationDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d sessionPropagationDeps) SegmentShipper() SegmentShipper               { return nil }
func (d sessionPropagationDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d sessionPropagationDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d sessionPropagationDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d sessionPropagationDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d sessionPropagationDeps) PipelineScanner() PipelineScanner         { return nil }

func (d sessionPropagationDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d sessionPropagationDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d sessionPropagationDeps) SimilarityForcer() SimilarityForcer       { return nil }
func (d sessionPropagationDeps) BlindSpotProvider() BlindSpotProvider     { return nil }
func (d sessionPropagationDeps) ClusterProvider() ClusterProvider         { return nil }
func (d sessionPropagationDeps) TensionsProvider() TensionsProvider       { return nil }

// TestSessionValuesReachInterceptLeaf proves the per-session values the HTTP
// daemon stamps in handlePOST — session.ContextWithSessionID and
// session.ContextWithWorkspaceCwd (mcp_http.go:269-270) — survive the intercept
// seam and are readable at the leaf that actually issues the RPC.
//
// The session id and workspace cwd are DIFFERENT concrete strings, neither
// derived from the other, so a fixture that collapsed the two could not pass.
//
// Not red-first, and it does not need to be: assertions on the two stamped values
// fail against any implementation that hands the leaf a context not derived from
// the caller's — a fresh Background, or a WithoutCancel of one. The operation
// assertion is a characterization guard pinning the labeling this ticket must not
// regress.
func TestSessionValuesReachInterceptLeaf(t *testing.T) {
	const (
		wantSession = "ful1014-session"
		wantCwd     = "/tmp/ful1014-workspace"
	)

	for _, tc := range []struct {
		name string
		call func(ctx context.Context, deps ClientDeps) (bool, kgtools.ToolResult)
	}{
		{
			// Route traced in current source: intercept_mutate.go:166 name gate →
			// :171 GraphCaller → :230 update arm → handleInterceptMutateUpdate
			// (:265) → lookupNodeBackend (:293) → render.FetchNode over Execute.
			name: "InterceptMutate_update",
			call: func(ctx context.Context, deps ClientDeps) (bool, kgtools.ToolResult) {
				return InterceptMutate(ctx, deps, kgtools.CallToolParams{
					Name:      "mutate",
					Arguments: json.RawMessage(`{"operation":"update","id":"ful1014-probe","status":"completed"}`),
				})
			},
		},
		{
			// Route traced in current source: intercept_query_lineage.go:33 name
			// gate → :40 mode gate → :43 id gate → :47 GraphCaller → :52
			// render.FetchNode over Execute — the first unconditional RPC.
			name: "InterceptQueryLineage",
			call: func(ctx context.Context, deps ClientDeps) (bool, kgtools.ToolResult) {
				return InterceptQueryLineage(ctx, deps, kgtools.CallToolParams{
					Name:      "query",
					Arguments: json.RawMessage(`{"mode":"lineage","id":"ful1014-probe"}`),
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gc := &ctxCapturingCaller{}
			ctx := session.ContextWithWorkspaceCwd(
				session.ContextWithSessionID(opCtx(), wantSession), wantCwd)

			tc.call(ctx, sessionPropagationDeps{gc: gc})

			// Precondition FIRST: a case that never reached the leaf is a broken
			// probe, not a propagation failure, and must say so.
			require.Positive(t, gc.entered,
				"the intercept never issued an RPC through GraphCaller.Execute — the probe is broken, not the property")

			assert.Equal(t, wantSession, gc.sessionID,
				"the daemon-stamped session id must reach the leaf that issues the RPC")
			assert.Equal(t, wantCwd, gc.cwd,
				"the daemon-stamped workspace cwd must reach the leaf that issues the RPC")
			assert.NotEmpty(t, gc.operation,
				"the query-origin operation label must still ride the same ctx")
		})
	}
}
