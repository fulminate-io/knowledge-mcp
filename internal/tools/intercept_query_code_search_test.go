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
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// codeSearchEngineFake drives the CLIENT code-search path: Manager.Search
// returns canned RRF Hits per (repo, query) and the hydrate Execute serves the
// ranked ids[] read from a shared node table. It satisfies BOTH the
// SegmentSearcher seam (Search) and the GraphCaller seam (Execute), so one fake
// backs cdeps.mgr and cdeps.gc.
// It HONORS the k it is handed (truncating its canned list) and RECORDS that k.
// Both are load-bearing for the over-fetch assertions: a fake that returned its
// whole canned list regardless of k would satisfy a shortfall test against an
// implementation that never over-fetched at all.
type codeSearchEngineFake struct {
	mu          sync.Mutex
	searchCalls int
	lastK       int
	hitsByRepo  map[string][]searchengine.Hit
	nodes       map[string]*knowledgev1.Node
	// searchErr, when set, fails every Search — the degraded-leg probe.
	searchErr error
	// pools records the pool shape of EVERY search leg, in request order: a
	// single-pool read appends just the base name, a two-pool read appends the
	// base and the overlay it was paired with. Recording both arms in one list
	// is what lets a test assert that repo A opened an overlay while repo B did
	// not, rather than only that some overlay was opened somewhere.
	pools []poolReq
}

// poolReq is one pool request the fake served. overlay is "" for a single-pool
// read, so the two arms stay distinguishable in the recorded list.
type poolReq struct {
	base    string
	overlay string
}

func (f *codeSearchEngineFake) Search(
	_ context.Context, _ kgtypes.GraphType, name, _ string, _ []byte, k int,
) ([]searchengine.Hit, error) {
	f.mu.Lock()
	f.searchCalls++
	f.lastK = k
	f.pools = append(f.pools, poolReq{base: name})
	err := f.searchErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	hits := f.hitsByRepo[name]
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// SearchOverlay makes this fake satisfy the two-pool seam as well, so a branch
// search reaches it instead of being rejected for an absent overlay arm. It
// serves the BASE pool's canned hits — the fusion of two real pools is
// segmentdist's job and is gated there; what this fake exists to observe is
// WHICH pools each repo was asked for.
func (f *codeSearchEngineFake) SearchOverlay(
	_ context.Context, _ kgtypes.GraphType, base, overlay, _ string, _ []byte, k int,
) ([]searchengine.Hit, error) {
	f.mu.Lock()
	f.searchCalls++
	f.lastK = k
	f.pools = append(f.pools, poolReq{base: base, overlay: overlay})
	err := f.searchErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	hits := f.hitsByRepo[base]
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// requestedPools returns a copy of the recorded pool requests. The fan-out is
// concurrent, so the ORDER is not meaningful and no caller should assert on it.
func (f *codeSearchEngineFake) requestedPools() []poolReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]poolReq(nil), f.pools...)
}

// recordedK reports the k the most recent Search was handed.
func (f *codeSearchEngineFake) recordedK() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastK
}

func (f *codeSearchEngineFake) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	// Serve the ids[] hydrate read from the shared node table.
	q := req.GetQuery()
	out := make([]*knowledgev1.Node, 0)
	if q != nil {
		for _, id := range q.GetIds() {
			if n, ok := f.nodes[id]; ok {
				out = append(out, n)
			}
		}
	}
	return &knowledgev1.ExecuteResponse{Nodes: out}, nil
}

func (f *codeSearchEngineFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searchCalls
}

// cdepsFor wires a codeSearchDeps where the Searcher, the two-pool overlay arm
// and the hydrate GraphCaller are all the same fake. Wiring ovl here does not
// move any single-pool test: codeSearchPoolHits only reaches the overlay arm
// when a branch makes the overlay name differ from the base.
func cdepsFor(f *codeSearchEngineFake) codeSearchDeps {
	return codeSearchDeps{mgr: f, gc: f, ovl: f, exec: f.Execute}
}

