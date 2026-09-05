// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestInterceptSearch_RetiredTransformersRefused pins the SEARCH rail's answer
// for a graph family that was removed.
//
// THE DEFECT IT DETECTS IS A WRONG ANSWER, NOT A MISSING ONE. Once the family is
// removed, kgtypes.IsBuiltinGraphType stops claiming its name, so the payload
// falls past the reducible-graph switch into the registered-custom default
// branch — which claims it and answers "transformers search: client segment
// engine unavailable". That is a message about a missing INDEX for a graph type
// that no longer exists, and it sends a caller looking for a collection problem
// they will never find. The refusal has to name the REMOVAL instead, and it has
// to fire ahead of the default branch to do so.
//
// BOTH EMBED STATES ARE DRIVEN, on the same reasoning the retired sibling test
// used: search.go's `!hasRewrite && !didEmbed && !claimKnowledge` guard sits
// AFTER embedKnowledgeQuery, so a no-embed payload and an embed-resolved payload
// reach the arm down different paths and used to fail differently. A single row
// would leave the other path unpinned.
func TestInterceptSearch_RetiredTransformersRefused(t *testing.T) {
	for _, emb := range []struct {
		name    string
		withEmb bool
	}{{"no-embed-identity", false}, {"embed-identity-resolved", true}} {
		t.Run(emb.name, func(t *testing.T) {
			var execHits, embedCalls atomic.Int64
			resp := &knowledgev1.ExecuteResponse{}
			if emb.withEmb {
				resp = cannedEmbeddedNodesResp()
			}
			gc, handler := newInterceptHarnessWithHandler(t, &execHits, resp)
			mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
			deps := &interceptDeps{gc: gc, segMgr: mgr}
			if emb.withEmb {
				deps.emb = stubEmbedder{calls: &embedCalls}
			}

			handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
				"graph": "transformers", "query": "x",
			}))
			body := engine.FirstTextContent(out)

			require.True(t, handled,
				"the retired name must be CLAIMED by the client, not left to fall through to the server")
			assert.Contains(t, body, "transformers", "the refusal names the value it rejected")
			assert.Contains(t, body, "retired", "the refusal says the family is retired")
			assert.Contains(t, body, "removed", "and names the REMOVAL rather than an index problem")

			// THE MISDIAGNOSIS THIS EXISTS TO PREVENT. Reaching the
			// registered-custom branch answers with the segment-engine message,
			// which is measurably what happens without the refusal above it.
			assert.NotContains(t, strings.ToLower(body), "segment engine",
				"a removed family is not an index outage: the segment-engine wording would send the caller after a collection problem that does not exist")

			// Nothing reached the wire and nothing reached the index.
			require.Equal(t, int64(0), mgr.calls.Load(), "the refusal drives no client segment engine")
			require.Empty(t, handler.recordedReqs(), "the refusal dispatches no RPC at all")
			require.Zero(t, execHits.Load(), "the refusal costs no read")
		})
	}

	// THE CONTROL. A never-existing name is UNKNOWN, not retired, and must not
	// pick up the removal wording — otherwise every typo would be reported as a
	// family that used to exist. Same rail, same harness, same call shape.
	t.Run("a name that was never a builtin is not reported as retired", func(t *testing.T) {
		var execHits atomic.Int64
		gc, _ := newInterceptHarnessWithHandler(t, &execHits, &knowledgev1.ExecuteResponse{})
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
		deps := &interceptDeps{gc: gc, segMgr: mgr}

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "transformerz", "query": "x",
		}))
		body := engine.FirstTextContent(out)

		require.True(t, handled, "a non-builtin name is still claimed by the custom-graph branch")
		assert.NotContains(t, body, "retired",
			"an unknown name is a typo; calling it retired asserts a history it does not have")
	})
}
