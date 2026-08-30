// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// render_json_truncated_test.go covers the `truncated` boolean on every JSON
// envelope this ticket adds it to. It is a NEW file rather than more of
// render_misc_test.go, which has no room under the 500-line staged-file cap.
//
// EVERY TEST HERE CARRIES BOTH POLARITIES IN ONE RUN — key present and FALSE on
// an untruncated response, key present and TRUE on a truncated one. A
// single-polarity assertion cannot tell a wired field from a hardcoded literal,
// in either direction: asserting only `true` is satisfied by a constant true,
// and asserting only `false` by a constant false.
//
// THE KEY IS UNCONDITIONAL. `truncated: false` is a positive statement of
// completeness; an absent key is indistinguishable from an old binary, which is
// the inference-from-absence defect this whole ticket is about. So every
// assertion checks PRESENCE first and value second.

// envelopeOf unmarshals a rendered ToolResult's JSON body into a generic map,
// which is what lets the tests distinguish an absent key from a false one — a
// typed struct would decode both to the zero value.
func envelopeOf(t *testing.T, res kgtools.ToolResult) map[string]any {
	t.Helper()
	require.NotEmpty(t, res.Content, "the render produced no content block")
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload),
		"content[0] must be the JSON envelope: %s", res.Content[0].Text)
	return payload
}

// assertTruncatedKey pins presence and value together, in that order.
func assertTruncatedKey(t *testing.T, payload map[string]any, want bool, envelope string) {
	t.Helper()
	got, ok := payload["truncated"]
	require.True(t, ok,
		"the %s envelope carries no `truncated` key — a caller following query_schema.go cannot "+
			"tell a whole result from a clamped one, and an absent key is indistinguishable from an "+
			"old binary. Keys present: %v", envelope, keysOf(payload))
	assert.Equal(t, want, got, "the %s envelope's truncated key does not track the response verdict", envelope)
}

func keysOf(payload map[string]any) []string {
	out := make([]string, 0, len(payload))
	for k := range payload {
		out = append(out, k)
	}
	return out
}

// TestBrowseJSON_TruncatedField covers the type-browse envelope
// {graph, type, results, total, truncated} on the 10,000-row node ceiling.
func TestBrowseJSON_TruncatedField(t *testing.T) {
	nodes := []*knowledgev1.Node{{Id: "n1", SymbolName: "First"}, {Id: "n2", SymbolName: "Second"}}
	render := func(truncated bool) map[string]any {
		resp := nodesResp(t, nodes, 2)
		resp.Truncated = truncated
		out, err := renderBrowseResponse(resp, browseContext{Label: "knowledge", NodeType: "finding", Format: "json"})
		require.NoError(t, err)
		return envelopeOf(t, out)
	}

	t.Run("whole result", func(t *testing.T) {
		assertTruncatedKey(t, render(false), false, "type-browse")
	})
	t.Run("clamped result", func(t *testing.T) {
		assertTruncatedKey(t, render(true), true, "type-browse")
	})
}

// TestNodesByIDsJSON_TruncatedField covers the bulk-hydrate envelope
// {label, nodes, truncated}, on BOTH its arms. The projected arm is a separate
// return statement from the unprojected one, so covering only the second would
// ship the key on plain ids[] reads and not on fields-projected ones — the same
// two-branch trap the search envelope carries.
func TestNodesByIDsJSON_TruncatedField(t *testing.T) {
	nodes := []*knowledgev1.Node{{Id: "a", SymbolName: "A"}, {Id: "b", SymbolName: "B"}}
	render := func(truncated bool, fields []string) map[string]any {
		resp := nodesResp(t, nodes, 2)
		resp.Truncated = truncated
		out, err := renderNodesByIDsResponse(resp, "knowledge", "json", fields, false)
		require.NoError(t, err)
		return envelopeOf(t, out)
	}

	t.Run("unprojected, whole result", func(t *testing.T) {
		assertTruncatedKey(t, render(false, nil), false, "bulk-ids")
	})
	t.Run("unprojected, clamped result", func(t *testing.T) {
		assertTruncatedKey(t, render(true, nil), true, "bulk-ids")
	})
	t.Run("fields-projected, whole result", func(t *testing.T) {
		assertTruncatedKey(t, render(false, []string{"id", "name"}), false, "bulk-ids (projected)")
	})
	t.Run("fields-projected, clamped result", func(t *testing.T) {
		assertTruncatedKey(t, render(true, []string{"id", "name"}), true, "bulk-ids (projected)")
	})
}

// TestDeletePreviewJSON_TruncatedField is the VALUE gate for the delete dry-run
// envelope. Its key was present but unasserted: pinning the threaded verdict to a
// constant false left every package green, because the only test touching this
// envelope checks the keys it carries and not what they say. A "would delete N"
// that understates the real delete is the worst place in the tool for a silent
// clamp, so the value gets its own both-polarity assertion.
func TestDeletePreviewJSON_TruncatedField(t *testing.T) {
	args := json.RawMessage(`{"dry_run":true,"ids":["a","b"],"format":"json"}`)

	render := func(truncated bool) map[string]any {
		exec := func(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			return &knowledgev1.ExecuteResponse{
				Nodes:     []*knowledgev1.Node{{Id: "a", SymbolName: "A", Type: "finding"}},
				Truncated: truncated,
			}, nil
		}
		res, handled := dispatchDeletePreview(context.Background(), exec, args)
		require.True(t, handled, "a dry-run delete is the shape this seam claims")
		require.False(t, res.IsError)
		return envelopeOf(t, res)
	}

	t.Run("whole preview", func(t *testing.T) {
		assertTruncatedKey(t, render(false), false, "delete-preview")
	})
	t.Run("clamped preview", func(t *testing.T) {
		assertTruncatedKey(t, render(true), true, "delete-preview")
	})
}

