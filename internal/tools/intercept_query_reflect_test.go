// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestInterceptQueryReflect_TimelineModeClaimed verifies query(mode:timeline)
// gets claimed by the recall routing branch after FUL-247.
func TestInterceptQueryReflect_TimelineModeClaimed(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, res := interceptQueryReflect(deps, kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(`{"mode":"timeline","limit":5}`),
	})
	require.True(t, handled, "timeline mode must be claimed by client")
	// The handler errors on missing GraphClient but the routing is what
	// we're testing — handled=true is the assertion.
	_ = res
}

// TestInterceptQueryReflect_ChargesModeClaimed verifies query(mode:charges)
// is claimed by the recall routing.
func TestInterceptQueryReflect_ChargesModeClaimed(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, _ := interceptQueryReflect(deps, kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(`{"mode":"charges"}`),
	})
	require.True(t, handled, "charges mode must be claimed")
}

// TestInterceptQueryReflect_ThoughtFilterClaimed verifies a query with any
// thought-property filter is claimed.
func TestInterceptQueryReflect_ThoughtFilterClaimed(t *testing.T) {
	for _, body := range []string{
		`{"valence_min": 0.5}`,
		`{"valence_max": 0.9}`,
		`{"magnitude_min": 1.0}`,
		`{"consistency_max": 0.5}`,
		`{"session": "test"}`,
		`{"connected_to": "node-1"}`,
		`{"status": "validated"}`,
	} {
		t.Run(body, func(t *testing.T) {
			deps := interceptTestDeps{gc: &fakeGraphCaller{}}
			handled, _ := interceptQueryReflect(deps, kgtools.CallToolParams{
				Name:      "query",
				Arguments: json.RawMessage(body),
			})
			assert.True(t, handled, "thought-property filter must be claimed: %s", body)
		})
	}
}

// TestInterceptQueryReflect_NonKnowledgeGraphFallsThrough verifies the
// advisory T3 guard from the plan-review notes: non-knowledge graphs
// return (false, _) so the server's existing generic-graph path serves
// them.
func TestInterceptQueryReflect_NonKnowledgeGraphFallsThrough(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, _ := interceptQueryReflect(deps, kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(`{"mode":"timeline","graph":"practice"}`),
	})
	assert.False(t, handled, "practice graph must fall through")
}

// TestInterceptQueryReflect_KnowledgeGraphExplicitClaimed verifies the
// guard accepts the explicit `graph:"knowledge"` form.
func TestInterceptQueryReflect_KnowledgeGraphExplicitClaimed(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, _ := interceptQueryReflect(deps, kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(`{"mode":"timeline","graph":"knowledge"}`),
	})
	assert.True(t, handled, "explicit knowledge graph must be claimed")
}

// TestInterceptQueryReflect_SimulateModeClaimed verifies simulate is always
// claimed by the intercept.
func TestInterceptQueryReflect_SimulateModeClaimed(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, _ := interceptQueryReflect(deps, kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(`{"mode":"simulate","action":"add_charge","target":"t-1","polarity":"positive","weight":2}`),
	})
	require.True(t, handled, "simulate mode must be claimed")
}

// TestInterceptQueryReflect_ExamineNoID_FallsThrough verifies that
// examine without an `id` falls through (other examine kinds use `target`).
func TestInterceptQueryReflect_ExamineNoID_FallsThrough(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	handled, _ := interceptQueryReflect(deps, kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(`{"mode":"examine"}`),
	})
	assert.False(t, handled, "examine with no id must fall through")
}

// TestRecallParamsFromQuery_FieldMapping verifies the args translation
// preserves the thought-filter fields.
func TestRecallParamsFromQuery_FieldMapping(t *testing.T) {
	vmin := flexFloat(0.25)
	a := queryReflectArgs{
		Mode:        "timeline",
		Format:      "json",
		Limit:       7,
		Text:        "search me",
		Status:      "validated",
		Session:     "ful-247",
		ConnectedTo: "node-7",
		ValenceMin:  &vmin,
		Type:        "all",
	}
	out := recallParamsFromQuery(kgtools.CallToolParams{Name: "query"}, a)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Arguments, &got))
	assert.Equal(t, "timeline", got["mode"])
	assert.Equal(t, "search me", got["query"])
	assert.Equal(t, "validated", got["status"])
	assert.Equal(t, "ful-247", got["session"])
	assert.Equal(t, "node-7", got["connected_to"])
	assert.InDelta(t, 0.25, got["valence_min"], 0.0001)
	assert.Equal(t, true, got["all_types"])
	assert.Equal(t, "json", got["format"])
}
