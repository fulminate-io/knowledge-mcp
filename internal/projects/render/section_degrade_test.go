// SPDX-License-Identifier: Apache-2.0

package render_test

// section_degrade_test.go covers the two ERROR ARMS of the annotation read: the
// plan assemble's and the section assemble's.
//
// WHY THEY NEED THEIR OWN TESTS. Both arms swallow the error from
// FetchSectionAnnotations, render what they have, and RAISE THE TRUNCATION
// VERDICT so the caller is told the render is incomplete rather than told there
// are no annotations. That verdict is the entire observable difference between a
// degrade and a lie, and nothing else in the suite reads it: setting both arms'
// verdicts to false left every other test in this package green, which is how a
// reviewer found this gap rather than a test finding it.
//
// EACH TEST ASSERTS THREE THINGS, and the third is the one that matters:
//   - the render still carries what it could read (a degrade, not a failure),
//   - it carries NO annotation state (the read did not reach them),
//   - and the truncation notice IS present, which is what stops the second point
//     from reading as "this section has no annotations".
//
// THE SAME-RUN KNOWN-POSITIVE runs the identical fixture WITHOUT the failure
// injected, and shows the annotations rendering and the notice absent. Without
// it, an arm that emitted the notice unconditionally would pass every assertion
// below.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
)

// annotationFailureNotice is the stable fragment of the text
// AppendAnnotationReadFailureNotice emits (annotations.go), transcribed rather
// than approximated.
//
// IT IS NOT THE TRUNCATION NOTICE, and the change is the point rather than a
// rename. Both degrade arms used to route a failed annotation read into
// AppendTruncationNotice, whose text names a server row ceiling that did not
// engage and tells the caller to re-run with a smaller `limit` — a parameter the
// assemble tool does not accept, so the only remedy it offered could not be
// taken. The disclosure is still mandatory, because "this section has no
// annotations" and "the annotations could not be read" are the same bytes
// without one; what changed is that it now names the cause that occurred.
const annotationFailureNotice = "The annotation read failed, so no annotation state is reported here"

// rowCeilingNotice is the TRUNCATION notice's own fragment, asserted ABSENT on a
// failed annotation read: a caller must not be told a ceiling engaged when none
// did. Without this the two notices could be merged again and the tests above
// would not notice.
const rowCeilingNotice = "the server row ceiling engaged"

// annotationReadFailure wraps a wireFixture and fails EXACTLY the annotation
// edge read, leaving every other read of the same plan working.
//
// IT KEYS ON THE PIVOT SET RATHER THAN THE EDGE TYPE, and that is forced rather
// than chosen: IterEdgesFor applies its edge-type filter CLIENT-SIDE, so the
// request carries no edge-type selection to key on. What distinguishes the
// annotation read is its pivots — they are the plan_section ids and nothing else,
// while the depends-on read of the same tree pivots on the root and every
// descendant. Failing every edges read instead would also fail depends-on, and
// the test would then be observing a different degrade from the one it names.
type annotationReadFailure struct {
	*wireFixture
	err  error
	hits int
}

func (a *annotationReadFailure) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES && a.allSections(q.GetIds()) {
		a.hits++
		return nil, a.err
	}
	return a.wireFixture.Execute(ctx, req)
}

// allSections reports whether every pivot in a non-empty set is a plan_section.
func (a *annotationReadFailure) allSections(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		n, ok := a.wireFixture.nodes[id]
		if !ok || kgtypes.NodeType(n.GetType()) != kgtypes.NodePlanSection {
			return false
		}
	}
	return true
}

// annotatedSectionFixture seeds a two-section plan with one annotation on the
// first section, and returns the fixture with that section's id.
func annotatedSectionFixture(t *testing.T, rootID string) (*wireFixture, string) {
	t.Helper()
	args := projects.PlanArgs{Name: "chunked", Goal: "g", Summary: "s", Sections: []projects.SectionArgs{
		{Name: "Touch points", Body: "the touch points body", Summary: "touch points"},
		{Name: "What to test", Body: "the what-to-test body", Summary: "what to test"},
	}}
	w := newWireFixture()
	ids := seedFromBuilder(t, w, args, rootID)
	sectionID := ids[1]

	ann := &knowledgev1.Node{
		Id: rootID + "-ann", SymbolName: "a finding on the touch points",
		Type: string(kgtypes.NodePlanAnnotation), Summary: "the caller census is short by two",
	}
	kgtypes.SetValue(ann, kgtypes.AnnotationKindKey, kgtypes.AnnotationKindFinding)
	kgtypes.SetValue(ann, kgtypes.AnnotationTierKey, "T2")
	w.addNode(ann)
	w.addEdge(ann.Id, sectionID, string(kgtypes.EdgeRelatesTo), "", "")
	return w, sectionID
}

