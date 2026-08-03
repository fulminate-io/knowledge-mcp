// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestRender_TruncationNotice pins that the server's truncation flag actually
// REACHES THE CALLER. A wire field nothing reads is not a notice, and two
// decisions on the server side rest on this arriving: the id-set bound is served
// rather than rejected on the grounds that the caller is told, and the
// exactly-at-ceiling caveat is only defensible if a caller can see the flag and
// re-page. If this stops working, both of those lose their justification.
func TestRender_TruncationNotice(t *testing.T) {
	// A small node carrier — enough for the renderers to produce a result.
	nodes := []*knowledgev1.Node{
		{Id: "n1", Type: "plan", SymbolName: "n1"},
		{Id: "n2", Type: "plan", SymbolName: "n2"},
	}
	args := json.RawMessage(`{"type":"plan"}`)
	jsonArgs := json.RawMessage(`{"type":"plan","format":"json"}`)

	t.Run("truncated_response_appends_one_notice_block", func(t *testing.T) {
		plain, err := Render("query", args, &knowledgev1.ExecuteResponse{Nodes: nodes})
		require.NoError(t, err)

		got, err := Render("query", args, &knowledgev1.ExecuteResponse{Nodes: nodes, Truncated: true})
		require.NoError(t, err)

		require.Len(t, got.Content, len(plain.Content)+1,
			"a truncated response gains exactly ONE block")
		notice := got.Content[len(got.Content)-1].Text
		assert.Contains(t, notice, "2", "the notice names the row count")
		assert.Contains(t, notice, "limit",
			"the notice names the `limit` parameter so a reader maps the advice onto it")
	})

	t.Run("untruncated_response_appends_nothing", func(t *testing.T) {
		// The guard against an UNCONDITIONAL notice. Before the wiring this passed
		// for the trivial reason that nothing appended at all; afterwards it is the
		// only thing keeping the notice off every complete response.
		plain, err := Render("query", args, &knowledgev1.ExecuteResponse{Nodes: nodes})
		require.NoError(t, err)
		for _, b := range plain.Content {
			assert.NotContains(t, b.Text, "server row ceiling",
				"a complete result carries no truncation notice")
		}
	})

	t.Run("json_payload_block_stays_valid_json", func(t *testing.T) {
		// The gate on appending as a SEPARATE block rather than concatenating: the
		// payload block must survive the notice intact. If this ever fails, the fix
		// is to skip the notice for format=json — never to weaken this assertion.
		got, err := Render("query", jsonArgs, &knowledgev1.ExecuteResponse{Nodes: nodes, Truncated: true})
		require.NoError(t, err)
		require.NotEmpty(t, got.Content)

		var payload any
		require.NoError(t, json.Unmarshal([]byte(got.Content[0].Text), &payload),
			"the first block must still parse as JSON with the notice appended")
		assert.Contains(t, got.Content[len(got.Content)-1].Text, "limit",
			"the notice rides in its own trailing block")
	})
}
