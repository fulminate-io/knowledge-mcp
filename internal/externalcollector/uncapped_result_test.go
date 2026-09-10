// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uncapped_result_test.go — COLLECTOR TRAFFIC CARRIES NO SIZE CAP, on the result
// side, over both transports.
//
// WHAT WAS REMOVED AND WHY IT IS NOT REPLACED. This package used to bound a
// provider result at 64 MiB, in two places: an admission bound in DecodeResult
// that every transport's result passed through, and a response-body bound on the
// http round tripper. Neither remains. The traffic is MCP to MCP and none of it
// reaches a model context, so the reason a cap exists on a rendered surface does
// not apply here; the declared-only context block and a module's own reads are
// what size a collect.
//
// THE ROWS BELOW SIZE A PAYLOAD OVER THE FORMER BOUND DIRECTLY. The retired
// tests reached their boundary by lowering the cap, which is no longer a
// technique available: there is no cap to lower, so the only way to prove the
// former bound does not apply is to cross it with real bytes.
//
// THE MUTATION THESE ROWS EXIST FOR: reintroduce the bound. Both go red.

// formerResultCap is the bound this package used to enforce. It is a LOCAL
// TEST CONSTANT rather than a production one — the production constant is gone,
// and reintroducing it is the mutation these rows are written to catch.
const formerResultCap = 64 << 20

// overCapPayload builds a conforming collector result whose marshaled JSON is
// larger than the former 64 MiB bound, and returns it with its exact size and
// its node and edge counts, so a caller asserts on what it actually sent.
func overCapPayload(t *testing.T) (payload map[string]any, size int, nodes, edges int) {
	t.Helper()
	const (
		bodyLen  = 1 << 20 // 1 MiB per node body.
		nodeRows = 68      // 68 MiB of bodies, comfortably over 64 MiB.
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
	payload = map[string]any{"nodes": nodeList, "edges": edgeList, "walk_complete": true}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Greater(t, len(raw), formerResultCap,
		"fixture control: the payload must actually cross the former bound, or this row proves nothing")
	return payload, len(raw), len(nodeList), len(edgeList)
}

// TestDecodeResult_AcceptsAResultOverTheFormerCapWhole is the admission arm every
// transport's result passes through. It asserts the result is DECODED WHOLE —
// every node and every edge present — rather than merely that no error came
// back, because a path that admitted the bytes and dropped them would satisfy
// the weaker claim.
func TestDecodeResult_AcceptsAResultOverTheFormerCapWhole(t *testing.T) {
	payload, size, wantNodes, wantEdges := overCapPayload(t)
	t.Logf("payload is %d bytes, %d over the former %d-byte bound", size, size-formerResultCap, formerResultCap)

	res, err := DecodeResult("collect_graph", payload)
	require.NoError(t, err, "collector traffic carries no size cap")
	require.NotNil(t, res)
	assert.Len(t, res.Nodes, wantNodes, "accepted WHOLE: every node the provider emitted is here")
	assert.Len(t, res.Edges, wantEdges)
	assert.True(t, res.WalkComplete)
}

// TestRunMCP_AcceptsAResultOverTheFormerCapOverHTTP drives the same payload
// through the http transport end to end, which is where the response-body bound
// used to live. The two bounds were separate code, so removing one and not the
// other would leave this arm refusing while the arm above passed.
func TestRunMCP_AcceptsAResultOverTheFormerCapOverHTTP(t *testing.T) {
	payload, size, wantNodes, wantEdges := overCapPayload(t)
	t.Logf("payload is %d bytes over http", size)

	url, _ := startHTTPStubPayload(t, payload)
	res, _, err := RunMCP(context.Background(), httpDef(url), nil, "board", nil)
	require.NoError(t, err, "the http transport bounds no response body")
	require.NotNil(t, res)
	assert.Len(t, res.Nodes, wantNodes)
	assert.Len(t, res.Edges, wantEdges)
}

// TestRunMCP_AcceptsAResultOverTheFormerCapOverStdio is the same claim on the
// other transport, whose framing the SDK owns and whose result the retired
// admission bound was the only thing standing in front of.
func TestRunMCP_AcceptsAResultOverTheFormerCapOverStdio(t *testing.T) {
	_, size, wantNodes, wantEdges := overCapPayload(t)
	t.Logf("stdio child emits %d bytes", size)

	res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeOverFormerCap), nil, "board", nil)
	require.NoError(t, err, "the stdio arm admits a result over the former bound")
	require.NotNil(t, res)
	assert.Len(t, res.Nodes, wantNodes)
	assert.Len(t, res.Edges, wantEdges)
}

// startHTTPStubPayload stands an http provider up serving a CALLER-SUPPLIED
// payload, for the rows whose subject is the payload's size rather than a stub
// mode's behavior. The inbound request-body bound is disabled so the row
// measures the RESULT side; there is nothing large on the argument side here.
func startHTTPStubPayload(t *testing.T, payload any) (url string, headers *headerRecorder) {
	t.Helper()
	headers = &headerRecorder{}
	server := mcp.NewServer(&mcp.Implementation{Name: "ful1776-stub", Version: "v1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:         defaultStubTool,
		Description:  "stub custom collector",
		InputSchema:  contractSchemaMap(InputContractJSON()),
		OutputSchema: contractSchemaMap(OutputContractJSON()),
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: payload}, nil
	})
	// The describe tool is REQUIRED of every provider, so this stub serves it
	// too: without it every row here would fail on the missing tool rather than
	// on the property it is about.
	server.AddTool(&mcp.Tool{
		Name:         DescribeToolName,
		Description:  "stub declaration",
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: contractSchemaMap(DescribeContractJSON()),
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: stubDeclarationDocument()}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	srv := httptest.NewServer(headers.wrap(handler))
	t.Cleanup(func() {
		for ss := range server.Sessions() {
			_ = ss.Close()
		}
		srv.Close()
	})
	return srv.URL, headers
}
