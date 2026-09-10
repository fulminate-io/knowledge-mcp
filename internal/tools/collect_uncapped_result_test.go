// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collect_uncapped_result_test.go — THE LANDING HALF of the removed result cap.
//
// The host package proves an over-former-bound result is DECODED whole over both
// transports. This proves it ARRIVES: the same result travels the production
// dispatch and reaches the sink with every node and every edge the provider
// emitted. A row asserting only the absence of an error would pass on a path
// that decoded the result and dropped it, which is a different bug and a worse
// one.

// formerResultCap is the bound the collector contract used to enforce, kept here
// as a local test constant because the production constant is gone. Putting it
// back is the mutation this row exists to catch.
const formerResultCap = 64 << 20

// overCapCollectPayload builds a conforming result over the former bound.
func overCapCollectPayload() (payload any, nodes, edges int) {
	const (
		bodyLen  = 1 << 20
		nodeRows = 68
	)
	body := strings.Repeat("x", bodyLen)
	nodeList := make([]any, 0, nodeRows)
	edgeList := make([]any, 0, nodeRows-1)
	for i := range nodeRows {
		nodeList = append(nodeList, map[string]any{
			"id": fmt.Sprintf("big-%d", i), "type": "blob", "content": body,
		})
		if i > 0 {
			edgeList = append(edgeList, map[string]any{
				"from_id": fmt.Sprintf("big-%d", i-1),
				"to_id":   fmt.Sprintf("big-%d", i),
				"type":    "NEXT",
			})
		}
	}
	return map[string]any{"nodes": nodeList, "edges": edgeList, "walk_complete": true},
		len(nodeList), len(edgeList)
}

// TestCustomCollect_AnOverFormerCapResultLandsWhole is R9(b)'s landing row.
func TestCustomCollect_AnOverFormerCapResultLandsWhole(t *testing.T) {
	payload, wantNodes, wantEdges := overCapCollectPayload()
	url := startCustomProvider(t, payload)
	deps := newCustomDeps(t, customDef(url))

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	shipped := deps.sink.last()
	require.NotNil(t, shipped, "a result over the former %d-byte bound must reach the sink", formerResultCap)
	assert.Len(t, shipped.Nodes, wantNodes, "landed WHOLE: every node the provider emitted")
	assert.Len(t, shipped.Edges, wantEdges, "and every edge")
}
