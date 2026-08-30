// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_query_fields_test.go drives renderQueryTool end-to-end for every JSON
// render arm to prove the tool-wide `fields` projection now reaches the LIVE
// render path. The bug these tests guard: each arm previously passed nil /
// omitted Fields, forcing full-node hydration (summary+description+content+
// metadata) regardless of the requested projection — the token-cap overflow.
//
// Each arm asserts BOTH directions: with fields=[id,name,status] the JSON
// carries id+name+status and OMITS description/content/metadata; with empty
// fields the full node is returned (no regression). A metadata.<key> projection
// is asserted to return just that key.

// queryArgsJSON marshals a queryArgs into the raw args renderQueryTool re-parses.
func queryArgsJSON(t *testing.T, a queryArgs) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(a)
	require.NoError(t, err)
	return b
}

// richNode is a node populated across every projectable field so a projection
// can be proven to OMIT the unrequested heavy fields.
func richNode(id string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id:          id,
		SymbolName:  "Rich Name",
		Type:        "finding",
		Status:      "open",
		Description: "a long description that would blow the token cap",
		Content:     "even longer content body",
		Summary:     "the summary",
		Metadata:    map[string]string{"dsl_pattern": "p1", "severity": "high"},
	}
}

func TestRenderQueryTool_TypeBrowse_FieldsProjection(t *testing.T) {
	resp := nodesResp(t, []*knowledgev1.Node{richNode("n1")}, 1)

	t.Run("projected omits heavy fields", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{Type: "finding", Format: "json", Fields: []string{"id", "name", "status"}})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		require.False(t, out.IsError)
		var payload struct {
			Results []map[string]any `json:"results"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		require.Len(t, payload.Results, 1)
		row := payload.Results[0]
		assert.Equal(t, "n1", row["id"])
		assert.Equal(t, "Rich Name", row["name"])
		assert.Equal(t, "open", row["status"])
		assert.NotContains(t, row, "description")
		assert.NotContains(t, row, "content")
		assert.NotContains(t, row, "metadata")
	})

	t.Run("empty fields returns full node (no regression)", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{Type: "finding", Format: "json"})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		var payload struct {
			Results []map[string]any `json:"results"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		require.Len(t, payload.Results, 1)
		row := payload.Results[0]
		// fullNodeJSON shape: id + name + type + status + metadata present.
		assert.Equal(t, "n1", row["id"])
		assert.Contains(t, row, "metadata")
	})

	t.Run("metadata.<key> projection returns just that key", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{Type: "finding", Format: "json", Fields: []string{"id", "metadata.dsl_pattern"}})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		var payload struct {
			Results []map[string]any `json:"results"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		row := payload.Results[0]
		assert.Equal(t, "p1", row["metadata.dsl_pattern"])
		assert.NotContains(t, row, "metadata")
		assert.NotContains(t, row, "description")
	})
}

func TestRenderQueryTool_IDsBulk_FieldsProjection(t *testing.T) {
	resp := nodesResp(t, []*knowledgev1.Node{richNode("a"), richNode("b")}, 2)

	t.Run("projected omits heavy fields", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{IDs: []string{"a", "b"}, Format: "json", Fields: []string{"id", "name", "status"}})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		var payload struct {
			Label string           `json:"label"`
			Nodes []map[string]any `json:"nodes"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		assert.Equal(t, "knowledge", payload.Label)
		require.Len(t, payload.Nodes, 2)
		for _, row := range payload.Nodes {
			assert.Contains(t, row, "id")
			assert.Contains(t, row, "name")
			assert.Contains(t, row, "status")
			assert.NotContains(t, row, "description")
			assert.NotContains(t, row, "content")
			assert.NotContains(t, row, "metadata")
		}
	})

	t.Run("empty fields returns full nodes (no regression)", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{IDs: []string{"a", "b"}, Format: "json"})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		var payload struct {
			Nodes []*knowledgev1.Node `json:"nodes"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		require.Len(t, payload.Nodes, 2)
		// Full node carries the heavy fields through the raw marshal.
		assert.Equal(t, "even longer content body", payload.Nodes[0].Content)
		assert.Equal(t, "a long description that would blow the token cap", payload.Nodes[0].Description)
	})

	t.Run("metadata.<key> projection returns just that key", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{IDs: []string{"a"}, Format: "json", Fields: []string{"id", "metadata.severity"}})
		out, err := renderQueryTool(args, nodesResp(t, []*knowledgev1.Node{richNode("a")}, 1))
		require.NoError(t, err)
		var payload struct {
			Nodes []map[string]any `json:"nodes"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		require.Len(t, payload.Nodes, 1)
		assert.Equal(t, "high", payload.Nodes[0]["metadata.severity"])
		assert.NotContains(t, payload.Nodes[0], "metadata")
	})
}

