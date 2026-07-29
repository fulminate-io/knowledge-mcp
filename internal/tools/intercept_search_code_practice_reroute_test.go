// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// perGraphSearcher returns canned Hits keyed by (graphType,name) and records the
// graph types it was queried for, so the code + practice reroute tests can
// assert the right per-graph client engine was driven.
type perGraphSearcher struct {
	calls   atomic.Int64
	hitsFor map[string][]searchengine.Hit
	lastGTs []kgtypes.GraphType
	mu      chan struct{}
}

func newPerGraphSearcher(hitsFor map[string][]searchengine.Hit) *perGraphSearcher {
	return &perGraphSearcher{hitsFor: hitsFor, mu: make(chan struct{}, 1)}
}

func (s *perGraphSearcher) Search(
	_ context.Context, gt kgtypes.GraphType, name, _ string, _ []byte, _ int,
) ([]searchengine.Hit, error) {
	s.calls.Add(1)
	s.mu <- struct{}{}
	s.lastGTs = append(s.lastGTs, gt)
	<-s.mu
	return s.hitsFor[string(gt)+":"+name], nil
}

// TestCodeSearchReroutesToClientEngine is Phase 3 Step 3's criterion: a code
// search (query graph:code arm) returns results from the per-repo CLIENT engine
// (Manager.Search + hydration), NOT a server RETURN_MODE_SEARCH Execute. The
// fake Manager returns ranked Hits; the recording handler serves the hydrate
// NODES read and proves no SEARCH plan was dispatched.
func TestCodeSearchReroutesToClientEngine(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "f.go:Foo", Type: "function", SymbolName: "Foo", FilePath: "f.go", StartLine: 1},
	))
	mgr := newPerGraphSearcher(map[string][]searchengine.Hit{
		"code:knowledge": {{ID: "f.go:Foo", Score: 0.9}},
	})
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	handled, out := InterceptQueryCodeSearch(opCtx(), deps, kgtools.CallToolParams{
		Name:      "query",
		Arguments: mustMarshal(t, map[string]any{"graph": "code", "repo": "knowledge", "text": "foo"}),
	})
	require.True(t, handled)
	require.False(t, out.IsError, engine.FirstTextContent(out))

	require.Equal(t, int64(1), mgr.calls.Load(), "per-repo client engine drove the code search")
	require.Equal(t, []kgtypes.GraphType{kgtypes.GraphCode}, mgr.lastGTs)
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()), "code arm must NOT dispatch a server search")
	assert.Contains(t, engine.FirstTextContent(out), "Foo")
}

// TestPracticeSearchReroutesToClientEngine is Phase 3 Step 3's criterion: a
// practice search routes through the per-LANGUAGE client engine + hydration,
// not a server search dispatch.
func TestPracticeSearchReroutesToClientEngine(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "p1", Type: "rule", SymbolName: "GoRule"},
	))
	mgr := newPerGraphSearcher(map[string][]searchengine.Hit{
		"practice:go": {{ID: "p1", Score: 0.9}},
	})
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	handled, out := InterceptQueryPracticeLinkage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "query",
		Arguments: mustMarshal(t, map[string]any{"graph": "practice", "language": "go", "text": "error handling"}),
	})
	require.True(t, handled)
	require.False(t, out.IsError, engine.FirstTextContent(out))

	require.Equal(t, int64(1), mgr.calls.Load(), "per-language client engine drove the practice search")
	require.Equal(t, []kgtypes.GraphType{kgtypes.GraphPractice}, mgr.lastGTs)
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()), "practice arm must NOT dispatch a server search")
	assert.Contains(t, engine.FirstTextContent(out), "GoRule")
}
