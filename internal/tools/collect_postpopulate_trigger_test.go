// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// ppTriggerStubName is the unique collector name used by ppTriggerStubCollector.
// Must not collide with any real collector (collector.Register panics on dupes)
// nor any real postpopulate hook key.
const ppTriggerStubName = "postpopulate-trigger-test"

// ppTriggerStubCollector is a minimal collector that succeeds with a benign
// cloud CollectResult so InterceptCollect reaches the post-collect tail-call.
type ppTriggerStubCollector struct{}

func (ppTriggerStubCollector) Name() string { return ppTriggerStubName }

func (ppTriggerStubCollector) Collect(_ context.Context, _ string, _ collector.CollectOptions) (*collectorwire.CollectResult, error) {
	return &collectorwire.CollectResult{GraphType: kgtypes.GraphCloud, GraphName: "pp-smoke"}, nil
}

var (
	ppTriggerStub     = ppTriggerStubCollector{}
	ppTriggerStubOnce sync.Once
)

func registerPPTriggerStub() {
	ppTriggerStubOnce.Do(func() { collector.Register(ppTriggerStub) })
}

// ppTriggerDeps is a ClientDeps whose GraphCaller() returns a seeded fake so the
// post-collect postpopulate orchestrator can enumerate graphs + fire the hook.
type ppTriggerDeps struct {
	sink collector.Sink
	gc   GraphCaller
}

func (d *ppTriggerDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d *ppTriggerDeps) Sink() collector.Sink                         { return d.sink }
func (d *ppTriggerDeps) RootDir() string                              { return "" }
func (d *ppTriggerDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
func (d *ppTriggerDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d *ppTriggerDeps) WorkerReady() bool                            { return true }
func (d *ppTriggerDeps) PropReady() bool                              { return true }
func (d *ppTriggerDeps) PipelineReady() bool                          { return true }
func (d *ppTriggerDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d *ppTriggerDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d *ppTriggerDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d *ppTriggerDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *ppTriggerDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *ppTriggerDeps) BackendResolver() BackendResolver             { return nil }
func (d *ppTriggerDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *ppTriggerDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *ppTriggerDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *ppTriggerDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *ppTriggerDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *ppTriggerDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d *ppTriggerDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d *ppTriggerDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d *ppTriggerDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d *ppTriggerDeps) PipelineScanner() PipelineScanner         { return nil }

func (d *ppTriggerDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d *ppTriggerDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d *ppTriggerDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d *ppTriggerDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *ppTriggerDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *ppTriggerDeps) TensionsProvider() TensionsProvider   { return nil }

// TestInterceptCollect_FiresPostPopulateHookOnLivePath proves the
// gate: PostPopulate edge enrichment demonstrably runs on the live collect path
// via wire calls. A stub collector + a stub postpopulate hook registered under
// the SAME collector type are driven through InterceptCollect with a fake Sink
// and a fakeGraphCaller; the hook must fire after the collect, receive the wire
// GraphCaller + an enumerated graph name, and its LinkEdgesBatch must land a
// captured create_batch mutation whose Target.Account==<account graph> (NOT a
// name:-only write the server would reject).
func TestInterceptCollect_FiresPostPopulateHookOnLivePath(t *testing.T) {
	registerPPTriggerStub()

	// Map the stub collector type onto the cloud graph type so the orchestrator
	// enumerates GraphCloud names. Restore afterwards so other tests are clean.
	prev, had := postPopulateGraphType[ppTriggerStubName]
	postPopulateGraphType[ppTriggerStubName] = kgtypes.GraphCloud
	t.Cleanup(func() {
		if had {
			postPopulateGraphType[ppTriggerStubName] = prev
		} else {
			delete(postPopulateGraphType, ppTriggerStubName)
		}
	})

	var (
		mu           sync.Mutex
		fired        int
		gotGraphName string
		gotNilCaller bool
	)
	postpopulate.Register(ppTriggerStubName, func(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
		mu.Lock()
		fired++
		gotGraphName = graphName
		gotNilCaller = gc == nil
		mu.Unlock()
		// Write a structural edge into the per-account cloud graph over the wire.
		return postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, []knowledgev1.Edge{
			{FromId: "role-a", ToId: "principal-b", Type: string(kgtypes.EdgeTrusts), Method: "test"},
		})
	})

	// Seed the fake to enumerate exactly one cloud graph named "aws-acct-123".
	fc := &fakeGraphCaller{
		listGraphsResult: &kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"graphs":[{"graph_type":"cloud","graph_name":"aws-acct-123"}]}`}},
		},
	}
	deps := &ppTriggerDeps{sink: noopSink{}, gc: fc}

	args := json.RawMessage(`{"type":"` + ppTriggerStubName + `","id":"pp-id","force":true}`)
	handled, result := InterceptCollect(opCtx(), deps, kgtools.CallToolParams{
		Name:      "collect",
		Arguments: args,
	})
	require.True(t, handled, "InterceptCollect should handle the call")
	require.False(t, result.IsError, "InterceptCollect returned IsError; content=%q", resultText(result))

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, fired, "the postpopulate hook must fire exactly once (one enumerated cloud graph)")
	assert.False(t, gotNilCaller, "the hook must receive a non-nil wire GraphCaller")
	assert.Equal(t, "aws-acct-123", gotGraphName, "the hook must receive the enumerated graph name")

	// The hook's LinkEdgesBatch must have landed a create_batch mutation whose
	// Target routes by Account (cloud), NOT by Name.
	require.Len(t, fc.execMutations, 1, "expected exactly one create_batch mutation from the hook")
	var foundCloudAccount bool
	for _, req := range fc.execRequests {
		if req.GetMutation() == nil {
			continue
		}
		tgt := req.GetTarget()
		assert.Equal(t, "cloud", tgt.GetGraph(), "edge write must target the cloud graph")
		assert.Equal(t, "aws-acct-123", tgt.GetAccount(), "edge write must route by Account==aws-acct-123 (NOT Name)")
		assert.Empty(t, tgt.GetName(), "cloud edge write must NOT route by Name")
		if tgt.GetGraph() == "cloud" && tgt.GetAccount() == "aws-acct-123" {
			foundCloudAccount = true
		}
	}
	assert.True(t, foundCloudAccount, "the live-path wire call must route to the per-account cloud graph")
}