// TestInterceptQueryCodeSearch covers single-repo (one client Search per query),
// multi-repo parallel fan-out + merge-by-score, group_by_file, comment exclusion —
// all driven through the CLIENT engine (no server-search fallback).
func TestInterceptQueryCodeSearch(t *testing.T) {
	t.Run("single repo one search per query + render", func(t *testing.T) {
		f := &codeSearchEngineFake{
			hitsByRepo: map[string][]searchengine.Hit{"knowledge": {{ID: "f.go:Foo", Score: 0.9}}},
			nodes: map[string]*knowledgev1.Node{
				"f.go:Foo": {Id: "f.go:Foo", SymbolName: "Foo", Type: "function", FilePath: "f.go", StartLine: 1},
			},
		}
		res := composeCodeSearchSingleRepo(context.Background(), nil, cdepsFor(f),
			codeSearchArgs{Graph: "code", Repo: "knowledge", Text: "foo"}, []string{"foo"}, nil, 10, true, false)
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Equal(t, 1, f.calls(), "single query → one client Search")
		assert.Contains(t, body, "[knowledge]")
		// No vector threaded (nil queryVecs) → mode label is "text", not "hybrid".
		assert.Contains(t, body, "Found 1 results for \"foo\" (mode: text):")
		assert.Contains(t, body, "### 1. Foo (function) — f.go:1 (score: 0.9000)")
	})

	t.Run("comment results excluded by default", func(t *testing.T) {
		f := &codeSearchEngineFake{
			hitsByRepo: map[string][]searchengine.Hit{"r": {{ID: "c1", Score: 0.9}, {ID: "fn", Score: 0.8}}},
			nodes: map[string]*knowledgev1.Node{
				"c1": {Id: "c1", Type: "comment", SymbolName: "c"},
				"fn": {Id: "fn", Type: "function", SymbolName: "Fn", FilePath: "a.go"},
			},
		}
		res := composeCodeSearchSingleRepo(context.Background(), nil, cdepsFor(f),
			codeSearchArgs{Graph: "code", Repo: "r", Text: "x"}, []string{"x"}, nil, 10, true, false)
		body := textBodyTools(res)
		assert.Contains(t, body, "Found 1 results")
		assert.Contains(t, body, "Fn")
		assert.NotContains(t, body, "### 1. c ")
	})

	t.Run("group_by_file groups returned results", func(t *testing.T) {
		f := &codeSearchEngineFake{
			hitsByRepo: map[string][]searchengine.Hit{"r": {{ID: "a", Score: 0.9}, {ID: "b", Score: 0.8}}},
			nodes: map[string]*knowledgev1.Node{
				"a": {Id: "a", SymbolName: "A", Type: "function", FilePath: "a.go", StartLine: 1, EndLine: 5},
				"b": {Id: "b", SymbolName: "B", Type: "function", FilePath: "a.go", StartLine: 7, EndLine: 9},
			},
		}
		res := composeCodeSearchSingleRepo(context.Background(), nil, cdepsFor(f),
			codeSearchArgs{Graph: "code", Repo: "r", Text: "x"}, []string{"x"}, nil, 10, true, true)
		body := textBodyTools(res)
		assert.Contains(t, body, "### a.go (2 symbols)")
		assert.Contains(t, body, "- `A` (function, L1-5)")
	})

	t.Run("multi-repo parallel fan-out + merge by score", func(t *testing.T) {
		// Empty temp manifest: the fan-out detects each repo's branch from the
		// machine-local manifest, and this subtest is about the fan-out and the
		// merge, not about branches. Pinning both repos to the no-entry state keeps
		// the single-pool "one client Search per repo" count deterministic instead
		// of dependent on whichever repos this developer has collected.
		withTestManifest(t)
		f := &codeSearchEngineFake{
			hitsByRepo: map[string][]searchengine.Hit{
				"repo1": {{ID: "r1", Score: 0.7}},
				"repo2": {{ID: "r2", Score: 0.95}},
			},
			nodes: map[string]*knowledgev1.Node{
				"r1": {Id: "r1", SymbolName: "Low", Type: "function", FilePath: "x.go"},
				"r2": {Id: "r2", SymbolName: "High", Type: "function", FilePath: "y.go"},
			},
		}
		res := composeCodeSearchMultiRepo(context.Background(), nil, cdepsFor(f),
			codeSearchArgs{Graph: "code", Repos: []string{"repo1", "repo2"}, Text: "x"}, []string{"x"}, nil, 10, true, false)
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Equal(t, 2, f.calls(), "one client Search per repo")
		assert.Contains(t, body, "Cross-repo search across repo1, repo2")
		// High (0.95) sorts before Low (0.70).
		assert.Less(t, indexOf(body, "High"), indexOf(body, "Low"))
		assert.Contains(t, body, "[repo2] High")
	})

	t.Run("gate: id → not claimed (analyze)", func(t *testing.T) {
		handled, _ := InterceptQueryCodeSearch(opCtx(), nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"code","id":"x"}`)})
		assert.False(t, handled)
	})
	t.Run("gate: no query → not claimed", func(t *testing.T) {
		handled, _ := InterceptQueryCodeSearch(opCtx(), nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"code"}`)})
		assert.False(t, handled)
	})
}
