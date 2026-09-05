// SPDX-License-Identifier: Apache-2.0

package render_test

// section_json_test.go covers what the JSON assemble does with a chunked plan:
// the section range it used to drop, and the review state it used to omit.
//
// BOTH WERE SILENT. assemble(id:<plan>, format:"json", section_start,
// section_end) returned bytes IDENTICAL to the same call with no range — the
// non-plan range refusal sits above the format branch, so a plan plus json sailed
// past it and the json arm took no range at all. On a real-sized plan that is the
// difference between a page and a result above the point where reads spill, and
// the caller was told nothing either way. And a json reader of a plan under
// review saw exactly what a json reader of an unreviewed plan saw.
//
// THE TWO ARE ONE FIX because they are one gap: the json arm was the text arm
// minus its chunked-plan features.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
)

// jsonRoot is the slice of the json envelope these tests read.
type jsonRoot struct {
	// THE ENVELOPE VERDICT, read rather than assumed. It is emitted
	// unconditionally, so `false` is a positive statement of completeness and a
	// test that never looks at it cannot tell a complete read from a clamped one.
	Truncated bool `json:"truncated"`
	Root      struct {
		Children []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Type        string `json:"type"`
			Description string `json:"description"`
			BodyBytes   int    `json:"body_bytes"`
			BodyOmitted bool   `json:"body_omitted"`
			Annotations *struct {
				Count int            `json:"count"`
				Kinds map[string]int `json:"kinds"`
			} `json:"annotations"`
		} `json:"children"`
	} `json:"root"`
}

// decodeJSONAssemble reads the envelope out of a tool result. The rendered-size
// disclosure rides as its own block, so the FIRST block is the json.
func decodeJSONAssemble(t *testing.T, res kgtools.ToolResult) jsonRoot {
	t.Helper()
	require.NotEmpty(t, res.Content)
	var out jsonRoot
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &out),
		"the first block must be the json envelope: %q", res.Content[0].Text)
	return out
}

// TestAssembleJSON_HonorsTheSectionRange is T1: the range reaches the json arm
// and bounds what it returns.
func TestAssembleJSON_HonorsTheSectionRange(t *testing.T) {
	w := newWireFixture()
	seedFromBuilder(t, w, tenSectionPlan(), "plan-json")

	whole := handleAssemble(w, `{"id":"plan-json","format":"json"}`)
	page := handleAssemble(w, `{"id":"plan-json","format":"json","section_start":2,"section_end":4}`)
	require.False(t, page.IsError, "%s", toolText(page))

	// THE OBSERVATION THAT NAMES THE FIRST DEFECT: the two used to be byte-identical,
	// because the json arm took no range at all.
	assert.NotEqual(t, toolText(whole), toolText(page),
		"a range must change the result — identical bytes IS the silent drop this closes")

	// AND THE PAGE IS NOW THE LARGER OF THE TWO, which reads backwards until you
	// see what each contains. A no-range read carries the index and the tree and
	// NO bodies, which is what the schema documents; a page carries three bodies
	// on top of that. The old ordering — the whole plan being the larger — was the
	// second defect: a no-range json read returned every body.
	assert.Greater(t, renderedSize(t, page), renderedSize(t, whole),
		"a page carries bodies and a no-range read carries none, so the page is the larger")
	for _, c := range decodeJSONAssemble(t, whole).Root.Children {
		if kgtypes.NodeType(c.Type) == kgtypes.NodePlanSection {
			assert.True(t, c.BodyOmitted, "a no-range read marks every body omitted")
			assert.Empty(t, c.Description)
		}
	}

	got := decodeJSONAssemble(t, page)
	var inRange, omitted []string
	for _, c := range got.Root.Children {
		if kgtypes.NodeType(c.Type) != kgtypes.NodePlanSection {
			continue
		}
		if c.BodyOmitted {
			omitted = append(omitted, c.Name)
			assert.Empty(t, c.Description, "an omitted section carries no body")
		} else {
			inRange = append(inRange, c.Name)
			assert.NotEmpty(t, c.Description, "a section in the range carries its body in full")
		}
		// EVERY section keeps its row and its size, in or out of the range: that
		// is what makes this a PAGE rather than a subset, because a reader can see
		// what they did not ask for and ask for it next.
		assert.Positive(t, c.BodyBytes, "every section reports its size so a reader can choose its pages")
	}
	assert.Equal(t, []string{"Section 2", "Section 3", "Section 4"}, inRange)
	assert.Len(t, omitted, 7, "the other seven keep their rows with their bodies dropped")
}

