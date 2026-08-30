// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
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

// TestByIDEdgeSummary_CompleteOrLoud pins the property that makes the by-id
// include_edges arm honest WITHOUT a truncation notice.
//
// A truncation DISCLOSURE was proposed here — capture the edge page's truncated
// flag out of composeEdgeSummary's drain callback and append the row-ceiling
// notice. It would have been wrong. paging.DrainPivotEdges never
// ACCEPTS a saturated page — it halves the pivot set, re-reads a single pivot as a
// from_id band tiling, splits a saturating band at its median interior id, and
// only a pivot no band can divide returns an error naming the pivot. So a summary
// that RETURNS is complete, and one that cannot be served FAILS. Appending "this
// result may be incomplete" would have been a false statement about a provably
// complete union.
//
// This is the control that keeps that reasoning honest rather than resting on a
// comment: if the drain ever starts returning a partial union, the first
// sub-test goes red.
//
// (There is no `truncated` KEY to assert on this arm: dispatchQueryByID never
// reads a.Format and both its renderers build markdown only, so an include_edges
// read has no JSON envelope at all — recorded as its own finding on this ticket.)
func TestByIDEdgeSummary_CompleteOrLoud(t *testing.T) {
	const notice = "the server row ceiling engaged, so this result may be incomplete"
	args := json.RawMessage(`{"id":"n1","include_edges":true}`)

	// byIDEdgeExec answers the three reads the arm issues. Its edge arm saturates
	// the UNBANDED page and, unless unsplittable, answers banded pages whole and
	// band-honoring — drainBand's out-of-band guard rejects a fake that does not.
	byIDEdgeExec := func(unsplittable bool) ExecuteFn {
		return func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			q := req.GetQuery()
			switch {
			case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
				band := q.GetEdgeFromBand()
				if band == nil || unsplittable {
					return &knowledgev1.ExecuteResponse{Truncated: true}, nil
				}
				lo, hi := band.GetFromIdGte(), band.GetFromIdLt()
				all := []*knowledgev1.Edge{
					{FromId: "a-peer", ToId: "n1", Type: "informed-by"},
					{FromId: "n1", ToId: "z-peer", Type: "relates-to"},
				}
				var edges []*knowledgev1.Edge
				for _, e := range all {
					if (lo != "" && e.FromId < lo) || (hi != "" && e.FromId >= hi) {
						continue
					}
					edges = append(edges, e)
				}
				return &knowledgev1.ExecuteResponse{Edges: edges}, nil
			case q.GetById() != "":
				return &knowledgev1.ExecuteResponse{
					Nodes: []*knowledgev1.Node{{Id: "n1", Type: "plan", SymbolName: "Root"}},
				}, nil
			default: // the bulk peer hydrate
				return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{
					{Id: "a-peer", Type: "phase", SymbolName: "APeer"},
					{Id: "z-peer", Type: "phase", SymbolName: "ZPeer"},
				}}, nil
			}
		}
	}

	t.Run("a saturated pivot the bands CAN divide renders completely and silently", func(t *testing.T) {
		res, handled := dispatchQueryByID(context.Background(), byIDEdgeExec(false), args)
		require.True(t, handled, "an include_edges by-id read is the absorption shape this arm claims")
		require.False(t, res.IsError)
		var sb strings.Builder
		for _, b := range res.Content {
			sb.WriteString(b.Text)
		}
		body := sb.String()
		assert.Contains(t, body, "APeer", "the in-edge survives the band tiling")
		assert.Contains(t, body, "ZPeer", "the out-edge survives it too — the summary is COMPLETE")
		assert.NotContains(t, body, notice,
			"a complete summary must not claim to be partial; the first page's saturation flag is a "+
				"signal about that PAGE, not a verdict on the result")
	})

	t.Run("a clamped PEER HYDRATE discloses", func(t *testing.T) {
		// THE OTHER READ. The edge drain is complete-or-loud; the peer hydrate is
		// not. composeEdgeSummary ends in ONE unbounded QueryPlan{Ids} over the
		// drained union's peer set, and the server clamps an id set above 10,000 on
		// the request alone. Every unreturned peer then renders under the
		// truncated-id fallback name, indistinguishable from a peer that genuinely
		// has no SymbolName — which is exactly the silent narrowing this arm must
		// not commit.
		exec := func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			q := req.GetQuery()
			switch {
			case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
				return &knowledgev1.ExecuteResponse{
					Edges: []*knowledgev1.Edge{{FromId: "n1", ToId: "z-peer", Type: "relates-to"}},
				}, nil
			case q.GetById() != "":
				return &knowledgev1.ExecuteResponse{
					Nodes: []*knowledgev1.Node{{Id: "n1", Type: "plan", SymbolName: "Root"}},
				}, nil
			default: // the bulk peer hydrate — CLAMPED
				return &knowledgev1.ExecuteResponse{Truncated: true}, nil
			}
		}
		res, handled := dispatchQueryByID(context.Background(), exec, args)
		require.True(t, handled)
		require.False(t, res.IsError)
		var sb strings.Builder
		for _, b := range res.Content {
			sb.WriteString(b.Text)
		}
		assert.Contains(t, sb.String(), notice,
			"the peer hydrate's verdict must reach the caller: an unnamed peer is indistinguishable "+
				"from a peer with no name, so a clamped hydrate renders a partial summary as a whole one")
	})

	t.Run("a clamped CROSS-LINK hydrate discloses, and the rows it drops are gone", func(t *testing.T) {
		// THE WORST OF THE FOUR HYDRATE SITES. collectProxyCrossLinks skips an
		// unresolved peer entirely (`peer, ok := peers[...]; if !ok { continue }`),
		// so a clamped hydrate DELETES cross-link rows rather than blanking a name.
		// Asserting the notice alone would not distinguish that from the
		// edge-summary case, so this pins the row LOSS as well: the whole run
		// renders both links, the clamped run renders one, and only the clamped run
		// carries the disclosure.
		crossLinkExec := func(hydrateTruncated bool) ExecuteFn {
			return func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
				q := req.GetQuery()
				switch {
				case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
					// The proxy's own edges: two peers, so two cross-link rows are due.
					return &knowledgev1.ExecuteResponse{Edges: []*knowledgev1.Edge{
						{FromId: "proxy-1", ToId: "peer-kept", Type: "relates-to"},
						{FromId: "proxy-1", ToId: "peer-dropped", Type: "relates-to"},
					}}, nil
				// THE TWO by-id READS ARE TOLD APART BY TARGET, NOT BY ID: the base
				// node read and findLinkageProxies' O(1) probe both carry ById:"n1",
				// and only the graph selector distinguishes them. Keying on the id
				// alone made the proxy probe answer with the plain node, so no proxy
				// resolved and the whole cross-link path was never exercised — the
				// fixture would have passed its notice assertion by rendering nothing.
				case q.GetById() != "" && req.GetTarget().GetGraph() == "linkage":
					return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{{
						Id: "proxy-1", Type: string(kgtypes.NodeProxy), SymbolName: "Proxy",
					}}}, nil
				case q.GetById() != "":
					return &knowledgev1.ExecuteResponse{
						Nodes: []*knowledgev1.Node{{Id: "n1", Type: "plan", SymbolName: "Root"}},
					}, nil
				default: // the bulk peer hydrate
					nodes := []*knowledgev1.Node{
						{Id: "peer-kept", Type: "finding", SymbolName: "KeptPeer"},
						{Id: "peer-dropped", Type: "finding", SymbolName: "DroppedPeer"},
					}
					if hydrateTruncated {
						// The clamp: the server returns fewer rows than were asked for.
						nodes = nodes[:1]
					}
					return &knowledgev1.ExecuteResponse{Nodes: nodes, Truncated: hydrateTruncated}, nil
				}
			}
		}

		bodyOf := func(t *testing.T, hydrateTruncated bool) string {
			t.Helper()
			res, handled := dispatchQueryByID(context.Background(), crossLinkExec(hydrateTruncated),
				json.RawMessage(`{"id":"n1","include_cross_links":true}`))
			require.True(t, handled, "an include_cross_links by-id read is an absorption shape this arm claims")
			require.False(t, res.IsError)
			var sb strings.Builder
			for _, b := range res.Content {
				sb.WriteString(b.Text)
			}
			return sb.String()
		}

		whole := bodyOf(t, false)
		require.Contains(t, whole, "KeptPeer", "the known-positive: both rows render when the hydrate is whole")
		require.Contains(t, whole, "DroppedPeer")
		assert.NotContains(t, whole, notice, "a whole hydrate must not claim to be partial")

		clamped := bodyOf(t, true)
		assert.Contains(t, clamped, notice,
			"a clamped cross-link hydrate must disclose: this arm bypasses Render and the clamp is silent otherwise")
		assert.Contains(t, clamped, "Showing 1 rows",
			"THE COUNT IS BOTH SECTIONS. This read carries no edges at all, so a row count taken from "+
				"len(edges) renders \"Showing 0 rows — the server row ceiling engaged\": a correct "+
				"disclosure wearing a number that contradicts it")
		assert.Contains(t, clamped, "KeptPeer", "the peer that did resolve still renders")
		assert.NotContains(t, clamped, "DroppedPeer",
			"THE ROW IS GONE, not merely unnamed — collectProxyCrossLinks skips an unresolved peer, "+
				"which is why the disclosure matters more here than on the edge summary")
	})

	t.Run("a saturated pivot the bands CANNOT divide fails by name", func(t *testing.T) {
		// The known-positive: without it, the silence above would be satisfied by a
		// fixture that never saturated at all.
		res, handled := dispatchQueryByID(context.Background(), byIDEdgeExec(true), args)
		require.True(t, handled)
		require.True(t, res.IsError, "an unservable neighborhood must FAIL, not render a silent sample")
		var sb strings.Builder
		for _, b := range res.Content {
			sb.WriteString(b.Text)
		}
		assert.Contains(t, sb.String(), "cannot be read completely by a pivot drain",
			"the failure names the pivot and the ceiling — that IS the disclosure on this arm")
	})
}
