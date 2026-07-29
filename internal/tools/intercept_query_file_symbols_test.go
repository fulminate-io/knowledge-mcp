// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// fsFake routes Execute for the file_symbols recipe: file ByID, CONTAINS-forward
// IDs, the keyset-paged type=file id index, the file_path-EQ per-file browse, and
// the bulk ids[] hydrate. Counts bulk hydrates so the no-per-symbol-N+1 invariant
// can be asserted, and counts the two fallback reads separately so the memo can
// be asserted (an index browsed once per CALL, not once per PATH).
//
// There is deliberately NO whole-graph default arm: a regression that
// reintroduces the match-all read must fail loudly here rather than be served.
type fsFake struct {
	fileNode     knowledgev1.Node
	symIDs       []string
	symbols      []knowledgev1.Node
	allNodes     []knowledgev1.Node // corpus for the file_path-EQ browse
	fileIDs      []string           // corpus for the type=file id index
	bulkHyd      int
	indexBrowses int
	pathBrowses  int
	useFallback  bool
}

func (f *fsFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	sel := q.GetSelection()
	switch {
	case q.GetById() != "":
		if f.useFallback {
			return &knowledgev1.ExecuteResponse{}, nil // file ByID misses → fallback.
		}
		resp := enginetest.ResponseWithNodes([]*knowledgev1.Node{&f.fileNode}...)
		return resp, nil
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_IDS && len(sel.GetFromId()) > 0:
		// CONTAINS-forward symbol-id traverse.
		return &knowledgev1.ExecuteResponse{Ids: f.symIDs}, nil
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_IDS && sel.GetNodeType() == string(kgtypes.NodeFile):
		// type=file id index — served as a real keyset page so the drain's cursor
		// contract is exercised rather than assumed.
		f.indexBrowses++
		return &knowledgev1.ExecuteResponse{Ids: keysetSlice(f.fileIDs, q.GetAfterId(), int(q.GetLimit()))}, nil
	case len(sel.GetFieldPredicates()) > 0:
		f.pathBrowses++
		want := sel.GetFieldPredicates()[0].GetValue()
		var out []*knowledgev1.Node
		for i := range f.allNodes {
			if f.allNodes[i].FilePath == want {
				out = append(out, &f.allNodes[i])
			}
		}
		return enginetest.ResponseWithNodes(out...), nil
	case len(q.GetIds()) > 0:
		f.bulkHyd++
		resp := enginetest.ResponseWithNodes(nodePtrs(f.symbols)...)
		return resp, nil
	}
	return nil, fmt.Errorf("fsFake: unrouted plan (a match-all whole-graph read is a regression): %+v", q)
}

// keysetSlice serves ids STRICTLY AFTER the cursor, ascending, capped at limit —
// the same contract the real backend's keyset browse honors.
func keysetSlice(ids []string, afterID string, limit int) []string {
	out := make([]string, 0, limit)
	for _, id := range ids {
		if afterID != "" && id <= afterID {
			continue
		}
		out = append(out, id)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// fsCaller adapts fsFake to the Execute-only GraphCaller the ast hydrator takes.
type fsCaller struct{ f *fsFake }

func (c fsCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return c.f.exec(ctx, req)
}

// TestComposeFileSymbols_ContainsPath asserts the CONTAINS fast path: file ByID +
// CONTAINS IDs + ONE bulk hydrate (no per-symbol N+1) + StartLine sort + render.
func TestComposeFileSymbols_ContainsPath(t *testing.T) {
	f := &fsFake{
		fileNode: knowledgev1.Node{Id: "f.go", Type: string(kgtypes.NodeFile), Summary: "the file"},
		symIDs:   []string{"f.go:B", "f.go:A"},
		symbols: []knowledgev1.Node{
			{Id: "f.go:B", SymbolName: "B", Type: "function", FilePath: "f.go", StartLine: 30, EndLine: 40},
			{Id: "f.go:A", SymbolName: "A", Type: "function", FilePath: "f.go", StartLine: 10, EndLine: 20},
		},
	}
	res := composeFileSymbols(context.Background(), f.exec, fileSymbolsArgs{Repo: "knowledge"}, []string{"f.go"})
	require.False(t, res.IsError, textBodyTools(res))
	body := textBodyTools(res)

	assert.Equal(t, 1, f.bulkHyd, "exactly one bulk ids[] hydrate (no per-symbol N+1)")
	assert.Contains(t, body, "[knowledge]")
	assert.Contains(t, body, "# Symbols in f.go (3 found)")
	assert.Contains(t, body, "**File summary:** the file")
	// StartLine sort: A (L10) before B (L30) — find their positions in body.
	assert.Less(t, indexOf(body, "A (function) — L10-20"), indexOf(body, "B (function) — L30-40"))
}

// TestComposeFileSymbols_JSON asserts the format=json buildFileSymbolsJSONPayload
// shape.
func TestComposeFileSymbols_JSON(t *testing.T) {
	f := &fsFake{
		fileNode: knowledgev1.Node{Id: "f.go", Type: string(kgtypes.NodeFile)},
		symIDs:   []string{"f.go:A"},
		symbols: []knowledgev1.Node{
			{Id: "f.go:A", SymbolName: "A", Type: "function", FilePath: "f.go", StartLine: 1, EndLine: 5, Signature: "func A()"},
		},
	}
	res := composeFileSymbols(context.Background(), f.exec, fileSymbolsArgs{Repo: "r", Format: "json"}, []string{"f.go"})
	require.False(t, res.IsError)
	var payload engine.FileSymbolsJSONResponse
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(res)), &payload))
	assert.Equal(t, []string{"f.go"}, payload.FilePaths)
	assert.Equal(t, 2, payload.Total) // file node + 1 symbol.
	found := false
	for _, s := range payload.Symbols {
		if s.SymbolName == "A" && s.Signature == "func A()" {
			found = true
		}
	}
	assert.True(t, found)
}

