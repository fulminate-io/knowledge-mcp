// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// listGraphsCaller is a scripted Execute-capable GraphCaller for repo-resolver
// tests. The resolver now enumerates code graphs via the generic Execute seam
// (RETURN_MODE_GRAPH_NAMES over the code GraphType, fetchGraphNamesOfType), so
// the fake serves the graph_names_json carrier for the requested graph type and
// records each Execute. graphsByType keys store.GraphInfo by graph type — a
// per-type query returns only that type's graphs (the per-type query is what
// makes the old code-only filter implicit).
type listGraphsCaller struct {
	mu           sync.Mutex
	callCount    atomic.Int32
	graphsByType map[string][]*knowledgev1.GraphInfo
	callErr      error
}

func newListGraphsCaller(graphsByType map[string][]*knowledgev1.GraphInfo) *listGraphsCaller {
	return &listGraphsCaller{graphsByType: graphsByType}
}

// Call is unused by the repointed resolver but kept to satisfy GraphCaller; any
// invocation is an unexpected-path failure.
func (l *listGraphsCaller) Call(_ context.Context, tool string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, errors.New("unexpected Call: " + tool)
}

func (l *listGraphsCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	l.callCount.Add(1)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.callErr != nil {
		return nil, l.callErr
	}
	gt := req.GetTarget().GetGraph()
	infos := l.graphsByType[gt]
	return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
}

// codeGraphs is a convenience builder for the common case: a set of code-graph
// names keyed under the "code" graph type.
func codeGraphs(names ...string) map[string][]*knowledgev1.GraphInfo {
	infos := make([]*knowledgev1.GraphInfo, len(names))
	for i, n := range names {
		infos[i] = &knowledgev1.GraphInfo{Name: n}
	}
	return map[string][]*knowledgev1.GraphInfo{"code": infos}
}

func TestResolveCwd_BasenameMatch(t *testing.T) {
	gc := newListGraphsCaller(codeGraphs("knowledge", "agent"))
	r := NewRepoResolver(gc)

	name, ok, err := r.ResolveCwd(context.Background(), "/Users/jonathan/code/knowledge")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "knowledge", name)
}

func TestResolveCwd_NoMatch(t *testing.T) {
	gc := newListGraphsCaller(codeGraphs("knowledge"))
	r := NewRepoResolver(gc)

	name, ok, err := r.ResolveCwd(context.Background(), "/tmp/unknown-project")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, name)
}

func TestResolveCwd_SubdirectorySuffixMatch(t *testing.T) {
	gc := newListGraphsCaller(codeGraphs("knowledge"))
	r := NewRepoResolver(gc)

	// cwd is a subdirectory of /Users/jonathan/code/knowledge — basename
	// is "tools" which doesn't match, so the second pass picks it up via
	// "/knowledge/" path-component match.
	name, ok, err := r.ResolveCwd(context.Background(), "/Users/jonathan/code/knowledge/cmd/knowledge/internal/tools")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "knowledge", name)
}

func TestResolveCwd_BareSuffixMatch(t *testing.T) {
	gc := newListGraphsCaller(codeGraphs("knowledge"))
	r := NewRepoResolver(gc)

	// cwd ends with /knowledge — both basename equality AND suffix match
	// fire; basename hits first. Use a different cwd shape to force the
	// suffix branch: cwd with name as final path component but basename
	// is a different string is impossible — basename returns the final
	// path component. Just verify the obvious-path case lands.
	name, ok, err := r.ResolveCwd(context.Background(), "/srv/repos/knowledge")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "knowledge", name)
}

func TestResolveCwd_LoadErrorReturnedOnlyOnFirstCall(t *testing.T) {
	wantErr := errors.New("connection refused")
	gc := &listGraphsCaller{callErr: wantErr}
	r := NewRepoResolver(gc)

	// First call returns the underlying error (wrapped).
	_, _, err1 := r.ResolveCwd(context.Background(), "/some/cwd")
	require.Error(t, err1)
	require.ErrorIs(t, err1, wantErr)

	// Second call returns the cached error (same wrapped form) WITHOUT
	// re-firing the RPC — sync.Once semantics.
	_, _, err2 := r.ResolveCwd(context.Background(), "/some/cwd")
	require.Error(t, err2)
	require.ErrorIs(t, err2, wantErr)

	assert.Equal(t, int32(1), gc.callCount.Load(), "Once.Do should fire Execute exactly once")
}

func TestResolveCwd_EmptyCwd(t *testing.T) {
	gc := newListGraphsCaller(codeGraphs("knowledge"))
	r := NewRepoResolver(gc)

	name, ok, err := r.ResolveCwd(context.Background(), "")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, name)
}

func TestResolveCwd_FiltersNonCodeGraphs(t *testing.T) {
	// The resolver issues a per-type RETURN_MODE_GRAPH_NAMES read over the code
	// type only — knowledge/logs graphs are never returned by that query, so the
	// old code-only filter is now implicit. Seed only non-code types: the code
	// query yields nothing, so a "knowledge" basename cannot match.
	gc := newListGraphsCaller(map[string][]*knowledgev1.GraphInfo{
		"knowledge": {{Name: "knowledge"}},
		"logs":      {{Name: "queryid-1"}},
	})
	r := NewRepoResolver(gc)

	name, ok, err := r.ResolveCwd(context.Background(), "/Users/jonathan/code/knowledge")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, name)
}

func TestResolveCwd_ConcurrentCallsFireOnce(t *testing.T) {
	gc := newListGraphsCaller(codeGraphs("knowledge"))
	r := NewRepoResolver(gc)

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	results := make([]bool, N)
	for i := range N {
		go func(i int) {
			defer wg.Done()
			_, ok, err := r.ResolveCwd(context.Background(), "/Users/jonathan/code/knowledge")
			if err == nil {
				results[i] = ok
			}
		}(i)
	}
	wg.Wait()

	for i, ok := range results {
		assert.True(t, ok, "goroutine %d did not see a match", i)
	}
	assert.Equal(t, int32(1), gc.callCount.Load(), "sync.Once: Execute must fire exactly once across 100 parallel ResolveCwd calls")
}

func TestResolveCwd_NilGraphCaller(t *testing.T) {
	r := NewRepoResolver(nil)
	_, _, err := r.ResolveCwd(context.Background(), "/anywhere")
	require.Error(t, err)
}