// TestAssemblePlan_AnnotationReadFailureDegradesAndDiscloses is the plan arm's
// error return: the tree still renders, no annotation line appears, and the
// truncation notice says the read was incomplete.
func TestAssemblePlan_AnnotationReadFailureDegradesAndDiscloses(t *testing.T) {
	w, _ := annotatedSectionFixture(t, "plan-deg")

	// KNOWN-POSITIVE FIRST, through the same instrument: with no failure injected
	// the annotation line renders and the notice is absent. Everything the failing
	// run asserts below is a change FROM this, not an absolute.
	good := toolText(handleAssemble(w, `{"id":"plan-deg"}`))
	require.Contains(t, good, "annotations: 1 (finding 1)")
	require.NotContains(t, good, annotationFailureNotice,
		"the healthy read discloses no truncation, so the notice below is the failure speaking")

	failing := &annotationReadFailure{wireFixture: w, err: assert.AnError}
	body := toolText(handleAssemble(failing, `{"id":"plan-deg"}`))

	require.Positive(t, failing.hits, "the injected failure must actually have been reached")
	assert.Contains(t, body, "Touch points", "the tree still renders — this is a degrade, not a failure")
	assert.Contains(t, body, "## Sections", "and the section index still renders")
	assert.NotContains(t, body, "annotations: ", "no annotation state is claimed when the read did not reach them")
	assert.Contains(t, body, annotationFailureNotice,
		"THE DISCLOSURE IS RAISED: without it a plan under review renders identically to one with no review on it")
	assert.Contains(t, body, assert.AnError.Error(), "and it names the error, so an operator can act on it")
	assert.NotContains(t, body, rowCeilingNotice,
		"a failed annotation read must NOT claim a server row ceiling engaged — no ceiling did, and that notice's "+
			"remedy is a `limit` parameter this tool does not accept")
}

// TestAssembleSection_AnnotationReadFailureDegradesAndDiscloses is the section
// arm's error return, with the same shape. The section BODY is the thing the
// caller asked for, so losing it to a failed annotation read would be the wrong
// trade; the body survives and the verdict carries the incompleteness.
func TestAssembleSection_AnnotationReadFailureDegradesAndDiscloses(t *testing.T) {
	w, sectionID := annotatedSectionFixture(t, "plan-degsec")

	good := toolText(handleAssemble(w, `{"id":"`+sectionID+`"}`))
	require.Contains(t, good, "## Annotations (1)")
	require.NotContains(t, good, annotationFailureNotice)

	failing := &annotationReadFailure{wireFixture: w, err: assert.AnError}
	body := toolText(handleAssemble(failing, `{"id":"`+sectionID+`"}`))

	require.Positive(t, failing.hits, "the injected failure must actually have been reached")
	assert.Contains(t, body, "# Section: Touch points")
	assert.Contains(t, body, "the touch points body", "the body the caller asked for survives the degrade")
	assert.NotContains(t, body, "## Annotations", "no annotation block is emitted when the read did not reach them")
	assert.Contains(t, body, annotationFailureNotice,
		"THE DISCLOSURE IS RAISED, which is the only thing distinguishing this render from a section that genuinely has none")
	assert.Contains(t, body, assert.AnError.Error(), "and it names the error")
	assert.NotContains(t, body, rowCeilingNotice,
		"a failed annotation read must not be reported as a row-ceiling truncation")
}

// TestSectionReads_TruncationVerdictIsNotUnconditional is the control that gives
// the two tests above their discriminating power, stated as its own test so it
// cannot be dropped as incidental.
//
// Both assert that a notice IS present on failure. An arm that emitted the notice
// on every read would satisfy both and disclose nothing. This asserts the other
// direction on the identical fixtures: a healthy read of the same plan and the
// same section carries NO notice.
func TestSectionReads_TruncationVerdictIsNotUnconditional(t *testing.T) {
	w, sectionID := annotatedSectionFixture(t, "plan-uncond")
	for _, args := range []string{`{"id":"plan-uncond"}`, `{"id":"` + sectionID + `"}`} {
		body := toolText(handleAssemble(w, args))
		assert.NotContains(t, body, annotationFailureNotice,
			"a complete read must NOT disclose truncation, or the disclosure means nothing: %s", args)
	}
}

