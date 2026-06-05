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
