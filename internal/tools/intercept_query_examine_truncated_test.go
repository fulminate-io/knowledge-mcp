// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// intercept_query_examine_truncated_test.go pins what the examine arm can
// honestly say about completeness — and pins the PROPERTY that makes the answer
// honest, rather than trusting a comment for it.
//
// A truncation DISCLOSURE was proposed for this arm — capture the edge page's
// truncated flag out of the drain callback and append "this result may be
// incomplete". It would have been wrong.
// paging.DrainPivotEdges never ACCEPTS a saturated page — it halves the pivot
// set, re-reads a single pivot as a from_id band tiling, splits a saturating band
// at its median interior id, and only a pivot no band can divide returns an error
// naming the pivot and the ceiling. So an examine that RETURNS has a complete
// neighborhood, and one that cannot be served FAILS loudly. The disclosure would
// have been a false statement about a provably complete result.
//
// The tests below are the control that keeps that reasoning honest: if the drain
// ever starts returning a partial union, the first sub-test goes red and the
// constant-false key at the emission site becomes a lie that must be rewired.

// bandedEdgeFake answers the examine reads with an edge neighborhood whose
// UNBANDED page saturates and whose BANDED pages come back whole — the case
// drainPivotByBands resolves completely. It honors the half-open band exactly as
// a real server does, because drainBand's out-of-band guard rejects a fake that
// does not.
type bandedEdgeFake struct {
	subjectID string
	// hydrateTruncated makes the bulk peer+ancestor hydrate answer with the
	// response's Truncated flag set — the one read in this composition the server
	// really can clamp (an unbounded QueryPlan{Ids} above the 10,000-id bound).
	hydrateTruncated bool
	// unsplittable makes every page saturate, including the banded ones, which is
	// the pivot the band escape genuinely cannot divide (an out-degree above the
	// ceiling: every edge leaving the pivot carries its id as from_id).
	unsplittable bool
}

func (f *bandedEdgeFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		band := q.GetEdgeFromBand()
		if band == nil || f.unsplittable {
			return &knowledgev1.ExecuteResponse{Truncated: true}, nil
		}
		lo, hi := band.GetFromIdGte(), band.GetFromIdLt()
		all := []*knowledgev1.Edge{
			{FromId: "B", ToId: f.subjectID, Type: "informed-by"},
			{FromId: f.subjectID, ToId: "A", Type: "relates-to"},
		}
		var edges []*knowledgev1.Edge
		for _, e := range all {
			if (lo != "" && e.FromId < lo) || (hi != "" && e.FromId >= hi) {
				continue
			}
			edges = append(edges, e)
		}
		return &knowledgev1.ExecuteResponse{Edges: edges}, nil
	case q.GetById() == f.subjectID:
		return &knowledgev1.ExecuteResponse{
			Nodes: []*knowledgev1.Node{{Id: f.subjectID, SymbolName: "Subject", Type: "step"}},
		}, nil
	case len(q.GetSelection().GetFromId()) > 0:
		return &knowledgev1.ExecuteResponse{}, nil // no ancestry
	default:
		return &knowledgev1.ExecuteResponse{
			Nodes:     []*knowledgev1.Node{{Id: "A", SymbolName: "Alpha"}, {Id: "B", SymbolName: "Bravo"}},
			Truncated: f.hydrateTruncated,
		}, nil
	}
}

// TestExamine_EdgeNeighborhoodIsCompleteOrLoud is the PROPERTY the examine
// envelope's constant-false `truncated` key rests on. Both directions are driven
// in one run, and the second is the known-positive: without it, a fake that never
// saturates would satisfy the first for the trivial reason that nothing was ever
// clamped.
func TestExamine_EdgeNeighborhoodIsCompleteOrLoud(t *testing.T) {
	t.Run("a saturated pivot the bands CAN divide yields a complete union", func(t *testing.T) {
		f := &bandedEdgeFake{subjectID: "S"}
		data, found, err := composeInspectData(context.Background(), f.Execute, "S")
		require.NoError(t, err, "the band escape must resolve a divisible pivot rather than erroring")
		require.True(t, found)
		assert.Len(t, data.Edges, 2,
			"the union is COMPLETE — both the in-edge and the out-edge survive the band tiling, "+
				"which is why reporting the first page's saturation flag as a verdict on the RESULT "+
				"would be a false statement")
	})

	t.Run("a saturated pivot the bands CANNOT divide fails by name", func(t *testing.T) {
		f := &bandedEdgeFake{subjectID: "S", unsplittable: true}
		_, _, err := composeInspectData(context.Background(), f.Execute, "S")
		require.Error(t, err,
			"an unservable neighborhood must FAIL rather than return a silent sample")
		assert.Contains(t, err.Error(), "cannot be read completely by a pivot drain",
			"the failure names the pivot and the ceiling, which is the disclosure on this arm")
	})
}