func TestRenderQueryTool_SingleID_FieldsProjection(t *testing.T) {
	t.Run("knowledge projected omits heavy fields", func(t *testing.T) {
		resp := nodeResp(t, richNode("n1"))
		args := queryArgsJSON(t, queryArgs{ID: "n1", Graph: "knowledge", Format: "json", Fields: []string{"id", "name", "status"}})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		var row map[string]any
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &row))
		assert.Equal(t, "n1", row["id"])
		assert.Equal(t, "Rich Name", row["name"])
		assert.Equal(t, "open", row["status"])
		assert.NotContains(t, row, "description")
		assert.NotContains(t, row, "content")
		assert.NotContains(t, row, "metadata")
	})

	t.Run("generic graph projected omits heavy fields", func(t *testing.T) {
		resp := nodeResp(t, richNode("n1"))
		args := queryArgsJSON(t, queryArgs{ID: "n1", Graph: "cloud", Account: "prod", Format: "json", Fields: []string{"id", "name"}})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		var row map[string]any
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &row))
		assert.Equal(t, "n1", row["id"])
		assert.Equal(t, "Rich Name", row["name"])
		assert.NotContains(t, row, "description")
		// Generic markdown body must NOT appear when projecting.
		assert.NotContains(t, out.Content[0].Text, "## cloud:prod node")
	})

	t.Run("knowledge empty fields drops nothing (no regression)", func(t *testing.T) {
		// THE PROPERTY THIS SUBTEST OWNS is that an ABSENT projection drops no
		// field — content and summary survive. It used to assert that through the
		// format:"json" read, which is no longer the right probe for it: format now
		// SELECTS the by-id render, so json returns the {node,truncated} envelope.
		// The property is asserted on both renders instead, which is strictly more
		// than it covered before.
		resp := nodeResp(t, richNode("n1"))
		args := queryArgsJSON(t, queryArgs{ID: "n1", Graph: "knowledge", Format: "json"})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		var env struct {
			Node *knowledgev1.Node `json:"node"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &env))
		require.NotNil(t, env.Node, "format:json wraps the node in the by-id envelope")
		assert.Equal(t, "n1", env.Node.GetId())
		assert.Equal(t, "even longer content body", env.Node.GetContent())
		assert.Equal(t, "the summary", env.Node.GetSummary())

		// An ABSENT format keeps the legacy bare-node body byte-for-byte.
		bareArgs := queryArgsJSON(t, queryArgs{ID: "n1", Graph: "knowledge"})
		bareOut, err := renderQueryTool(bareArgs, nodeResp(t, richNode("n1")))
		require.NoError(t, err)
		var decoded knowledgev1.Node
		require.NoError(t, json.Unmarshal([]byte(bareOut.Content[0].Text), &decoded))
		assert.Equal(t, "n1", decoded.Id)
		assert.Equal(t, "even longer content body", decoded.Content)
		assert.Equal(t, "the summary", decoded.Summary)
	})

	t.Run("metadata.<key> projection returns just that key", func(t *testing.T) {
		resp := nodeResp(t, richNode("n1"))
		args := queryArgsJSON(t, queryArgs{ID: "n1", Graph: "knowledge", Format: "json", Fields: []string{"id", "metadata.dsl_pattern"}})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		var row map[string]any
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &row))
		assert.Equal(t, "p1", row["metadata.dsl_pattern"])
		assert.NotContains(t, row, "metadata")
		assert.NotContains(t, row, "description")
	})
}

func TestRenderQueryTool_SearchArm_FieldsProjection(t *testing.T) {
	results := []SearchResult{
		{Score: 0.9, Node: &knowledgev1.Node{
			Id: "n1", Type: "finding", SymbolName: "Hit",
			Description: "heavy description", Content: "heavy content",
			Metadata: map[string]string{"dsl_pattern": "p1"},
		}},
	}
	resp := searchResp(t, results)

	t.Run("text-mode arm projects fields", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{Mode: "text", Text: "q", Format: "json", Fields: []string{"id", "name", "status"}})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		var payload struct {
			Results []map[string]any `json:"results"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		require.Len(t, payload.Results, 1)
		row := payload.Results[0]
		assert.Equal(t, "n1", row["id"])
		assert.NotContains(t, row, "description")
		assert.NotContains(t, row, "content")
		assert.NotContains(t, row, "symbol_name")
	})

	t.Run("default text arm projects fields", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{Text: "q", Format: "json", Fields: []string{"id", "metadata.dsl_pattern"}})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		assert.Contains(t, out.Content[0].Text, `"id":"n1"`)
		assert.Contains(t, out.Content[0].Text, `"metadata.dsl_pattern":"p1"`)
		assert.NotContains(t, out.Content[0].Text, `"description"`)
	})

	t.Run("empty fields returns full search envelope (no regression)", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{Mode: "text", Text: "q", Format: "json"})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		var env SearchJSONResponse
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &env))
		require.Len(t, env.Results, 1)
		assert.Equal(t, "heavy description", env.Results[0].Description)
		assert.Equal(t, "heavy content", env.Results[0].Content)
	})
}