// TestComposeFileSymbols_SuffixFallback asserts the suffix fallback resolves the
// right file and returns only its symbols when the direct file-id ByID misses.
// Behavior-preservation guard: the assertions are unchanged from the pre-bounding
// implementation; only the seeded corpus moved from one whole-graph read to the
// file index plus the per-path browse.
func TestComposeFileSymbols_SuffixFallback(t *testing.T) {
	f := &fsFake{
		useFallback: true,
		fileIDs:     []string{"pkg/bar.go", "pkg/foo.go"},
		allNodes: []knowledgev1.Node{
			{Id: "x", SymbolName: "Match", Type: "function", FilePath: "pkg/foo.go", StartLine: 1},
			{Id: "y", SymbolName: "NoMatch", Type: "function", FilePath: "pkg/bar.go", StartLine: 2},
		},
	}
	res := composeFileSymbols(context.Background(), f.exec, fileSymbolsArgs{Repo: "r"}, []string{"foo.go"})
	body := textBodyTools(res)
	assert.Contains(t, body, "Match")
	assert.NotContains(t, body, "NoMatch")
}

// seedFileIndex builds an id-ascending file corpus of n paths, one node each, so
// a drain over it pages predictably.
func seedFileIndex(n int) ([]string, []knowledgev1.Node) {
	ids := make([]string, 0, n)
	nodes := make([]knowledgev1.Node, 0, n)
	for i := range n {
		p := fmt.Sprintf("pkg/f%05d.go", i)
		ids = append(ids, p)
		nodes = append(nodes, knowledgev1.Node{Id: p + ":Fn", SymbolName: "Fn", Type: "function_declaration", FilePath: p, StartLine: 1})
	}
	return ids, nodes
}

// TestFileSymbolsCollector_FallbackBrowsesFileIndexOncePerCall is the assertion
// that the per-path multiplier is gone: three resolving paths in ONE tool call
// cost ONE index drain, not three. It fails against any implementation that
// re-browses the index per path.
func TestFileSymbolsCollector_FallbackBrowsesFileIndexOncePerCall(t *testing.T) {
	// More than 2*BrowsePageSize so the drain pages at least three times — a
	// single-page corpus could not tell one drain from three.
	const corpus = 2*engine.BrowsePageSize + 1
	ids, nodes := seedFileIndex(corpus)
	f := &fsFake{useFallback: true, fileIDs: ids, allNodes: nodes}

	wantPages := corpus/engine.BrowsePageSize + 1 // full pages + the short final one
	res := composeFileSymbols(context.Background(), f.exec, fileSymbolsArgs{Repo: "r"},
		[]string{"f00001.go", "f00002.go", "f00003.go"})
	require.False(t, res.IsError, textBodyTools(res))

	assert.Equal(t, wantPages, f.indexBrowses, "the file index is drained ONCE per tool call, not once per path")
	assert.Equal(t, 3, f.pathBrowses, "one bounded file_path-EQ browse per resolved path")
}

