// SPDX-License-Identifier: Apache-2.0

package render_test

// section_format_parity_test.go is the enumeration this branch kept needing and
// kept not doing: every assemble read arm, times every format it accepts, times
// every parameter that could differ between them.
//
// THE RULE IT ENFORCES is one I recorded after the first time a feature reached
// the text arm and not the json one, and then failed to apply to the rest of the
// family: for every (arm, format, parameter) cell, the arm either implements the
// same behavior the text arm does, or refuses naming the format. Silently
// accepting and ignoring is the third outcome, it is the one that happens by
// default because an arm that takes no parameter cannot refuse one, and it is
// how the same defect reached three separate cells across three review rounds.
//
// SO THE TESTS BELOW ARE WRITTEN AS PARITY rather than as per-format expectations.
// A per-format expectation is a second place to forget something; a parity
// assertion fails the moment the two formats disagree about anything it covers,
// including a cell nobody thought to enumerate.
//
// THE CELLS, and where each is pinned:
//
//	assemble PLAN    text  no range      index and tree, NO bodies      here
//	assemble PLAN    json  no range      same, bodies marked omitted    here
//	assemble PLAN    text  range         bodies for the range           section_reads_test.go
//	assemble PLAN    json  range         same, others marked            section_json_test.go
//	assemble PLAN    both  annotations   count and kinds agree          here
//	assemble PLAN    both  sizes         agree                          here
//	assemble PLAN    both  invalid range same refusal text              section_json_test.go
//	assemble SECTION text  body          in full                        section_reads_test.go
//	assemble SECTION json  body          in full                        here
//	assemble SECTION both  annotations   agree                          here
//	assemble SECTION both  range         REFUSED, both formats          here
//	plan_tree        text/json/fields    annotations                    package tools
//	create_plan      text  annotations   none by construction           package tools

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
)

// parityFixture seeds a four-section plan whose SECOND section carries three
// annotations of two kinds, so every parity assertion below has both a populated
// case and an empty one in the same read.
func parityFixture(t *testing.T, rootID string) (*wireFixture, string) {
	t.Helper()
	w := newWireFixture()
	ids := seedFromBuilder(t, w, tenSectionPlan(), rootID)
	sectionID := ids[2]
	for i, a := range []struct{ kind, tier string }{
		{kgtypes.AnnotationKindFinding, "T2"},
		{kgtypes.AnnotationKindFinding, "T3"},
		{kgtypes.AnnotationKindCorrect, ""},
	} {
		id := rootID + "-ann" + strconv.Itoa(i)
		n := &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodePlanAnnotation),
			SymbolName: "note " + id, Summary: "a reviewer note",
		}
		kgtypes.SetValue(n, kgtypes.AnnotationKindKey, a.kind)
		if a.tier != "" {
			kgtypes.SetValue(n, kgtypes.AnnotationTierKey, a.tier)
		}
		w.addNode(n)
		w.addEdge(id, sectionID, string(kgtypes.EdgeRelatesTo), kgtypes.AnnotationEdgeMethod, "")
	}
	return w, sectionID
}

// TestAssembleFormats_AgreeOnTheNoRangeDefault is the cell the round-5 audit
// found: the documented default — supplying neither bound returns the plan's
// index and tree alone — held for text and not for json.
func TestAssembleFormats_AgreeOnTheNoRangeDefault(t *testing.T) {
	// A LARGE-BODIED FIXTURE, because the property under test is invisible on a
	// small one: the text tree truncates EVERY description at 120 characters, so
	// a 107-byte section body rides it whole and a test built on one would assert
	// nothing about what a real plan does.
	w := newWireFixture()
	seedFromBuilder(t, w, largeSectionPlan(), "plan-parity")

	text := toolText(handleAssemble(w, `{"id":"plan-parity"}`))
	jsonRes := handleAssemble(w, `{"id":"plan-parity","format":"json"}`)

	// TEXT: the index lists every section, and the tree carries the standard
	// 120-character truncation it applies to every node of every type — not a
	// section body block, which appears only when a range asks for one.
	require.Contains(t, text, "## Sections")
	assert.NotContains(t, text, "\n## Section ", "the text default renders no section BODY block")

	// JSON: every section present, every body dropped and MARKED.
	var sections int
	for _, c := range decodeJSONAssemble(t, jsonRes).Root.Children {
		if kgtypes.NodeType(c.Type) != kgtypes.NodePlanSection {
			continue
		}
		sections++
		assert.Empty(t, c.Description, "the json default renders no section body")
		assert.True(t, c.BodyOmitted,
			"and marks it omitted — an absent description is also what an EMPTY section looks like")
		assert.Positive(t, c.BodyBytes, "while still reporting the size, so a reader can choose pages")
	}
	assert.Equal(t, 10, sections, "both formats list every section")

	// THE PARITY THAT MATTERS, and the one the documented default promises:
	// NEITHER format returns a section's full body, so a caller who supplies no
	// range gets a bounded read whichever format they asked for. That is the cell
	// that was false — json returned every body, 76,093 bytes against 2,458.
	const deepInTheBody = "MARKER-DEEP"
	assert.NotContains(t, text, deepInTheBody, "the text default carries no body past its truncation")
	assert.NotContains(t, toolText(jsonRes), deepInTheBody, "and the json default carries none at all")
	assert.Less(t, renderedSize(t, jsonRes), 44000, "so the json default is a bounded read")
	assert.Less(t, renderedSize(t, handleAssemble(w, `{"id":"plan-parity"}`)), 44000,
		"and so is the text one")
}

