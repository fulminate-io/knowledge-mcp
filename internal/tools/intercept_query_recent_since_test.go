// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// sinceFake is a store double for the recent browse. It serves a seeded corpus
// through the SERVER's own updated_at rule rather than a rule invented here: parse
// the predicate's RFC3339 comparand, drop a node whose UpdatedAt falls before it
// (query_executor_match_field.go's matchTimeField, GTE arm). It stands in for the
// STORE — the dependency — not for the code under test.
//
// It is deliberately paired with a wire-shape assertion on the recorded plan. A
// double that evaluates the predicate could agree with a client that built the
// WRONG predicate (a lower bound on created_at, say, or an LTE), so the exclusion
// leg alone would not distinguish them; asserting the recorded field and op is what
// makes the pair discriminating.
type sinceFake struct {
	nodes []*knowledgev1.Node
	reqs  []*knowledgev1.ExecuteRequest
}

func (f *sinceFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.reqs = append(f.reqs, req)
	q := req.GetQuery()
	// The drain pages by id keyset; serve everything on page one and nothing after,
	// so the drain terminates.
	if q.GetAfterId() != "" {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	out := make([]*knowledgev1.Node, 0, len(f.nodes))
	for _, n := range f.nodes {
		if f.admits(q.GetSelection(), n) {
			out = append(out, n)
		}
	}
	return enginetest.ResponseWithNodes(out...), nil
}

// admits applies every field predicate on the selection to one node, mirroring the
// server's updated_at GTE semantics.
func (f *sinceFake) admits(sel *knowledgev1.Selection, n *knowledgev1.Node) bool {
	for _, p := range sel.GetFieldPredicates() {
		if p.GetField() != "updated_at" || p.GetOp() != knowledgev1.MetadataPredicate_OP_GTE {
			return false // an unexpected predicate matches nothing, as the server's default arm does.
		}
		want, err := time.Parse(time.RFC3339, p.GetValue())
		if err != nil {
			return false
		}
		got := time.Unix(0, n.GetUpdatedAt())
		if got.Before(want) {
			return false
		}
	}
	return true
}

// sincePredicate returns the single updated_at field predicate the recorded browse
// carried, or nil when the plan carried none.
func sincePredicate(reqs []*knowledgev1.ExecuteRequest) *knowledgev1.FieldPredicate {
	for _, r := range reqs {
		for _, p := range r.GetQuery().GetSelection().GetFieldPredicates() {
			if p.GetField() == "updated_at" {
				return p
			}
		}
	}
	return nil
}

// driveRecentSince runs query(mode:"recent") through the real intercept against the
// store double and returns the rendered result plus the double.
func driveRecentSince(t *testing.T, args map[string]any) (kgtools.ToolResult, *sinceFake) {
	t.Helper()
	f := &sinceFake{nodes: []*knowledgev1.Node{
		{Id: "inside", Type: "ticket", SymbolName: "InsideWindow", UpdatedAt: daysAgoNanos(0.25)},
		{Id: "outside", Type: "ticket", SymbolName: "OutsideWindow", UpdatedAt: daysAgoNanos(90)},
	}}
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	handled, res := InterceptQueryKnowledgeSearch(opCtx(), interceptTestDeps{gc: f},
		kgtools.CallToolParams{Name: "query", Arguments: raw})
	require.True(t, handled, "bare recent is claimed client-side")
	return res, f
}

// TestRecentBrowse_SinceFiltersByUpdatedAt (FAILS-WHEN-ABSENT) asserts the three
// properties `since` owes on mode:recent, and the third is what makes it more than
// a smoke test.
//
//	(1) a node updated INSIDE the window is returned;
//	(2) a node updated OUTSIDE it is EXCLUDED — without this leg the gate passes
//	    against an implementation that parses since and then ignores it;
//	(3) an unparseable value is REFUSED naming the offending value AND both
//	    accepted forms, never silently dropped and never defaulted to a window.
func TestRecentBrowse_SinceFiltersByUpdatedAt(t *testing.T) {
	t.Run("relative window admits inside and excludes outside", func(t *testing.T) {
		res, f := driveRecentSince(t, map[string]any{
			"mode": "recent", "types": []string{"ticket"}, "since": "24h",
		})
		require.False(t, res.IsError, "a valid since is served: %s", engine.FirstTextContent(res))
		body := engine.FirstTextContent(res)
		assert.Contains(t, body, "InsideWindow", "a node updated inside the window is returned")
		assert.NotContains(t, body, "OutsideWindow", "a node updated outside the window is excluded")

		// The wire shape: an updated_at LOWER bound, pushed onto the fetch selection
		// rather than applied in the render.
		p := sincePredicate(f.reqs)
		require.NotNil(t, p, "the browse carries an updated_at field predicate")
		assert.Equal(t, knowledgev1.MetadataPredicate_OP_GTE, p.GetOp(), "since is a LOWER bound")
		cutoff, err := time.Parse(time.RFC3339, p.GetValue())
		require.NoError(t, err, "the comparand is RFC3339, which is what the server parses")
		assert.WithinDuration(t, time.Now().Add(-24*time.Hour), cutoff, time.Minute,
			"a 24h window resolves to a cutoff 24 hours back")
	})

	t.Run("absolute RFC3339 window is accepted", func(t *testing.T) {
		res, f := driveRecentSince(t, map[string]any{
			"mode": "recent", "types": []string{"ticket"},
			"since": time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339),
		})
		require.False(t, res.IsError, "an RFC3339 since is served: %s", engine.FirstTextContent(res))
		assert.Contains(t, engine.FirstTextContent(res), "InsideWindow")
		assert.NotContains(t, engine.FirstTextContent(res), "OutsideWindow")
		require.NotNil(t, sincePredicate(f.reqs))
	})

	t.Run("absent since returns everything", func(t *testing.T) {
		// BOTH DIRECTIONS: without this, the two legs above are satisfiable by an arm
		// that drops the older node unconditionally.
		res, f := driveRecentSince(t, map[string]any{"mode": "recent", "types": []string{"ticket"}})
		require.False(t, res.IsError)
		body := engine.FirstTextContent(res)
		assert.Contains(t, body, "InsideWindow")
		assert.Contains(t, body, "OutsideWindow", "an absent since filters nothing")
		assert.Nil(t, sincePredicate(f.reqs), "no since means no predicate on the wire")
	})

	t.Run("unparseable since is refused naming the value and both forms", func(t *testing.T) {
		res, f := driveRecentSince(t, map[string]any{
			"mode": "recent", "types": []string{"ticket"}, "since": "last tuesday",
		})
		require.True(t, res.IsError, "an unparseable since is REFUSED, never silently dropped")
		body := engine.FirstTextContent(res)
		assert.Contains(t, body, `"last tuesday"`, "the refusal names the offending value")
		assert.Contains(t, body, "RFC3339", "the refusal names the absolute form")
		assert.True(t, strings.Contains(body, "24h") && strings.Contains(body, "7d"),
			"the refusal names the relative form; got: %s", body)
		assert.Empty(t, f.reqs, "the refusal precedes the read — no window means no browse")
	})
}