// TestNodeProjection_SummaryPopulatedAndEmpty pins the ticket's own reproduction:
// `summary` is projectable, and a requested key is ALWAYS present in the row.
//
// The two fixtures are what make the claim meaningful. Asserting only the
// populated case proves the arm exists but says nothing about presence; the
// genuinely-empty case is what distinguishes "the field is present and unset"
// from "the key was not in your projection" — the indistinguishability this
// ticket exists to kill. require.Contains runs BEFORE the value assertion,
// because an assertion on the value alone passes against a MISSING key through
// a zero-value comparison, reintroducing the same ambiguity.
func TestNodeProjection_SummaryPopulatedAndEmpty(t *testing.T) {
	t.Run("populated summary projects its value", func(t *testing.T) {
		n := &knowledgev1.Node{Id: "s1", SymbolName: "Populated", Summary: "the distinct summary value"}
		out, err := renderNodesByIDsResponse(nodesResp(t, []*knowledgev1.Node{n}, 1), "knowledge", "json", []string{"id", "summary"}, false)
		require.NoError(t, err)
		require.False(t, out.IsError)
		var payload struct {
			Nodes []map[string]any `json:"nodes"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		require.Len(t, payload.Nodes, 1)
		row := payload.Nodes[0]
		require.Contains(t, row, "summary")
		require.Equal(t, "the distinct summary value", row["summary"])
	})

	t.Run("genuinely unset summary returns the key with an empty value", func(t *testing.T) {
		n := &knowledgev1.Node{Id: "s2", SymbolName: "Unset", Summary: ""}
		out, err := renderNodesByIDsResponse(nodesResp(t, []*knowledgev1.Node{n}, 1), "knowledge", "json", []string{"id", "summary"}, false)
		require.NoError(t, err)
		require.False(t, out.IsError)
		var payload struct {
			Nodes []map[string]any `json:"nodes"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		require.Len(t, payload.Nodes, 1)
		row := payload.Nodes[0]
		require.Contains(t, row, "summary", "a requested key must be present even when the field is unset")
		// Type-asserted before the emptiness check: require.Empty is satisfied by
		// a nil interface too, so on a bare `any` it would also pass for a key
		// that came back absent — the very ambiguity this sub-case exists to rule
		// out. The string assertion makes "present and empty" the only pass.
		summary, isString := row["summary"].(string)
		require.True(t, isString, "summary must project as a string, not a null")
		require.Empty(t, summary)
	})
}