// largeSectionPlan is the ten-section plan sized to the largest prefill measured
// on this project, with a marker buried deep in each body so a test can tell a
// truncated render from a whole one.
func largeSectionPlan() projects.PlanArgs {
	sizes := []int{12986, 9000, 8000, 8000, 7500, 7500, 7000, 6000, 5000, 3562}
	args := projects.PlanArgs{Name: "chunked", Goal: "the goal", Summary: "s"}
	for i, n := range sizes {
		head := "BODY-" + strconv.Itoa(i) + " "
		body := head + strings.Repeat("x", 300) + "MARKER-DEEP" +
			strings.Repeat("x", n-len(head)-300-len("MARKER-DEEP"))
		args.Sections = append(args.Sections, projects.SectionArgs{
			Name: "Section " + strconv.Itoa(i), Body: body, Summary: "summary " + strconv.Itoa(i),
		})
	}
	return args
}

// TestAssembleFormats_AgreeOnAnnotationState pins that the two formats report the
// SAME review state for the same sections — counts, kinds and the omit-when-none
// rule — rather than each having its own idea of it.
func TestAssembleFormats_AgreeOnAnnotationState(t *testing.T) {
	w, sectionID := parityFixture(t, "plan-parityann")

	text := toolText(handleAssemble(w, `{"id":"plan-parityann"}`))
	assert.Contains(t, text, "annotations: 3 (correct 1, finding 2)")
	assert.Equal(t, 2, strings.Count(text, "annotations: "),
		"the text render carries the line on the section's tree row and its index row, and nowhere else")

	var annotated, bare int
	for _, c := range decodeJSONAssemble(t, handleAssemble(w, `{"id":"plan-parityann","format":"json"}`)).Root.Children {
		if kgtypes.NodeType(c.Type) != kgtypes.NodePlanSection {
			continue
		}
		if c.ID != sectionID {
			assert.Nil(t, c.Annotations, "a section with none emits NO key, matching the text render's no-line rule")
			bare++
			continue
		}
		annotated++
		require.NotNil(t, c.Annotations)
		assert.Equal(t, 3, c.Annotations.Count, "the same count the text line reports")
		assert.Equal(t, map[string]int{kgtypes.AnnotationKindCorrect: 1, kgtypes.AnnotationKindFinding: 2},
			c.Annotations.Kinds, "and the same kinds")
	}
	assert.Equal(t, 1, annotated)
	assert.Equal(t, 9, bare)
}