// TestAssembleJSON_InvalidRangeErrorsAsTheTextPathDoes pins that the two formats
// answer a bad range identically — they call the same resolver, so a caller
// cannot get a refusal in one format and a silent something in the other.
func TestAssembleJSON_InvalidRangeErrorsAsTheTextPathDoes(t *testing.T) {
	w := newWireFixture()
	seedFromBuilder(t, w, tenSectionPlan(), "plan-jsonbad")

	for _, args := range []string{
		`{"id":"plan-jsonbad","format":"json","section_start":8,"section_end":12}`,
		`{"id":"plan-jsonbad","format":"json","section_start":4,"section_end":2}`,
		`{"id":"plan-jsonbad","format":"json","section_start":-1}`,
	} {
		t.Run(args, func(t *testing.T) {
			jsonRes := handleAssemble(w, args)
			require.True(t, jsonRes.IsError, "an invalid range must error in json too: %s", toolText(jsonRes))
			textRes := handleAssemble(w, strings.Replace(args, `,"format":"json"`, "", 1))
			require.True(t, textRes.IsError)
			assert.Equal(t, toolText(textRes), toolText(jsonRes),
				"both formats share resolveSectionRange, so the refusal text must be the same")
		})
	}
}

// TestAssembleJSON_PagesTheLargestMeasuredPlanUnderTheCap is R4's observation for
// the json format, which had none: the whole plan in one json read is ABOVE the
// proven-spill point, and a page of it is below the proven-good one.
//
// THE TWO NUMBERS ARE THE POINT. Before this change the range was dropped, so the
// second read returned the first read's bytes and a json consumer had no way to
// read a large plan at all.
func TestAssembleJSON_PagesTheLargestMeasuredPlanUnderTheCap(t *testing.T) {
	const (
		provenGood  = 44000
		provenSpill = 75059
	)
	sizes := []int{12986, 9000, 8000, 8000, 7500, 7500, 7000, 6000, 5000, 3562}
	args := projects.PlanArgs{Name: "largest", Goal: "g", Summary: "s"}
	for i, n := range sizes {
		args.Sections = append(args.Sections, projects.SectionArgs{
			Name:    "Section " + strconv.Itoa(i),
			Body:    "BODY-" + strconv.Itoa(i) + " " + strings.Repeat("q", n-len("BODY-0 ")),
			Summary: "summary " + strconv.Itoa(i),
		})
	}
	w := newWireFixture()
	seedFromBuilder(t, w, args, "plan-jsonbig")

	// THE NO-RANGE READ IS NOW SMALL, and that is the fix rather than an accident
	// of the fixture: it returns the index and the tree, which is what the schema,
	// help(assemble) and the guide all say it returns. Before this it returned
	// every body — 76,093 bytes on this fixture, ABOVE the proven-spill point, so
	// a caller doing exactly what the documentation says got the outcome the
	// paging requirement exists to prevent.
	noRange := handleAssemble(w, `{"id":"plan-jsonbig","format":"json"}`)
	assert.Less(t, renderedSize(t, noRange), provenGood,
		"a no-range json read returns the index and tree, not every body")

	// AND THE PLAN IS GENUINELY LARGE, measured by something the fix does not
	// move: the sizes the sections report. Without this the assertion above would
	// pass just as well on a tiny plan, and would be measuring the fixture rather
	// than the behavior.
	var totalBody int
	for _, c := range decodeJSONAssemble(t, noRange).Root.Children {
		if kgtypes.NodeType(c.Type) == kgtypes.NodePlanSection {
			assert.True(t, c.BodyOmitted, "a no-range read marks each body omitted rather than leaving it silently absent")
			assert.Empty(t, c.Description)
			totalBody += c.BodyBytes
		}
	}
	// 74,548 is the largest prefill body measured on this project, which is what
	// this fixture is sized to. It is BELOW the 75,059 proven-spill point on its
	// own — the json envelope around it is what carried the whole read over, to a
	// measured 76,093. So the number to assert against is the proven-GOOD point:
	// these bodies cannot be returned in one read whatever the format, which is
	// what makes the no-range default above load-bearing rather than cosmetic.
	assert.Equal(t, 74548, totalBody, "the fixture is sized to the largest prefill body measured on this project")
	assert.Greater(t, totalBody, provenGood,
		"the plan's bodies alone are far past the point a single read can carry, so a no-range read must not return them")

	seen := map[string]bool{}
	for lo := 0; lo < 10; lo += 2 {
		hi := min(lo+1, 9)
		res := handleAssemble(w, `{"id":"plan-jsonbig","format":"json","section_start":`+
			strconv.Itoa(lo)+`,"section_end":`+strconv.Itoa(hi)+`}`)
		require.False(t, res.IsError, "%s", toolText(res))
		assert.Less(t, renderedSize(t, res), provenGood,
			"json page [%d,%d] must fit under the proven-good point", lo, hi)
		for _, c := range decodeJSONAssemble(t, res).Root.Children {
			if c.BodyOmitted || kgtypes.NodeType(c.Type) != kgtypes.NodePlanSection {
				continue
			}
			seen[c.Name] = true
		}
	}
	assert.Len(t, seen, 10, "every section of the largest measured plan is reachable in bounded json pages")
}

