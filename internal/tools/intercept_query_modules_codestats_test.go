// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// modFake routes Match(package)/Match(file) Executes + the Stats RPC for the
// list_modules + code-stats composers.
type modFake struct {
	packages   []knowledgev1.Node
	files      []knowledgev1.Node
	matchCalls int
	stats      *knowledgev1.GraphStats
}

func (f *modFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.matchCalls++
	nt := req.GetQuery().GetSelection().GetNodeType()
	var nodes []knowledgev1.Node
	switch kgtypes.NodeType(nt) {
	case kgtypes.NodePackage:
		nodes = f.packages
	case kgtypes.NodeFile:
		nodes = f.files
	}
	resp := enginetest.ResponseWithNodes(nodePtrs(nodes)...)
	return resp, nil
}

func (f *modFake) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{GraphStats: f.stats}, nil
}

// TestComposeListModules asserts the two-Match + rollup recipe (no N+1) and the
// longest-prefix file-count attribution.
func TestComposeListModules(t *testing.T) {
	f := &modFake{
		packages: []knowledgev1.Node{
			{Id: "pkg/a", Type: string(kgtypes.NodePackage), Summary: "package a"},
			{Id: "pkg/a/sub", Type: string(kgtypes.NodePackage)},
		},
		files: []knowledgev1.Node{
			{Id: "pkg/a/foo.go", Type: string(kgtypes.NodeFile), FilePath: "pkg/a/foo.go"},
			{Id: "pkg/a/sub/bar.go", Type: string(kgtypes.NodeFile), FilePath: "pkg/a/sub/bar.go"},
		},
	}
	res := composeListModules(context.Background(), nil, f.Execute, modulesCodeStatsArgs{Graph: "code", Mode: "modules", Repo: "knowledge"})
	require.False(t, res.IsError, textBodyTools(res))
	body := textBodyTools(res)
	assert.Equal(t, 2, f.matchCalls, "exactly two Match Executes (package + file), no N+1")
	assert.Contains(t, body, "[knowledge]")
	assert.Contains(t, body, "### Modules (2)")
	// foo.go attributes to pkg/a; bar.go attributes to the longer pkg/a/sub.
	assert.Contains(t, body, "**pkg/a** (1 files)")
	assert.Contains(t, body, "**pkg/a/sub** (1 files)")
	assert.Contains(t, body, "package a")
}

// TestComposeCodeStats asserts the code-stats Stats RPC + RenderStatsBreakdown
// with the per-repo header.
func TestComposeCodeStats(t *testing.T) {
	f := &modFake{stats: &knowledgev1.GraphStats{
		NodeCount: 42, EdgeCount: 17,
		NodesByType: map[string]int64{"function": 30, "file": 12},
	}}
	res := composeCodeStats(context.Background(), f, modulesCodeStatsArgs{Graph: "code", Mode: "stats", Repo: "knowledge"})
	require.False(t, res.IsError, textBodyTools(res))
	body := textBodyTools(res)
	assert.Contains(t, body, "## Code Graph: knowledge")
	assert.Contains(t, body, "Nodes: 42")
	assert.Contains(t, body, "### Nodes by Type")
	assert.Contains(t, body, "- function: 30")
}

// TestComposeCodeStats_JSON asserts the format:"json" branch returns the
// structured GraphStats shape with graph=code + repo + counts + type maps,
// BEFORE the markdown render. The text path stays covered by TestComposeCodeStats.
func TestComposeCodeStats_JSON(t *testing.T) {
	f := &modFake{stats: &knowledgev1.GraphStats{
		NodeCount: 42, EdgeCount: 17, BinaryVectorCount: 8,
		NodesByType: map[string]int64{"function": 30, "file": 12},
		EdgesByType: map[string]int64{"calls": 17},
	}}
	res := composeCodeStats(context.Background(), f, modulesCodeStatsArgs{Graph: "code", Mode: "stats", Repo: "knowledge", Format: "json"})
	require.False(t, res.IsError, textBodyTools(res))

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(res)), &payload), "body must be valid JSON")
	assert.Equal(t, "code", payload["graph"])
	assert.Equal(t, "knowledge", payload["repo"])
	assert.EqualValues(t, 42, payload["node_count"])
	assert.EqualValues(t, 17, payload["edge_count"])
	assert.EqualValues(t, 8, payload["binary_vector_count"])
	nbt, ok := payload["nodes_by_type"].(map[string]any)
	require.True(t, ok, "nodes_by_type is an object")
	assert.EqualValues(t, 30, nbt["function"])
	ebt, ok := payload["edges_by_type"].(map[string]any)
	require.True(t, ok, "edges_by_type is an object")
	assert.EqualValues(t, 17, ebt["calls"])
}

// TestInterceptQueryModulesCodeStats_Gate asserts the graph=code mode gate.
func TestInterceptQueryModulesCodeStats_Gate(t *testing.T) {
	// non-code → not claimed.
	handled, _ := InterceptQueryModulesCodeStats(nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"knowledge","mode":"modules"}`)})
	assert.False(t, handled)
	// code but mode=examine → not claimed.
	handled, _ = InterceptQueryModulesCodeStats(nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"code","mode":"examine"}`)})
	assert.False(t, handled)
}