// TestSearchJSON_TruncatedField covers BOTH text-search envelopes — the full
// SearchJSONResponse and renderJSONProjected's function-local projectedResponse.
// They are separate structs, so covering only the first would ship the key on
// unprojected search reads and not on fields-projected ones.
//
// THE VALUE IS FALSE, AND THAT IS TRUE BY CONSTRUCTION: the verdict would ride
// ExecuteResponse.SearchResults, which nothing server-side populates, so no
// ceiling can signal engagement on this arm. It is READ from the response rather
// than hardcoded, so the day that changes the key starts telling the truth.
//
// THE PAIRED BROWSE LEG IS THE POINT. A search-only assertion of "false" is
// satisfied by hardcoding false in EVERY envelope in the codebase; driving a
// genuinely truncated fixture through the type-browse arm in the SAME test is
// what refuses that implementation. The two legs live in one test so the pairing
// cannot be split apart later.
func TestSearchJSON_TruncatedField(t *testing.T) {
	results := []SearchResult{{Score: 0.9, Node: &knowledgev1.Node{Id: "s1", SymbolName: "Hit", Type: "finding"}}}
	searchResp := func() *knowledgev1.ExecuteResponse {
		return &knowledgev1.ExecuteResponse{SearchResults: []*knowledgev1.HydratedResult{
			{Node: results[0].Node, Score: results[0].Score},
		}}
	}

	t.Run("full envelope: key present and false", func(t *testing.T) {
		out, err := renderSearchResponse(searchResp(), "q", "json", nil,
			knowledgev1.SearchMode_SEARCH_MODE_HYBRID, "BM25-only")
		require.NoError(t, err)
		assertTruncatedKey(t, envelopeOf(t, out), false, "search")
	})

	t.Run("projected envelope: key present and false", func(t *testing.T) {
		out, err := renderSearchResponse(searchResp(), "q", "json", []string{"id", "name"},
			knowledgev1.SearchMode_SEARCH_MODE_HYBRID, "BM25-only")
		require.NoError(t, err)
		assertTruncatedKey(t, envelopeOf(t, out), false, "search (projected)")
	})

	t.Run("resource-filtered arm carries it too", func(t *testing.T) {
		// Wiring only renderSearchResponse would leave this arm without the key.
		out, err := renderSearchResponseFiltered(searchResp(), "q", "json", nil,
			knowledgev1.SearchMode_SEARCH_MODE_HYBRID, "", "BM25-only")
		require.NoError(t, err)
		assertTruncatedKey(t, envelopeOf(t, out), false, "search (resource-filtered)")
	})

	t.Run("PAIRED: a truncated browse response renders the key TRUE", func(t *testing.T) {
		// The refusal of a hardcode-false-everywhere implementation. Same process,
		// same key, a fixture that IS clamped — if this reads false, the key is a
		// constant rather than a field and every "false" above means nothing.
		resp := nodesResp(t, []*knowledgev1.Node{{Id: "n1", SymbolName: "First"}}, 1)
		resp.Truncated = true
		out, err := renderBrowseResponse(resp, browseContext{Label: "knowledge", NodeType: "finding", Format: "json"})
		require.NoError(t, err)
		assertTruncatedKey(t, envelopeOf(t, out), true, "type-browse (pairing control)")
	})
}

// TestTraversalJSON_TruncatedField covers the traversal envelope, which rides the
// 50,000-row edges ceiling — the largest truncation surface in the system.
//
// THE FIXTURE CARRIES NO CANDIDATE GROUPS ON PURPOSE. attachCandidateGroupsJSON
// emits group_reconstruction_incomplete only when a group exists, so a GROUPED
// fixture would carry an incompleteness signal from that key and prove nothing
// about this one. The ungrouped traversal — the overwhelming majority — is
// exactly the case whose payload carried no truncation signal at all.
func TestTraversalJSON_TruncatedField(t *testing.T) {
	results := []TraversalResult{
		{Distance: 0, Node: &knowledgev1.Node{Id: "n0", SymbolName: "Root", Type: "plan"}},
		{Distance: 1, Node: &knowledgev1.Node{Id: "n1", SymbolName: "Child", Type: "phase"}},
	}
	render := func(truncated bool) map[string]any {
		resp := &knowledgev1.ExecuteResponse{
			TraversalResults: traversalResultsToProtoForTest(results),
			Truncated:        truncated,
		}
		out, err := renderTraversalResponse(resp, traverseContext{
			Start: "n0", GraphName: "code", Direction: "out", Format: "json",
		})
		require.NoError(t, err)
		return envelopeOf(t, out)
	}

	t.Run("whole walk", func(t *testing.T) {
		payload := render(false)
		assertTruncatedKey(t, payload, false, "traversal")
		_, hasGroupKey := payload["group_reconstruction_incomplete"]
		require.False(t, hasGroupKey,
			"this fixture must carry NO candidate groups, or the truncation assertion is confounded "+
				"with the group-incompleteness key")
	})
	t.Run("clamped walk", func(t *testing.T) {
		payload := render(true)
		assertTruncatedKey(t, payload, true, "traversal")
		_, hasGroupKey := payload["group_reconstruction_incomplete"]
		require.False(t, hasGroupKey,
			"group_reconstruction_incomplete answers a NARROWER question and is not the truncation "+
				"signal — it must stay absent on an ungrouped walk however the ceiling behaved")
	})
}
