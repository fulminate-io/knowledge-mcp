// SPDX-License-Identifier: Apache-2.0

package tools

// plan_tree_annotations_test.go covers query(mode:"plan_tree")'s annotation read,
// end to end through the real intercept.
//
// IT HAD NO TEST AT ALL. planTreeAnnotationLines could be short-circuited to
// return no annotations and no verdict and every test in tools, projects, render
// and the root client package stayed green — so a plan_tree render showing
// per-section annotation kinds and counts was a behavior nothing observed. That
// is also why the arm's DEGRADE half went unswept when its two siblings were
// fixed: nothing exercised the path in either direction.
//
// THE TWO TESTS HERE ARE THE TWO DIRECTIONS. One asserts the line appears with
// its kinds and count on a plan that has annotations; the other asserts that when
// the read FAILS the caller is told so, in the arm's own words rather than as a
// row-ceiling truncation.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// annotatedPlanTreeFixture seeds a plan with two positioned sections, one of
// which carries two annotations of different kinds.
func annotatedPlanTreeFixture() *parityGraphFixture {
	f := newParityFixture()
	f.add(&knowledgev1.Node{Id: "plan-a", Type: string(kgtypes.NodePlan), SymbolName: "chunked", Status: kgtypes.StatusActive})
	for i, name := range []string{"Touch points", "What to test"} {
		id := "sec-" + string(rune('0'+i))
		n := &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodePlanSection), SymbolName: name,
			Description: "body of " + name, Status: kgtypes.StatusActive,
		}
		kgtypes.SetValue(n, "position", string(rune('0'+i)))
		f.add(n)
		f.linkPositioned("plan-a", id, i)
	}
	for _, a := range []struct{ id, kind, tier string }{
		{"ann-1", kgtypes.AnnotationKindFinding, "T2"},
		{"ann-2", kgtypes.AnnotationKindCorrect, ""},
	} {
		n := &knowledgev1.Node{
			Id: a.id, Type: string(kgtypes.NodePlanAnnotation),
			SymbolName: "note " + a.id, Summary: "a reviewer note",
		}
		kgtypes.SetValue(n, kgtypes.AnnotationKindKey, a.kind)
		if a.tier != "" {
			kgtypes.SetValue(n, kgtypes.AnnotationTierKey, a.tier)
		}
		f.add(n)
		f.edges = append(f.edges, &knowledgev1.Edge{
			FromId: a.id, ToId: "sec-0", Type: string(kgtypes.EdgeRelatesTo),
			Method: kgtypes.AnnotationEdgeMethod,
		})
	}
	return f
}

// TestInterceptQueryPlanTree_RendersSectionAnnotations is the behavior nothing
// observed: the tree names each annotated section's kinds and count.
func TestInterceptQueryPlanTree_RendersSectionAnnotations(t *testing.T) {
	handled, res := InterceptQueryPlanTree(context.Background(),
		interceptTestDeps{gc: annotatedPlanTreeFixture().gc()},
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"plan_tree","id":"plan-a"}`)})
	require.True(t, handled)
	require.False(t, res.IsError, "%s", toolResultText(res))
	body := toolResultText(res)

	assert.Contains(t, body, "annotations: 2 (correct 1, finding 1)",
		"the annotated section's line names both kinds and the count")

	// THE OTHER SECTION CARRIES NO LINE, which is the omit-when-none rule that
	// keeps every plan written before annotations existed rendering identically.
	// Asserted as a count so a second line anywhere would fail.
	assert.Equal(t, 1, strings.Count(body, "annotations: "),
		"exactly one section carries an annotation line; a section with none gets no line at all")
	assert.Contains(t, body, "What to test", "and the unannotated section still renders")
}

// TestInterceptQueryPlanTree_AnnotationReadFailureDisclosesItsOwnCause is the
// arm's DEGRADE half — the third of three, and the one that was not swept when
// its two siblings were.
//
// IT ASSERTS THE ROW-CEILING TEXT IS ABSENT, which is the whole finding: a failed
// annotation read used to be reported as a server row ceiling engaging, telling
// the caller to retry with a smaller `limit`. No ceiling engaged, and `limit` on
// this arm is the subtree DEPTH, so following that advice would return a
// shallower tree and the same missing annotations.
func TestInterceptQueryPlanTree_AnnotationReadFailureDisclosesItsOwnCause(t *testing.T) {
	f := annotatedPlanTreeFixture()

	// KNOWN-POSITIVE FIRST, through the same instrument: the healthy read carries
	// the annotation line and no notice at all.
	good := toolResultText(mustPlanTree(t, f.gc()))
	require.Contains(t, good, "annotations: 2 (correct 1, finding 1)")
	require.NotContains(t, good, planTreeAnnotationFailureNotice)
	require.NotContains(t, good, planTreeRowCeilingNotice)

	failing := &annotationEdgeReadFails{parityGraphFixture: f, err: assert.AnError}
	body := toolResultText(mustPlanTree(t, failing))

	require.Positive(t, failing.hits, "the injected failure must actually have been reached")
	assert.Contains(t, body, "chunked", "the tree still renders — a degrade, not a failure")
	assert.Contains(t, body, "Touch points")
	assert.NotContains(t, body, "annotations: ", "no annotation state is claimed when the read did not reach them")
	assert.Contains(t, body, planTreeAnnotationFailureNotice,
		"the caller is told the annotation read failed, which is the only thing distinguishing this from a plan with no review")
	assert.Contains(t, body, assert.AnError.Error(), "and the error is named, so an operator can act on it")
	assert.NotContains(t, body, planTreeRowCeilingNotice,
		"and NOT told a server row ceiling engaged — none did, and this arm's `limit` is the subtree depth, "+
			"so that notice's remedy would return a shallower tree with the same annotations missing")
}

// The two notice fragments, transcribed from the functions that emit them:
// render.AppendAnnotationReadFailureNotice and render.AppendTruncationNotice.
const (
	planTreeAnnotationFailureNotice = "The annotation read failed, so no annotation state is reported here"
	planTreeRowCeilingNotice        = "the server row ceiling engaged"
)

func mustPlanTree(t *testing.T, gc GraphCaller) kgtools.ToolResult {
	t.Helper()
	handled, res := InterceptQueryPlanTree(context.Background(), interceptTestDeps{gc: gc},
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"plan_tree","id":"plan-a"}`)})
	require.True(t, handled)
	require.False(t, res.IsError, "%s", toolResultText(res))
	return res
}