// TestFileSymbolsCollector_FallbackReturnsContainsOrphans guards against a future
// simplification that re-enters the CONTAINS path with a resolved exact file id:
// anonymous declarations carry a file_path but have NO inbound CONTAINS edge, so
// that shortcut would silently drop them. Only a file_path-EQ browse reproduces
// the fallback's set.
func TestFileSymbolsCollector_FallbackReturnsContainsOrphans(t *testing.T) {
	f := &fsFake{
		useFallback: true,
		fileIDs:     []string{"pkg/foo.go"},
		symIDs:      []string{"pkg/foo.go:Fn"}, // the orphan is deliberately absent
		allNodes: []knowledgev1.Node{
			{Id: "pkg/foo.go", SymbolName: "foo.go", Type: string(kgtypes.NodeFile), FilePath: "pkg/foo.go"},
			{Id: "pkg/foo.go:Fn", SymbolName: "Fn", Type: "function_declaration", FilePath: "pkg/foo.go", StartLine: 10, EndLine: 20},
			{Id: "pkg/foo.go:L70-78", SymbolName: "anonVar", Type: "var_declaration", FilePath: "pkg/foo.go", StartLine: 70, EndLine: 78},
		},
	}
	res := composeFileSymbols(context.Background(), f.exec, fileSymbolsArgs{Repo: "r"}, []string{"foo.go"})
	require.False(t, res.IsError, textBodyTools(res))
	body := textBodyTools(res)

	assert.Contains(t, body, "(3 found)", "the file node, the CONTAINS-linked function AND the CONTAINS-orphan")
	assert.Contains(t, body, "Fn")
	assert.Contains(t, body, "anonVar")
}

// recordingCaller captures the GraphSelector every Execute carries, which is the
// only place the branch is observable: a hydration result is identical with and
// without the branch against a fake that ignores it, so a result-only assertion
// would be green against the unfixed code.
type recordingCaller struct {
	f       *fsFake
	targets []*knowledgev1.GraphSelector
}

func (c *recordingCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.targets = append(c.targets, req.GetTarget())
	return c.f.exec(ctx, req)
}

func TestAstHydrator_PassesBranch(t *testing.T) {
	f := &fsFake{
		fileNode: knowledgev1.Node{Id: "pkg/foo.go", Type: string(kgtypes.NodeFile), FilePath: "pkg/foo.go"},
		symIDs:   []string{"pkg/foo.go:Fn"},
		symbols:  []knowledgev1.Node{{Id: "pkg/foo.go:Fn", SymbolName: "Fn", Type: "function_declaration", FilePath: "pkg/foo.go"}},
	}
	rc := &recordingCaller{f: f}
	b := graphClientHydratorBackend{gc: rc, repo: "knowledge", branch: "feature-x"}

	require.NoError(t, b.IterateFunctionish(context.Background(), []string{"pkg/foo.go"}, func(*knowledgev1.Node) error { return nil }))

	require.NotEmpty(t, rc.targets)
	for _, tg := range rc.targets {
		assert.Equal(t, "feature-x", tg.GetBranch(), "hydration must read the branch overlay, not the base graph")
		assert.Equal(t, "knowledge", tg.GetRepo())
	}
}

// TestAstHydrator_MemoizesFileIndexAcrossFiles is the exact mirror of the
// composeFileSymbols memo assertion, so both collectors are held to one standard.
// Without it a hydrator that builds its collector INSIDE the per-file loop would
// pass every other test in this plan.
func TestAstHydrator_MemoizesFileIndexAcrossFiles(t *testing.T) {
	const corpus = 2*engine.BrowsePageSize + 1
	ids, nodes := seedFileIndex(corpus)
	f := &fsFake{useFallback: true, fileIDs: ids, allNodes: nodes}
	b := graphClientHydratorBackend{gc: fsCaller{f: f}, repo: "knowledge"}

	wantPages := corpus/engine.BrowsePageSize + 1
	require.NoError(t, b.IterateFunctionish(context.Background(),
		[]string{"f00001.go", "f00002.go", "f00003.go"}, func(*knowledgev1.Node) error { return nil }))

	assert.Equal(t, wantPages, f.indexBrowses, "ONE collector per Hydrate call — the index is drained once, not once per file")
	assert.Equal(t, 3, f.pathBrowses, "one bounded file_path-EQ browse per resolved file")
}

// TestInterceptFileSymbols_Gate asserts the standalone tool + query-mode claims.
func TestInterceptFileSymbols_Gate(t *testing.T) {
	// query without file_symbols mode → not claimed.
	handled, _ := InterceptFileSymbols(opCtx(), nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"stats"}`)})
	assert.False(t, handled)
	// standalone tool with no path → claimed but errors.
	handled, res := InterceptFileSymbols(opCtx(), &fsGateDeps{}, kgtools.CallToolParams{Name: "file_symbols", Arguments: json.RawMessage(`{}`)})
	assert.True(t, handled)
	assert.True(t, res.IsError)
}

// fsGateDeps is a minimal ClientDeps stub for the gate test (no GraphClient
// needed — the path-required error fires first).
type fsGateDeps struct{ ClientDeps }
