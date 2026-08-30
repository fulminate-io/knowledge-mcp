// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_query_plan_tree_fields_test.go covers the `fields` projection on
// plan_tree. It is a SIBLING of intercept_query_plan_tree_test.go rather than an
// addition to it: that file is within a handful of lines of the repo's 500-line
// convention, and the split rule (query_arm_registry.go's header) says to split
// rather than trim what is asserted.

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// planTreeFieldsRow is one node of the rendered plan_tree json payload, kept
// generic so a row's ABSENT keys are observable — the load-bearing half of the
// projection assertion.
type planTreeFieldsRow = map[string]any

// drivePlanTreeFields runs the real intercept over the shared plan-tree fixture.
func drivePlanTreeFields(t *testing.T, extra map[string]any) kgtools.ToolResult {
	t.Helper()
	f := newParityFixture()
	planID := seedPlanTreeFixture(f)
	args := map[string]any{"mode": "plan_tree", "id": planID}
	maps.Copy(args, extra)
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	handled, res := InterceptQueryPlanTree(opCtx(), &parityDeps{gc: f.gc()},
		kgtools.CallToolParams{Name: "query", Arguments: raw})
	require.True(t, handled, "plan_tree is claimed client-side")
	return res
}

// planTreeRows flattens the rendered json tree into every row it contains, so an
// assertion covers the whole tree rather than only its root.
func planTreeRows(t *testing.T, body string) []planTreeFieldsRow {
	t.Helper()
	var root planTreeFieldsRow
	require.NoError(t, json.Unmarshal([]byte(body), &root), "plan_tree must emit valid JSON: %s", body)
	var out []planTreeFieldsRow
	var walk func(planTreeFieldsRow)
	walk = func(r planTreeFieldsRow) {
		out = append(out, r)
		children, ok := r["children"].([]any)
		if !ok {
			return
		}
		for _, c := range children {
			if row, ok := c.(planTreeFieldsRow); ok {
				walk(row)
			}
		}
	}
	walk(root)
	return out
}

// TestPlanTree_FieldsProjectsAndRefusesUnknownKey (FAILS-WHEN-ABSENT) asserts the
// two halves a same-run control proved were both missing, plus the both-directions
// leg that keeps them honest.
//
//	(1) PROJECTION APPLIES — fields:["id","name"] renders rows carrying id and name
//	    and NOT description. The ABSENCE of description is the load-bearing half: a
//	    projection that is parsed and then ignored still emits id and name, so a
//	    presence-only assertion is green against the defect.
//	(2) UNSUPPORTED KEY IS REFUSED, naming the offending key and the accepted list —
//	    the same refusal the ids-hydrate arm gives for the identical key.
//	(3) BOTH DIRECTIONS — with NO fields the tree renders unchanged, description and
//	    all. Without it, legs 1 and 2 are satisfiable by an arm that strips
//	    description unconditionally.
func TestPlanTree_FieldsProjectsAndRefusesUnknownKey(t *testing.T) {
	t.Run("projection applies to every row", func(t *testing.T) {
		res := drivePlanTreeFields(t, map[string]any{"fields": []string{"id", "name"}})
		require.False(t, res.IsError, "a valid projection is served: %s", extractText(res))

		rows := planTreeRows(t, extractText(res))
		require.Greater(t, len(rows), 1, "the fixture tree has descendants, so the walk covers more than the root")
		for _, r := range rows {
			assert.Contains(t, r, "id", "a projected row carries the requested id")
			assert.Contains(t, r, "name", "a projected row carries the requested name")
			assert.NotContains(t, r, "description",
				"an unrequested key is DROPPED — this is the assertion the inert projection failed")
			assert.NotContains(t, r, "status", "and so is every other unrequested key")
		}
	})

	t.Run("a projection selects json whatever format says", func(t *testing.T) {
		// A projected row is a field map, so `fields` forces the json envelope —
		// the same override both by-id arms carry. Asserted because it is the
		// property that makes leg 1 reachable for a caller who never sets format.
		res := drivePlanTreeFields(t, map[string]any{"fields": []string{"id"}, "format": "text"})
		require.False(t, res.IsError, "%s", extractText(res))
		rows := planTreeRows(t, extractText(res))
		require.NotEmpty(t, rows)
		assert.Contains(t, rows[0], "id")
		assert.NotContains(t, rows[0], "name")
	})

	t.Run("unsupported key is refused naming it and the vocabulary", func(t *testing.T) {
		res := drivePlanTreeFields(t, map[string]any{"fields": []string{"zzz_not_a_field"}})
		require.True(t, res.IsError, "an unsupported projection key is REFUSED, never ignored")
		body := extractText(res)
		assert.Contains(t, body, `"zzz_not_a_field"`, "the refusal names the offending key")
		assert.Contains(t, body, "Accepted keys:", "and the accepted vocabulary")
		assert.Contains(t, body, "metadata.<key>", "including the per-metadata-key form")
	})

	t.Run("a per-metadata-key projection is accepted", func(t *testing.T) {
		// The open half of the vocabulary. Without this leg, leg 3's refusal is
		// satisfiable by a validator that refuses every dotted key.
		res := drivePlanTreeFields(t, map[string]any{"fields": []string{"id", "metadata.author"}})
		assert.False(t, res.IsError, "metadata.<key> is a valid projection: %s", extractText(res))
	})

	t.Run("no fields renders the full tree unchanged", func(t *testing.T) {
		jsonRes := drivePlanTreeFields(t, map[string]any{"format": "json"})
		require.False(t, jsonRes.IsError)
		rows := planTreeRows(t, extractText(jsonRes))
		require.NotEmpty(t, rows)
		for _, r := range rows {
			assert.Contains(t, r, "description", "an absent projection drops nothing")
			assert.Contains(t, r, "status")
		}

		// And the text render is still the text render.
		textRes := drivePlanTreeFields(t, nil)
		require.False(t, textRes.IsError)
		assert.NotContains(t, extractText(textRes), `"children"`,
			"an absent projection leaves the default text tree in place")
	})
}