// annotationEdgeReadFails fails EXACTLY the annotation edge read, leaving the
// traversal and the depends-on read working.
//
// It keys on the pivot set for the reason its sibling in the render package does:
// IterEdgesFor applies its edge-type filter client-side, so the request carries
// no edge type to key on, and what distinguishes the annotation read is that its
// pivots are the plan_section ids and nothing else.
type annotationEdgeReadFails struct {
	*parityGraphFixture
	err  error
	hits int
}

func (a *annotationEdgeReadFails) Call(ctx context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error) {
	return (&parityCaller{f: a.parityGraphFixture}).Call(ctx, tool, args)
}

func (a *annotationEdgeReadFails) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES && a.allSectionPivots(q.GetIds()) {
		a.hits++
		return nil, a.err
	}
	return (&parityCaller{f: a.parityGraphFixture}).Execute(ctx, req)
}

func (a *annotationEdgeReadFails) allSectionPivots(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		n, ok := a.parityGraphFixture.nodes[id]
		if !ok || kgtypes.NodeType(n.GetType()) != kgtypes.NodePlanSection {
			return false
		}
	}
	return true
}

// TestInterceptQueryPlanTree_AnnotationsReachEveryFormat is the plan_tree row of
// the format-parity matrix: the same fixture read three ways must report the same
// review state.
//
// TEXT HAD IT AND THE OTHER TWO DID NOT, which is the shape this branch hit in
// three separate places before the sweep. The annotation read was wired into the
// text branch alone, so a caller asking for json — or for a projection, which
// selects json — saw a reviewed plan as an unreviewed one.
//
// THE FIELDS CELL IS THE INTERESTING ONE. A projection names node fields, and
// annotations are not a node field: they are read state derived from a separate
// edge walk. The key rides beside the projection rather than inside it, on the
// rule the `children` key in the same builder already follows and states — it
// describes the READ rather than the node. Dropping it under a projection would
// mean a projected read of a reviewed plan reports no review, which is the defect
// in a new costume.
func TestInterceptQueryPlanTree_AnnotationsReachEveryFormat(t *testing.T) {
	const wantCount = 2

	t.Run("text", func(t *testing.T) {
		body := toolResultText(mustPlanTree(t, annotatedPlanTreeFixture().gc()))
		assert.Contains(t, body, "annotations: 2 (correct 1, finding 1)")
	})

	for _, tc := range []struct{ name, args string }{
		{"json", `{"mode":"plan_tree","id":"plan-a","format":"json"}`},
		{"fields projection", `{"mode":"plan_tree","id":"plan-a","fields":["id","name"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handled, res := InterceptQueryPlanTree(context.Background(),
				interceptTestDeps{gc: annotatedPlanTreeFixture().gc()},
				kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(tc.args)})
			require.True(t, handled)
			require.False(t, res.IsError, "%s", toolResultText(res))

			var payload struct {
				Children []struct {
					ID          string `json:"id"`
					Annotations *struct {
						Count int            `json:"count"`
						Kinds map[string]int `json:"kinds"`
					} `json:"annotations"`
				} `json:"children"`
			}
			require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &payload))
			require.Len(t, payload.Children, 2)

			var annotated, bare int
			for _, c := range payload.Children {
				if c.Annotations == nil {
					bare++
					continue
				}
				annotated++
				assert.Equal(t, "sec-0", c.ID)
				assert.Equal(t, wantCount, c.Annotations.Count, "the same count the text line reports")
				assert.Equal(t, map[string]int{kgtypes.AnnotationKindCorrect: 1, kgtypes.AnnotationKindFinding: 1},
					c.Annotations.Kinds, "and the same kinds")
			}
			assert.Equal(t, 1, annotated, "the annotated section carries the key")
			assert.Equal(t, 1, bare,
				"and the other omits it entirely — a zero would change the bytes of every plan written before annotations existed")
		})
	}
}

// TestInterceptCreatePlan_ReturnedTreeCarriesNoAnnotations is the create_plan cell
// of the matrix, and it is a cell where NO behavior is the right behavior — stated
// as a test rather than left as an omission, so a later reader does not have to
// decide whether it was considered.
//
// A plan is created and rendered in one call, so nothing can have annotated it
// yet. The tree renderer is handed nil, and the correct assertion is that no
// annotation line appears — not that some empty state is reported.
func TestInterceptCreatePlan_ReturnedTreeCarriesNoAnnotations(t *testing.T) {
	gc := &createPlanTreeCaller{f: sectionOrderFixture()}
	handled, res := InterceptCreatePlan(opCtx(), interceptTestDeps{gc: gc}, kgtools.CallToolParams{
		Name: "create_plan",
		Arguments: json.RawMessage(`{
			"name":"chunked","goal":"g","summary":"s","no_patterns_reason":"x",
			"sections":[{"name":"Section zero","body":"b","summary":"s0","position":0}]
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "%s", toolResultText(res))
	assert.NotContains(t, toolResultText(res), "annotations: ",
		"a plan cannot be annotated before it exists, so its create render carries no annotation line at all")
}