// TestExamine_TruncatedField pins the examine JSON envelope's `truncated` key:
// present unconditionally, so no consumer special-cases this arm, and FALSE —
// which is accurate rather than a placeholder, per the property above.
func TestExamine_TruncatedField(t *testing.T) {
	const notice = "the server row ceiling engaged, so this result may be incomplete"

	examine := func(t *testing.T, format string) kgtools.ToolResult {
		t.Helper()
		f := &examineFake{subjectID: "S"}
		args := `{"mode":"examine","id":"S"}`
		if format != "" {
			args = `{"mode":"examine","id":"S","format":"` + format + `"}`
		}
		handled, res := InterceptQueryExamine(opCtx(), &parityDeps{gc: f},
			kgtools.CallToolParams{Name: "query", Arguments: []byte(args)})
		require.True(t, handled, "the examine intercept must claim this call")
		require.False(t, res.IsError, textBodyTools(res))
		return res
	}

	// THE HYDRATE IS THE READ THAT CAN ACTUALLY BE CLAMPED, and these two legs are
	// the fails-when-absent pair for it. An earlier revision emitted this key as a
	// CONSTANT false on the grounds that the edge drain is complete-or-loud — true
	// of the edges, false of the composition, which ends in one unbounded
	// QueryPlan{Ids} over the drained union's peer set.
	t.Run("clamped peer hydrate: key true and the disclosure appears", func(t *testing.T) {
		f := &bandedEdgeFake{subjectID: "S", hydrateTruncated: true}
		handled, res := InterceptQueryExamine(opCtx(), &parityDeps{gc: f},
			kgtools.CallToolParams{Name: "query", Arguments: []byte(`{"mode":"examine","id":"S","format":"json"}`)})
		require.True(t, handled)
		require.False(t, res.IsError, textBodyTools(res))

		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload),
			"content[0] must stay the JSON payload — the notice rides a SEPARATE block")
		assert.Equal(t, true, payload["truncated"],
			"a clamped peer hydrate leaves peers and ancestry rows unnamed; the key must say so")
		assert.Contains(t, textBodyTools(res), notice,
			"examine bypasses engine.Render, so it must append the disclosure itself")
		require.Len(t, res.Content, 2,
			"the payload and the notice are two blocks: concatenating them would break JSON.parse")
	})

	t.Run("whole peer hydrate: key false and no disclosure", func(t *testing.T) {
		// The known-negative. Without it a constant true, or a notice appended
		// unconditionally, would satisfy the leg above.
		f := &bandedEdgeFake{subjectID: "S"}
		handled, res := InterceptQueryExamine(opCtx(), &parityDeps{gc: f},
			kgtools.CallToolParams{Name: "query", Arguments: []byte(`{"mode":"examine","id":"S","format":"json"}`)})
		require.True(t, handled)
		require.False(t, res.IsError, textBodyTools(res))

		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload))
		assert.Equal(t, false, payload["truncated"])
		assert.NotContains(t, textBodyTools(res), notice)
	})

	t.Run("the key is present and false", func(t *testing.T) {
		res := examine(t, "json")
		require.NotEmpty(t, res.Content)
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload), textBodyTools(res))

		got, ok := payload["truncated"]
		require.True(t, ok,
			"the key is UNCONDITIONAL: an absent key is indistinguishable from an old binary, and a "+
				"caller following query_schema.go would have to special-case this arm. Keys present: %v",
			keysOfPayload(payload))
		assert.Equal(t, false, got,
			"a successfully rendered examine has a COMPLETE neighborhood, so false is the accurate answer")
	})

	t.Run("no truncation notice is appended on either arm", func(t *testing.T) {
		// The known-negative for the notice: appending one here would claim an
		// incompleteness the drain's complete-or-error contract rules out.
		assert.NotContains(t, textBodyTools(examine(t, "json")), notice)
		assert.NotContains(t, textBodyTools(examine(t, "")), notice)
	})

	t.Run("the payload block stays parseable JSON", func(t *testing.T) {
		res := examine(t, "json")
		require.Len(t, res.Content, 1,
			"nothing is appended, so the payload is the only block")
	})
}

// keysOfPayload lists a payload's keys for a failure message, so an absent key
// reports what WAS there rather than a bare false.
func keysOfPayload(payload map[string]any) []string {
	out := make([]string, 0, len(payload))
	for k := range payload {
		out = append(out, k)
	}
	return out
}
