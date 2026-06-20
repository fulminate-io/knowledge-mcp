// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// tailRoutingDeps is a ClientDeps whose GraphCaller() (the login-routed caller)
// and LocalGraphCaller() (the always-local caller) return DISTINCT recorders so
// the post-collect tail test can prove which one the linker + postpopulate use.
type tailRoutingDeps struct {
	routed GraphCaller
	local  GraphCaller
}

func (d *tailRoutingDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d *tailRoutingDeps) Sink() collector.Sink                         { return noopSink{} }
func (d *tailRoutingDeps) RootDir() string                              { return "" }
func (d *tailRoutingDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d *tailRoutingDeps) WorkerReady() bool                            { return true }
func (d *tailRoutingDeps) PropReady() bool                              { return true }
func (d *tailRoutingDeps) PipelineReady() bool                          { return true }
func (d *tailRoutingDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d *tailRoutingDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d *tailRoutingDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d *tailRoutingDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *tailRoutingDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *tailRoutingDeps) BackendResolver() BackendResolver             { return nil }
func (d *tailRoutingDeps) GraphCaller() GraphCaller                     { return d.routed }
func (d *tailRoutingDeps) LocalGraphCaller() GraphCaller                { return d.local }
func (d *tailRoutingDeps) RepoResolver() *RepoResolver                  { return nil }
func (d *tailRoutingDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *tailRoutingDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *tailRoutingDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *tailRoutingDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d *tailRoutingDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d *tailRoutingDeps) PipelineScanner() PipelineScanner             { return nil }
func (d *tailRoutingDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d *tailRoutingDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d *tailRoutingDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *tailRoutingDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *tailRoutingDeps) TensionsProvider() TensionsProvider   { return nil }

// TestPostCollectTail_RoutesThroughGraphCaller proves Phase 2: the post-collect
// linker + postpopulate tail follow the data through deps.GraphCaller() (the
// login-routed caller), NOT deps.LocalGraphCaller(). Both accessors return
// distinct recording fakeGraphCallers; after driving both tail helpers, the
// ROUTED recorder must have received the calls and the LOCAL recorder none.
func TestPostCollectTail_RoutesThroughGraphCaller(t *testing.T) {
	const tailType = "postcollect-tail-routing-test"

	// Map the test collector type onto the cloud graph type so postpopulate
	// enumerates GraphCloud names; restore afterwards.
	prev, had := postPopulateGraphType[tailType]
	postPopulateGraphType[tailType] = kgtypes.GraphCloud
	t.Cleanup(func() {
		if had {
			postPopulateGraphType[tailType] = prev
		} else {
			delete(postPopulateGraphType, tailType)
		}
	})

	// Register a postpopulate hook for the test type that records its firing
	// but does no work — the enumeration + hook invocation ride the caller.
	var hookMu sync.Mutex
	var hookFired int
	postpopulate.Register(tailType, func(_ context.Context, _ postpopulate.GraphCaller, _ string) error {
		hookMu.Lock()
		hookFired++
		hookMu.Unlock()
		return nil
	})

	// Add the test type to the linker-trigger set so runPostCollectLinker fires.
	prevLink, hadLink := postCollectLinkerTypes[tailType]
	postCollectLinkerTypes[tailType] = true
	t.Cleanup(func() {
		if hadLink {
			postCollectLinkerTypes[tailType] = prevLink
		} else {
			delete(postCollectLinkerTypes, tailType)
		}
	})

	seed := func() *fakeGraphCaller {
		return &fakeGraphCaller{
			listGraphsResult: &kgtools.ToolResult{
				Content: []kgtools.ContentBlock{{Type: "text", Text: `{"graphs":[{"graph_type":"cloud","graph_name":"aws-acct-123"}]}`}},
			},
		}
	}
	routed := seed()
	local := seed()
	deps := &tailRoutingDeps{routed: routed, local: local}

	ctx := context.Background()

	// Drive the linker tail (test type is in the linker-trigger set).
	runPostCollectLinker(ctx, deps, tailType)
	// Drive the postpopulate tail.
	runPostCollectPostPopulate(ctx, deps, tailType)

	hookMu.Lock()
	fired := hookFired
	hookMu.Unlock()
	assert.Equal(t, 1, fired, "the postpopulate hook must fire once via the routed caller's enumeration")
	assert.NotEmpty(t, routed.calls, "the routed GraphCaller must receive the post-collect tail calls")
	assert.Empty(t, local.calls, "the local-only GraphCaller must NOT receive any post-collect tail calls")
}
