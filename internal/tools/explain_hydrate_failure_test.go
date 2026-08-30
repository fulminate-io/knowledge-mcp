// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// explain_hydrate_failure_test.go covers the endpoint-hydrate failure of the
// explain arm. renderExplainWithNames used to wrap that read in
// `if err == nil { ... }`, so a broken hydrate rendered EVERY endpoint under the
// truncated-id fallback name explainEndpointName produces — a render
// indistinguishable from one whose peers genuinely have no SymbolName.
//
// THE THREE LEGS ARE ONE TEST ON PURPOSE. A failure assertion alone is satisfied
// by an arm that errors on every hydrate; a success assertion alone by the
// swallowing implementation this fix removes; and neither can tell a FAILED read
// from a read that legitimately resolved nothing. The unresolved-peer leg is what
// pins the split the fix is about.

// TestRenderExplainWithNames_HydrateFailureIsLoud drives the three legs against
// the same edge set, changing only what the hydrate does.
func TestRenderExplainWithNames_HydrateFailureIsLoud(t *testing.T) {
	edges := []knowledgev1.Edge{{FromId: "endpoint-alpha-000001", ToId: "endpoint-bravo-000002", Type: "relates-to"}}

	t.Run("a failed hydrate is an error, not a nameless render", func(t *testing.T) {
		exec := func(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			return nil, errors.New("connection reset by peer")
		}
		res := renderExplainWithNames(opCtx(), exec, nil, "knowledge", edges)
		require.True(t, res.IsError,
			"a hydrate that failed must not be rendered as an ordinary explain body")
		body := textBodyTools(res)
		assert.Contains(t, body, "explain endpoint hydrate failed")
		assert.Contains(t, body, "connection reset by peer",
			"the cause is named, so a reader can tell a broken read from an empty one")
		assert.NotContains(t, body, "## Explain",
			"the swallowing implementation rendered the explain body anyway — with every endpoint "+
				"under its truncated-id fallback name")
	})

	t.Run("known-positive: a whole hydrate renders the real names", func(t *testing.T) {
		exec := func(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{
				{Id: "endpoint-alpha-000001", SymbolName: "Alpha"},
				{Id: "endpoint-bravo-000002", SymbolName: "Bravo"},
			}}, nil
		}
		res := renderExplainWithNames(opCtx(), exec, nil, "knowledge", edges)
		require.False(t, res.IsError, textBodyTools(res))
		assert.Contains(t, textBodyTools(res), "Alpha -> Bravo",
			"without this leg the failure assertion is satisfied by an arm that errors unconditionally")
	})

	t.Run("an unresolved endpoint is an ordinary answer, still rendered", func(t *testing.T) {
		// THE SPLIT THIS FIX IS ABOUT. The read SUCCEEDS and simply returns no row
		// for one endpoint — a peer that was deleted, or one the caller cannot see.
		// That is not a fault and must still render, under the fallback name.
		exec := func(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{
				{Id: "endpoint-alpha-000001", SymbolName: "Alpha"},
			}}, nil
		}
		res := renderExplainWithNames(opCtx(), exec, nil, "knowledge", edges)
		require.False(t, res.IsError,
			"a successful read that resolved fewer rows than ids is NOT a failure")
		body := textBodyTools(res)
		assert.Contains(t, body, "Alpha -> endpoint-bravo-00000",
			"the unresolved endpoint keeps explainEndpointName's 20-char id fallback")
	})
}