// annotationHydrateClamp wraps a wireFixture and CLAMPS exactly the bulk ids[]
// hydrate of the ANNOTATION nodes: it drops their rows and raises the response's
// truncation flag, which is what a server row ceiling does to this read and the
// only truncation FetchSectionAnnotations can suffer.
//
// IT KEYS ON THE HYDRATED TYPE rather than on call order or on every bulk read,
// and that is forced rather than tidy: the plan arm also bulk-hydrates its linked
// research and decisions, so a clamp on every ids[] read would truncate those too
// and the test would be observing a different incompleteness from the one it
// names. The edge drain is left alone — it is complete-or-loud and contributes no
// truncation of its own.
type annotationHydrateClamp struct {
	*wireFixture
	hits int
}

func (a *annotationHydrateClamp) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_EDGES && a.allAnnotations(q.GetIds()) {
		a.hits++
		return &knowledgev1.ExecuteResponse{Truncated: true}, nil
	}
	return a.wireFixture.Execute(ctx, req)
}

// allAnnotations reports whether every id in a non-empty set is a plan_annotation.
func (a *annotationHydrateClamp) allAnnotations(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		n, ok := a.wireFixture.nodes[id]
		if !ok || kgtypes.NodeType(n.GetType()) != kgtypes.NodePlanAnnotation {
			return false
		}
	}
	return true
}

// TestAnnotationHydrateClamp_IsDisclosedInBothFormats is the TRUNCATION arm of
// the read whose ERROR arm the two tests above cover, and it is a parity test
// because that is where the gap was: three of the four reads that take this
// verdict ORed it into their own, and the json assemble discarded it with a blank
// identifier.
//
// WHY THE VALUE OF THE KEY IS THE POINT. `truncated` rides the json envelope
// UNCONDITIONALLY, so `false` is a positive statement of completeness rather than
// a missing key. A clamped annotation hydrate drops the annotation rows outright;
// a reader who then sees no annotations and `"truncated": false` has been told,
// affirmatively, that this plan carries no review state. That is the one thing
// the verdict exists to prevent.
func TestAnnotationHydrateClamp_IsDisclosedInBothFormats(t *testing.T) {
	for _, tc := range []struct{ name, root string }{
		{name: "the plan read"},
		{name: "the section read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, sectionID := annotatedSectionFixture(t, "plan-clamp")
			id := "plan-clamp"
			if tc.name == "the section read" {
				id = sectionID
			}

			// KNOWN-POSITIVE FIRST, through the same instrument and the same
			// fixture: with no clamp injected both formats report the annotation
			// and neither discloses truncation.
			goodText := toolText(handleAssemble(w, `{"id":"`+id+`"}`))
			goodJSON := handleAssemble(w, `{"id":"`+id+`","format":"json"}`)
			require.Contains(t, goodText, "finding", "the healthy text read carries the annotation state")
			require.Contains(t, toolText(goodJSON), `"annotations"`, "and so does the healthy json read")
			require.NotContains(t, goodText, rowCeilingNotice)
			require.False(t, decodeJSONAssemble(t, goodJSON).Truncated,
				"the healthy json read reports itself complete, so a true below is the clamp speaking")

			clampText := &annotationHydrateClamp{wireFixture: w}
			textBody := toolText(handleAssemble(clampText, `{"id":"`+id+`"}`))
			clampJSON := &annotationHydrateClamp{wireFixture: w}
			jsonRes := handleAssemble(clampJSON, `{"id":"`+id+`","format":"json"}`)

			require.Positive(t, clampText.hits, "the injected clamp must actually have been reached (text)")
			require.Positive(t, clampJSON.hits, "the injected clamp must actually have been reached (json)")

			// BOTH FORMATS LOSE THE ROWS — that half is not the defect and is
			// asserted so the disclosure below is the only difference between them.
			assert.NotContains(t, textBody, "annotations: ", "a clamped hydrate returns no annotation rows")
			assert.NotContains(t, toolText(jsonRes), `"annotations"`, "in either format")

			// AND BOTH MUST SAY SO.
			assert.Contains(t, textBody, rowCeilingNotice,
				"the text arm ORs the annotation read's verdict into its own")
			assert.True(t, decodeJSONAssemble(t, jsonRes).Truncated,
				"and the json envelope must carry the same verdict — reporting `false` here tells a caller "+
					"affirmatively that a plan under review has no review state on it")
			assert.Contains(t, toolText(jsonRes), rowCeilingNotice,
				"with the prose notice beside the key, the way every other truncated read discloses")
		})
	}
}
