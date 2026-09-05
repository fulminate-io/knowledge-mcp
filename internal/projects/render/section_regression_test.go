// SPDX-License-Identifier: Apache-2.0

package render_test

// section_regression_test.go holds the chunked-plan reads' CROSS-TREE
// regressions and the range refusal that guards them, split out of
// section_reads_test.go for the repository's 500-line per-file cap.
//
// WHAT THESE HAVE IN COMMON, and why they are the natural half to move: each
// asserts against something OUTSIDE this tree. Two compare bytes to output
// captured at origin/main before this branch; the third asserts a refusal that
// no other test in either package observes. Everything left behind in
// section_reads_test.go asserts a property of the current tree against itself.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
)

// TestPhasePlan_AssembleJSONIsByteIdenticalToPreChange is R6-e's FOURTH read,
// pinned the way the other three already are.
//
// WHY IT NEEDS ITS OWN TEST. plan_tree text, plan_tree json and assemble text are
// pinned to pre-change bytes by golden files this branch does not touch, so any
// drift in them fails a golden. assemble JSON has no golden. The nearest thing
// was TestPhasePlan_RendersUnchangedAlongsideSections, which compares a phase
// plan rendered ALONE against the same plan rendered BESIDE a sectioned one —
// a real property, and not this one: both sides are post-change, so a change that
// moved assemble-json's bytes uniformly would pass it unchanged.
//
// THE LITERAL IS CAPTURED, NOT COMPOSED. It is the output of this exact fixture
// run through render.Handle at origin/main 46196268, the tree before this branch,
// transcribed from that run's own log.
func TestPhasePlan_AssembleJSONIsByteIdenticalToPreChange(t *testing.T) {
	const preChangeJSON = `{"root":{"id":"phase-1","name":"phase plan","type":"plan","status":"active",` +
		`"description":"g","children":[{"id":"phase-1-n1","name":"phase one","type":"phase","status":"pending",` +
		`"description":"o","children":[{"id":"phase-1-n2","name":"step one","type":"step","status":"pending",` +
		`"description":"d","children":[{"id":"phase-1-n3","name":"c","type":"criterion","description":"c",` +
		`"metadata":{"type":"manual"}}]}]}]},"truncated":false}` + "429 rendered bytes."

	w := newWireFixture()
	seedFromBuilder(t, w, projects.PlanArgs{
		Name: "phase plan", Goal: "g", Summary: "s",
		Phases: []projects.PhaseArgs{{
			Name: "phase one", Overview: "o", Summary: "ps",
			Steps: []projects.StepArgs{{
				Name: "step one", Description: "d", Summary: "ss",
				Criteria: []projects.CriterionArgs{{Description: "c", Summary: "cs"}},
			}},
		}},
	}, "phase-1")

	// A PLAIN COMPARISON, NOT assert.Equal, AND THAT IS DELIBERATE. golangci's
	// testifylint autofixer rewrites assert.Equal over two JSON-looking strings
	// into assert.JSONEq — a SEMANTIC comparison, insensitive to key order and
	// whitespace, which is the opposite of what this test asserts. It did exactly
	// that to this line once. A plain != gives the autofixer nothing to
	// reinterpret, and a byte compare is what "byte-identical" means.
	got := toolText(handleAssemble(w, `{"id":"phase-1","format":"json"}`))
	if got != preChangeJSON {
		t.Errorf("a phase plan's assemble-json output must be byte-identical to the pre-change tree's\nwant: %q\ngot:  %q",
			preChangeJSON, got)
	}
}

// TestAssemble_SectionRangeOnANonPlanIsRefused observes the range refusal that
// TestSectionRange cannot reach.
//
// THE DIVISION OF LABOR, stated because the two look alike. TestSectionRange
// (package render) drives resolveSectionRange directly and covers every INVALID
// RANGE over a plan: inverted, out of bounds, negative, a range on a plan with no
// sections. What it cannot see is the check that runs BEFORE any of that, in
// Handle: a range supplied against a node that is not a plan at all. That one had
// no test — prefixing its condition with `false &&` left every test in tools and
// projects green — so it is observed here, through the real Handle, where it
// lives.
//
// WHY THE REFUSAL EXISTS RATHER THAN A SILENT IGNORE: a range on a node with no
// sections is not a harmless extra param. Ignoring it would hand the caller the
// whole node while they asked for a slice of it, with nothing in the result
// saying their range was discarded.
func TestAssemble_SectionRangeOnANonPlanIsRefused(t *testing.T) {
	w := newWireFixture()
	ids := seedFromBuilder(t, w, tenSectionPlan(), "plan-nonplan")
	sectionID := ids[1]

	for _, args := range []string{
		`{"id":"` + sectionID + `","section_start":0}`,
		`{"id":"` + sectionID + `","section_end":2}`,
		`{"id":"` + sectionID + `","section_start":0,"section_end":2}`,
	} {
		t.Run(args, func(t *testing.T) {
			res := handleAssemble(w, args)
			require.True(t, res.IsError, "a section range on a non-plan node must be refused: %s", toolText(res))
			body := toolText(res)
			assert.Contains(t, body, "section_start/section_end apply to a plan")
			assert.Contains(t, body, sectionID, "the refusal names the node the caller gave")
			assert.Contains(t, body, string(kgtypes.NodePlanSection), "and the type it actually is")
		})
	}

	// CONTROL ONE: the SAME range on the PLAN succeeds, so the refusal is about
	// the node's type and not about the range params existing.
	ok := handleAssemble(w, `{"id":"plan-nonplan","section_start":0,"section_end":2}`)
	assert.False(t, ok.IsError, "the identical range on the plan must land: %s", toolText(ok))

	// CONTROL TWO: the same non-plan node with NO range assembles fine, so the
	// refusal is about the range and not about assembling a section.
	plain := handleAssemble(w, `{"id":"`+sectionID+`"}`)
	assert.False(t, plain.IsError, "a section read with no range is untouched: %s", toolText(plain))
}
