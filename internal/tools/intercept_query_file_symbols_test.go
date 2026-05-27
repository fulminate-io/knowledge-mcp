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

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// fsFake routes Execute for the file_symbols recipe: file ByID, CONTAINS-forward
// IDs, bulk ids[] hydrate, and the Match-empty suffix fallback. Counts bulk
// hydrates so the no-per-symbol-N+1 invariant can be asserted.
type fsFake struct {
	fileNode    knowledgev1.Node
	symIDs      []string
	symbols     []knowledgev1.Node
	allNodes    []knowledgev1.Node // for the suffix fallback
	bulkHyd     int
	useFallback bool
}

func (f *fsFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	switch {
	case q.GetById() != "":
		if f.useFallback {
			return &knowledgev1.ExecuteResponse{}, nil // file ByID misses → fallback.
		}
		resp := enginetest.ResponseWithNodes([]*knowledgev1.Node{&f.fileNode}...)
		return resp, nil
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_IDS:
		return &knowledgev1.ExecuteResponse{Ids: f.symIDs}, nil
	case len(q.GetIds()) > 0:
		f.bulkHyd++
		resp := enginetest.ResponseWithNodes(nodePtrs(f.symbols)...)
		return resp, nil
	default:
		// Match-empty (suffix fallback).
		resp := enginetest.ResponseWithNodes(nodePtrs(f.allNodes)...)
		return resp, nil
	}
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

// TestComposeFileSymbols_SuffixFallback asserts the Match-empty + suffix filter
// when the direct file-id ByID misses.
func TestComposeFileSymbols_SuffixFallback(t *testing.T) {
	f := &fsFake{
		useFallback: true,
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

// TestInterceptFileSymbols_Gate asserts the standalone tool + query-mode claims.
func TestInterceptFileSymbols_Gate(t *testing.T) {
	// query without file_symbols mode → not claimed.
	handled, _ := InterceptFileSymbols(nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"stats"}`)})
	assert.False(t, handled)
	// standalone tool with no path → claimed but errors.
	handled, res := InterceptFileSymbols(&fsGateDeps{}, kgtools.CallToolParams{Name: "file_symbols", Arguments: json.RawMessage(`{}`)})
	assert.True(t, handled)
	assert.True(t, res.IsError)
}

// fsGateDeps is a minimal ClientDeps stub for the gate test (no GraphClient
// needed — the path-required error fires first).
type fsGateDeps struct{ ClientDeps }
