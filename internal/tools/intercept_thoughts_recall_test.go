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
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

func TestValidateRecallClientArgs_OutOfRange(t *testing.T) {
	vMin := -2.0
	a := recallClientArgs{ValenceMin: &vMin}
	msg := validateRecallClientArgs(a)
	require.NotEmpty(t, msg)
	assert.Contains(t, msg, "valence_min")
}

func TestValidateRecallClientArgs_SwappedBounds(t *testing.T) {
	vMin := 0.8
	vMax := 0.2
	a := recallClientArgs{ValenceMin: &vMin, ValenceMax: &vMax}
	msg := validateRecallClientArgs(a)
	require.NotEmpty(t, msg)
	assert.Contains(t, msg, "swapped")
}

func TestHandleRecallClient_GraphClientUnavailable(t *testing.T) {
	// GraphCaller() (was GraphClient()) returns nil → unavailable.
	// interceptTestDeps.GraphCaller() reads d.gc; leaving it unset / nil
	// triggers the nil-guard.
	deps := interceptTestDeps{gc: nil}
	res := handleRecallClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"recall","query":"q"}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "graph client unavailable")
}

func TestHandleRecallClient_InvalidTimeStart(t *testing.T) {
	// Pass a bad date format — handler should reject before reaching gc.
	// But because GraphClient is nil, the validation must fire AFTER the
	// nil check. Re-order test: pass a valid graph client stub? We do
	// not have one. Use BadDate AND empty GraphClient — the error will
	// come from gc nil first. That's fine for this test; the date
	// validation is exercised in the next test which uses a stub.
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	res := handleRecallClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"recall","query":"q","time_start":"not-a-date"}`),
	})
	require.True(t, res.IsError)
}

// recallClampCorpus builds n thought nodes and the seeded (id → node body) map
// the fake's bulk hydrate serves them from, so a recall gather can return more
// candidates than the declared maximum.
func recallClampCorpus(t *testing.T, n int) ([]*knowledgev1.Node, map[string]kgtools.ToolResult) {
	t.Helper()
	nodes := make([]*knowledgev1.Node, 0, n)
	seeded := make(map[string]kgtools.ToolResult, n)
	for i := range n {
		id := fmt.Sprintf("clamp-thought-%03d", i)
		node := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), SymbolName: id}
		nodes = append(nodes, node)
		body, err := json.Marshal(node)
		require.NoError(t, err)
		seeded[id] = kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(body)}}}
	}
	return nodes, seeded
}

// TestHandleRecallClient_LimitClampedToDeclaredMax covers the recall QUERY path:
// a caller limit of 200 yields exactly the declared 50, and the clamp is
// disclosed in BOTH render formats. The table is over Format on purpose —
// neither the text nor the json render may hide the clamp, and a disclosure
// wired into one branch only is exactly the shape this catches.
func TestHandleRecallClient_LimitClampedToDeclaredMax(t *testing.T) {
	nodes, seeded := recallClampCorpus(t, 60)
	hits := make([]searchengine.Hit, 0, len(nodes))
	for i, n := range nodes {
		hits = append(hits, searchengine.Hit{ID: n.Id, Score: float64(len(nodes)-i) / float64(len(nodes))})
	}

	newDeps := func() interceptTestDeps {
		return interceptTestDeps{
			gc:       &fakeGraphCaller{queryResponses: seeded},
			searcher: &fakeSegmentSearcher{hits: hits},
		}
	}

	t.Run("text render discloses the clamp", func(t *testing.T) {
		res := handleRecallClient(context.Background(), newDeps(), kgtools.CallToolParams{
			Name:      "thoughts",
			Arguments: json.RawMessage(`{"operation":"recall","query":"q","limit":200}`),
		})
		require.False(t, res.IsError, toolResultText(res))
		text := toolResultText(res)
		assert.Contains(t, text, "Found 50 thoughts:", "the caller's 200 must be clamped to the declared 50")
		assert.Contains(t, text, "the declared `limit` maximum of 50 engaged")
	})

	t.Run("json render discloses the clamp", func(t *testing.T) {
		res := handleRecallClient(context.Background(), newDeps(), kgtools.CallToolParams{
			Name:      "thoughts",
			Arguments: json.RawMessage(`{"operation":"recall","query":"q","limit":200,"format":"json"}`),
		})
		require.False(t, res.IsError, toolResultText(res))
		var payload struct {
			Total        int  `json:"total"`
			LimitClamped bool `json:"limit_clamped"`
		}
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &payload))
		assert.Equal(t, 50, payload.Total)
		assert.True(t, payload.LimitClamped, "the json branch must not ship the clamp silently")
	})
}

// TestHandleRecallClient_BareRecallLimitClampedToDeclaredMax is the catcher for
// a branch-scoped clamp. handleRecallClient builds its RecallOptions before the
// query branch, and the query path owns the rerank trim — so a clamp wired only
// into that trim leaves BARE recall passing the caller's 200 straight through to
// RecallThoughts. This is the only assertion that fails in that arrangement.
func TestHandleRecallClient_BareRecallLimitClampedToDeclaredMax(t *testing.T) {
	nodes, _ := recallClampCorpus(t, 60)
	fc := &fakeGraphCaller{
		nodeMatchResults: map[graphKey][]*knowledgev1.Node{{Type: "knowledge"}: nodes},
	}

	res := handleRecallClient(context.Background(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"recall","limit":200}`),
	})

	require.False(t, res.IsError, toolResultText(res))
	text := toolResultText(res)
	assert.Contains(t, text, "Found 50 thoughts:", "bare recall must honor the declared maximum too")
	assert.Contains(t, text, "the declared `limit` maximum of 50 engaged")
}