// TestAssembleSectionFormats_Agree covers the SECTION read in both formats: the
// body rides in full in each, the review state agrees, and a range is refused by
// both because a section is not a plan.
func TestAssembleSectionFormats_Agree(t *testing.T) {
	w, sectionID := parityFixture(t, "plan-paritysec")

	text := toolText(handleAssemble(w, `{"id":"`+sectionID+`"}`))
	assert.Contains(t, text, "# Section: Section 1")
	assert.Contains(t, text, "BODY-1 ", "the text section read carries the body in full")
	assert.Contains(t, text, "## Annotations (3)")

	// JSON: the section is the ROOT, so its own review state belongs on the root —
	// which is exactly what a helper walking only the DESCENDANTS could not see,
	// and why this cell reported nothing until the round-5 sweep.
	var root struct {
		Root struct {
			ID          string `json:"id"`
			Type        string `json:"type"`
			Description string `json:"description"`
			BodyBytes   int    `json:"body_bytes"`
			BodyOmitted bool   `json:"body_omitted"`
			Annotations *struct {
				Count int            `json:"count"`
				Kinds map[string]int `json:"kinds"`
			} `json:"annotations"`
		} `json:"root"`
	}
	res := handleAssemble(w, `{"id":"`+sectionID+`","format":"json"}`)
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &root))
	assert.Equal(t, sectionID, root.Root.ID)
	assert.Contains(t, root.Root.Description, "BODY-1 ", "the json section read carries the body in full too")
	assert.False(t, root.Root.BodyOmitted, "a section read is not a page, so nothing is dropped from it")
	assert.Positive(t, root.Root.BodyBytes)
	require.NotNil(t, root.Root.Annotations, "the json section read carries its review state")
	assert.Equal(t, 3, root.Root.Annotations.Count)
	assert.Equal(t, map[string]int{kgtypes.AnnotationKindCorrect: 1, kgtypes.AnnotationKindFinding: 2},
		root.Root.Annotations.Kinds)

	// THE RANGE IS REFUSED IN BOTH FORMATS, with the same message: a section has
	// no sections to page.
	textRefusal := handleAssemble(w, `{"id":"`+sectionID+`","section_start":0}`)
	jsonRefusal := handleAssemble(w, `{"id":"`+sectionID+`","section_start":0,"format":"json"}`)
	require.True(t, textRefusal.IsError)
	require.True(t, jsonRefusal.IsError, "a range on a section must be refused in json too: %s", toolText(jsonRefusal))
	assert.Equal(t, toolText(textRefusal), toolText(jsonRefusal),
		"one refusal, both formats — the check sits above the format branch and must stay there")
}

// phaseAndStepPlan is a plan with phases and steps and NO sections — the shape
// every plan on this project has today, and the one a section range has nothing
// to page over.
func phaseAndStepPlan() projects.PlanArgs {
	return projects.PlanArgs{
		Name: "phase plan", Goal: "g", Summary: "s",
		Phases: []projects.PhaseArgs{{
			Name: "phase one", Overview: "o", Summary: "ps",
			Steps: []projects.StepArgs{{
				Name: "step one", Description: "d", Summary: "ss",
				Criteria: []projects.CriterionArgs{{Description: "c", Summary: "cs"}},
			}},
		}},
	}
}

// TestAssembleFormats_AgreeOnARangeOverAPlanWithNoSections drives the cell the
// enumeration above named and the input class it never drove: a range over a
// plan that HAS no sections.
//
// THE PARAMETER WAS ENUMERATED AND THE INPUT CLASS WAS NOT. "Both formats refuse
// an invalid range through the same resolveSectionRange" was true for every plan
// WITH sections and false for every plan without, because the json arm returned
// at its empty-sections guard before the resolver ran — so the resolver's own
// count==0 refusal, written and unit-tested, was unreachable from this format.
// The caller asked for a page of a phase-and-step plan, received the whole plan,
// and was told nothing.
func TestAssembleFormats_AgreeOnARangeOverAPlanWithNoSections(t *testing.T) {
	w := newWireFixture()
	seedFromBuilder(t, w, phaseAndStepPlan(), "phase-range")

	// The control: the same plan and format with NO range renders normally, so a
	// refusal below is a property of the range rather than of a broken fixture.
	noRange := handleAssemble(w, `{"id":"phase-range","format":"json"}`)
	require.False(t, noRange.IsError, "the no-range control must render: %s", toolText(noRange))

	for _, args := range []string{
		`"section_start":0`,
		`"section_end":3`,
		`"section_start":2,"section_end":9`,
	} {
		t.Run(args, func(t *testing.T) {
			text := handleAssemble(w, `{"id":"phase-range",`+args+`}`)
			jsonRes := handleAssemble(w, `{"id":"phase-range",`+args+`,"format":"json"}`)

			require.True(t, text.IsError, "the text arm refuses a range over a plan with no sections")
			require.True(t, jsonRes.IsError,
				"and json must refuse it too — accepting a page request and returning the whole plan is the silent degradation this file exists to catch: %s",
				toolText(jsonRes))
			assert.Equal(t, toolText(text), toolText(jsonRes),
				"one refusal through one resolveSectionRange, not two messages that can drift")
			assert.Contains(t, toolText(jsonRes), "this plan has no sections",
				"and the refusal names the plan shape, so a caller knows why their range was rejected")

			// THE DISCRIMINATING CONTROL for the exact shape of the defect: an
			// error flag on a body that is still the whole plan would satisfy
			// IsError while doing precisely what was wrong.
			assert.NotEqual(t, toolText(noRange), toolText(jsonRes),
				"a range request answered with the no-range body is the defect, whatever IsError says")
		})
	}
}