// TestAssembleJSON_CarriesAnnotationState is the other half: a json reader can
// tell a reviewed section from an unreviewed one.
func TestAssembleJSON_CarriesAnnotationState(t *testing.T) {
	w := newWireFixture()
	ids := seedFromBuilder(t, w, tenSectionPlan(), "plan-jsonann")
	sectionID := ids[1]
	for _, a := range []struct{ id, kind, tier string }{
		{"ja-1", kgtypes.AnnotationKindFinding, "T2"},
		{"ja-2", kgtypes.AnnotationKindFinding, "T3"},
		{"ja-3", kgtypes.AnnotationKindCorrect, ""},
	} {
		n := &knowledgev1.Node{
			Id: a.id, Type: string(kgtypes.NodePlanAnnotation),
			SymbolName: "note " + a.id, Summary: "a reviewer note",
		}
		kgtypes.SetValue(n, kgtypes.AnnotationKindKey, a.kind)
		if a.tier != "" {
			kgtypes.SetValue(n, kgtypes.AnnotationTierKey, a.tier)
		}
		w.addNode(n)
		w.addEdge(a.id, sectionID, string(kgtypes.EdgeRelatesTo), kgtypes.AnnotationEdgeMethod, "")
	}

	got := decodeJSONAssemble(t, handleAssemble(w, `{"id":"plan-jsonann","format":"json"}`))
	var annotated, bare int
	for _, c := range got.Root.Children {
		if kgtypes.NodeType(c.Type) != kgtypes.NodePlanSection {
			continue
		}
		if c.ID != sectionID {
			assert.Nil(t, c.Annotations,
				"a section with no annotations emits NO key — a zero would change the bytes of every plan written before annotations existed")
			bare++
			continue
		}
		annotated++
		require.NotNil(t, c.Annotations, "the annotated section must carry its review state")
		assert.Equal(t, 3, c.Annotations.Count)
		assert.Equal(t, map[string]int{kgtypes.AnnotationKindFinding: 2, kgtypes.AnnotationKindCorrect: 1},
			c.Annotations.Kinds, "the kinds are counted, so a reader can rank sections by unresolved findings")
	}
	assert.Equal(t, 1, annotated)
	assert.Equal(t, 9, bare, "the control: nine sections carry no key at all")
}
