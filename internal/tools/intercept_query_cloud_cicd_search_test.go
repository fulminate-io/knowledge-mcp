// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
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

func queryParams(t *testing.T, args map[string]any) kgtools.CallToolParams {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: "query", Arguments: raw}
}

// TestComposeResourceSearch_JSONAndText covers the JSON contract for the shared resource
// composer (cloud + cicd): format:"json" parses to the SearchJSONResponse envelope
// with resource_type carried through metadata; the no-format run stays on the
// RenderResourceSearch markdown path.
func TestComposeResourceSearch_JSONAndText(t *testing.T) {
	for _, tc := range []struct {
		graph string
		gt    kgtypes.GraphType
	}{
		{"cloud", kgtypes.GraphCloud},
		{"cicd", kgtypes.GraphCICD},
	} {
		t.Run(tc.graph, func(t *testing.T) {
			seed := func() *interceptDeps {
				var execHits, embedCalls atomic.Int64
				// The hydrate ids[] read returns one resource node carrying resource_type.
				gc := newInterceptHarness(t, &execHits, cannedNodesResp(
					&knowledgev1.Node{
						Id:         "res-1",
						SymbolName: "my-bucket",
						Type:       "cloud_resource",
						Metadata:   map[string]string{"resource_type": "s3:bucket"},
					},
				))
				mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "res-1", Score: 0.8}}}
				return &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}
			}

			handled, jsonOut := InterceptQueryCloudCICD(seed(), queryParams(t, map[string]any{
				"graph":   tc.graph,
				"account": "acct",
				"text":    "bucket",
				"format":  "json",
			}))
			require.True(t, handled)
			require.False(t, jsonOut.IsError, engine.FirstTextContent(jsonOut))
			var env engine.SearchJSONResponse
			require.NoError(t, json.Unmarshal([]byte(engine.FirstTextContent(jsonOut)), &env), "json branch must parse to SearchJSONResponse")
			require.Equal(t, 1, env.Total)
			require.Len(t, env.Results, 1)
			assert.Equal(t, "res-1", env.Results[0].ID)
			assert.Equal(t, "s3:bucket", env.Results[0].Metadata["resource_type"], "resource_type rides through metadata")

			_, textOut := InterceptQueryCloudCICD(seed(), queryParams(t, map[string]any{
				"graph":   tc.graph,
				"account": "acct",
				"text":    "bucket",
			}))
			body := engine.FirstTextContent(textOut)
			assert.Contains(t, body, "my-bucket", "text path renders the resource markdown")
			var env2 engine.SearchJSONResponse
			assert.Error(t, json.Unmarshal([]byte(body), &env2), "text path must not emit JSON")
		})
	}
}

// TestInterceptQueryCloudCICDSearch_EmptyAccountZeroResults is Phase 1 Step 2's
// criterion (B): a cloud/cicd ranked search against an EMPTY/un-collected account
// drives the CLIENT engine (Manager.Search → RRF → hydrate → RenderResourceSearch)
// and renders zero results CLEANLY — never an error and never a server
// RETURN_MODE_SEARCH dispatch.
func TestInterceptQueryCloudCICDSearch_EmptyAccountZeroResults(t *testing.T) {
	for _, tc := range []struct {
		graph string
		gt    kgtypes.GraphType
	}{
		{"cloud", kgtypes.GraphCloud},
		{"cicd", kgtypes.GraphCICD},
	} {
		t.Run(tc.graph, func(t *testing.T) {
			var execHits, embedCalls atomic.Int64
			// Empty account → the segment engine has no segments → zero hits. The
			// hydrate read (if any) returns no nodes.
			gc, handler := newInterceptHarnessWithHandler(t, &execHits, &knowledgev1.ExecuteResponse{})
			mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
			deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

			handled, out := InterceptQueryCloudCICD(deps, queryParams(t, map[string]any{
				"graph":   tc.graph,
				"account": "empty-account",
				"text":    "anything",
			}))
			require.True(t, handled)
			require.False(t, out.IsError, "empty account renders zero results, not an error: %v", engine.FirstTextContent(out))

			// The CLIENT engine ran against the right graph + account.
			require.Equal(t, int64(1), mgr.calls.Load(), "Manager.Search drove the cloud/cicd arm")
			require.Equal(t, tc.gt, mgr.lastGT)
			require.Equal(t, "empty-account", mgr.lastName)

			// No SERVER search dispatch — the arm is fully client-side.
			require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
				"cloud/cicd search must NOT dispatch a server search")

			// Zero-result render (scored resource renderer), graceful.
			body := engine.FirstTextContent(out)
			assert.Contains(t, body, "0 results")
		})
	}
}
