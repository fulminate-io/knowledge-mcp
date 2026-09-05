// SPDX-License-Identifier: Apache-2.0

package render_test

// section_reads_test.go is the SEAM test for the chunked plan: a graph written
// by the real projects.BuildPlanGraph, read back through every render path by
// the real render.Handle. BOTH SIDES ARE PRODUCTION CODE — no double stands in
// for the builder and none for the readers; the only stand-in is the wire
// itself, which carries every edge field exactly as the decode does.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// seedFromBuilder runs the REAL projects.BuildPlanGraph and loads its nodes and
// edges into the wire fixture, assigning ids the way PersistBatch does.
func seedFromBuilder(t *testing.T, w *wireFixture, args projects.PlanArgs, rootID string) []string {
	t.Helper()
	nodes, edges, err := projects.BuildPlanGraph(args, nil, nil)
	require.NoError(t, err)
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		id := rootID
		if i > 0 {
			id = rootID + "-n" + strconv.Itoa(i)
		}
		ids[i] = id
		n.Id = id
		w.addNode(n)
	}
	for _, e := range edges {
		if e.FromIdx < 0 || e.ToIdx < 0 {
			continue // an edge to an id outside the batch; unused by these fixtures.
		}
		w.addEdge(ids[e.FromIdx], ids[e.ToIdx], string(e.Type), e.Method, e.Evidence)
	}
	return ids
}

func tenSectionPlan() projects.PlanArgs {
	args := projects.PlanArgs{Name: "chunked", Goal: "the goal", Summary: "s"}
	for i := range 10 {
		args.Sections = append(args.Sections, projects.SectionArgs{
			Name:    "Section " + strconv.Itoa(i),
			Body:    "BODY-" + strconv.Itoa(i) + " " + strings.Repeat("x", 100),
			Summary: "summary " + strconv.Itoa(i),
		})
	}
	return args
}

func handleAssemble(gc render.GraphCaller, args string) kgtools.ToolResult {
	return render.Handle(context.Background(), gc, json.RawMessage(args))
}

// S3 / R1-e: the section order survives every read path.
//
// THE SECTIONS ARE SEEDED IN REVERSE with explicit positions, so the ARRIVAL
// order and the INTENDED order disagree. An assertion over a plan whose sections
// happen to arrive in order proves nothing: it passes against a renderer that
// ignores position entirely.
func TestSectionedPlan_OrderHoldsOnEveryReadPath(t *testing.T) {
	base := tenSectionPlan()
	reversed := projects.PlanArgs{Name: base.Name, Goal: base.Goal, Summary: base.Summary}
	for i, s := range slices.Backward(base.Sections) {
		pos := i
		s.Position = &pos
		reversed.Sections = append(reversed.Sections, s)
	}
	w := newWireFixture()
	seedFromBuilder(t, w, reversed, "plan-1")

	want := make([]string, 10)
	for i := range want {
		want[i] = "Section " + strconv.Itoa(i)
	}

	t.Run("assemble text", func(t *testing.T) {
		assert.Equal(t, want, orderOfNames(toolText(handleAssemble(w, `{"id":"plan-1"}`)), want))
	})

	t.Run("assemble json", func(t *testing.T) {
		var payload struct {
			Root struct {
				Children []struct {
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"children"`
			} `json:"root"`
		}
		res := handleAssemble(w, `{"id":"plan-1","format":"json"}`)
		require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload))
		var got []string
		for _, c := range payload.Root.Children {
			if c.Type == string(kgtypes.NodePlanSection) {
				got = append(got, c.Name)
			}
		}
		assert.Equal(t, want, got,
			"the json assemble walks childIndex directly with no topo-sort, so childIndex order IS the json order")
	})

	t.Run("the child index every consumer shares", func(t *testing.T) {
		childIndex, _, _, _ := render.AssembleSubtree(context.Background(), w, "plan-1", 4)
		var got []string
		for _, c := range childIndex["plan-1"] {
			got = append(got, c.SymbolName)
		}
		assert.Equal(t, want, got)
	})

	t.Run("the section index block", func(t *testing.T) {
		body := toolText(handleAssemble(w, `{"id":"plan-1"}`))
		require.Contains(t, body, "## Sections")
		assert.Equal(t, want, orderOfNames(sliceFrom(t, body, "## Sections"), want))
	})
}

// R4-a / R4-b: the read SIZES, observed through the assemble arm's own
// rendered-bytes disclosure.
//
// THE PROVEN-GOOD POINT IS 44,000 BYTES. The harness cap is bracketed between
// 44,000 (proven to fit) and 75,059 (proven to spill); a figure BETWEEN them is
// indeterminate and is never written as a pass here.
func TestSectionedPlan_ReadsFitTheCap(t *testing.T) {
	const provenGood = 44000

	w := newWireFixture()
	// The largest section measured on a real reviewed plan, carrying the eight
	// annotations that plan's largest section actually carried, at their measured
	// summary and body weights.
	big := projects.PlanArgs{Name: "big", Goal: "g", Summary: "s", Sections: []projects.SectionArgs{
		{Name: "WHAT TO TEST", Body: strings.Repeat("y", 12986), Summary: "what to test"},
	}}
	ids := seedFromBuilder(t, w, big, "plan-2")
	sectionID := ids[1]
	for i := range 8 {
		annID := "ann-" + strconv.Itoa(i)
		n := &knowledgev1.Node{
			Id: annID, SymbolName: "annotation " + strconv.Itoa(i),
			Type:        string(kgtypes.NodePlanAnnotation),
			Summary:     strings.Repeat("z", 379),  // 8 x 379 ≈ the measured 3,034 bytes of summary.
			Description: strings.Repeat("w", 3331), // 8 x 3,331 ≈ the measured 26,652 bytes of BODY.
		}
		kgtypes.SetValue(n, kgtypes.AnnotationKindKey, kgtypes.AnnotationKindFinding)
		kgtypes.SetValue(n, kgtypes.AnnotationTierKey, "T2")
		kgtypes.SetValue(n, kgtypes.AnnotationLaneKey, "rv-1")
		w.addNode(n)
		w.addEdge(annID, sectionID, string(kgtypes.EdgeRelatesTo), "", "")
	}

	t.Run("R4-a one section with its annotations, at 1x and at 2x", func(t *testing.T) {
		size := renderedSize(t, handleAssemble(w, `{"id":"`+sectionID+`"}`))
		assert.Less(t, size, provenGood, "a section read must fit under the proven-good point")
		assert.Less(t, size*2, provenGood,
			"and at TWICE the measured sizes — this is the clause that forces annotation SUMMARIES rather than inlined bodies")

		// THE CONTROL THAT SHOWS THE DESIGN IS LOAD-BEARING: the eight annotation
		// BODIES alone are 26,648 bytes, so a section read that inlined them would
		// be 39,634 at 1x and 79,268 at 2x — the second past the proven-spill
		// point. The read must therefore NOT contain a body.
		body := toolText(handleAssemble(w, `{"id":"`+sectionID+`"}`))
		assert.NotContains(t, body, strings.Repeat("w", 100),
			"a section read carries annotation summaries and ids, never annotation bodies")
		assert.Contains(t, body, "ann-0", "and it carries the id with which to fetch one")
	})

	t.Run("R4-b the root with its tree, at 1x and at 2x", func(t *testing.T) {
		ten := newWireFixture()
		seedFromBuilder(t, ten, tenSectionPlan(), "plan-3")
		size := renderedSize(t, handleAssemble(ten, `{"id":"plan-3"}`))
		assert.Less(t, size, provenGood)
		assert.Less(t, size*2, provenGood)
	})
}

// R4-c through the ARM: the range returns exactly its sections, and every
// invalid range errors naming the bound rather than clamping.
func TestSectionedPlan_AssembleRange(t *testing.T) {
	w := newWireFixture()
	seedFromBuilder(t, w, tenSectionPlan(), "plan-4")

	body := toolText(handleAssemble(w, `{"id":"plan-4","section_start":2,"section_end":4}`))
	// THE ASSERTION IS SCOPED TO THE RANGE BLOCK, not to the whole result. The
	// tree at the top of a plan assemble names every section with its
	// description truncated to 120 characters, which is the index doing its job;
	// what the range governs is which section BODIES are returned in full below
	// it. Asserting over the whole result would test the index, not the range.
	rangeBlock := sliceFrom(t, body, "## Section 2:")
	for _, want := range []string{"BODY-2 ", "BODY-3 ", "BODY-4 "} {
		assert.Contains(t, rangeBlock, want)
	}
	for _, unwanted := range []string{"BODY-0 ", "BODY-1 ", "BODY-5 ", "BODY-9 "} {
		assert.NotContains(t, rangeBlock, unwanted, "a range returns exactly its sections and no neighbors")
	}
	assert.Equal(t, 3, strings.Count(body, "\n## Section "), "exactly three section bodies are returned")

	t.Run("an out-of-bounds range errors naming the bound", func(t *testing.T) {
		res := handleAssemble(w, `{"id":"plan-4","section_start":8,"section_end":12}`)
		require.True(t, res.IsError, "an out-of-bounds range must error, never clamp: %s", toolText(res))
		assert.Contains(t, toolText(res), "section_end")
		assert.Contains(t, toolText(res), "10 sections")
	})

	t.Run("an inverted range errors", func(t *testing.T) {
		res := handleAssemble(w, `{"id":"plan-4","section_start":4,"section_end":2}`)
		require.True(t, res.IsError)
		assert.Contains(t, toolText(res), "inverted")
	})

	t.Run("R4-e the whole plan pages completely and the pages tile", func(t *testing.T) {
		seen := map[string]bool{}
		for lo := 0; lo < 10; lo += 3 {
			hi := min(lo+2, 9)
			page := handleAssemble(w, fmt.Sprintf(`{"id":"plan-4","section_start":%d,"section_end":%d}`, lo, hi))
			require.False(t, page.IsError, "%s", toolText(page))
			assert.Less(t, renderedSize(t, page), 44000, "no page is above the proven-good point")
			pageText := toolText(page)
			at := strings.Index(pageText, "\n## Section ")
			require.GreaterOrEqual(t, at, 0, "every page carries at least one section body")
			bodies := pageText[at:]
			for i := lo; i <= hi; i++ {
				marker := "BODY-" + strconv.Itoa(i) + " "
				require.Contains(t, bodies, marker, "page [%d,%d] must carry section %d", lo, hi, i)
				seen[marker] = true
			}
		}
		assert.Len(t, seen, 10, "every section is reachable in bounded pages")
	})
}

// R4-e AT THE MEASURED SCALE. The paging test above proves the pages TILE — every
// section reachable, none duplicated across a page boundary — on a plan small
// enough that tiling is the only thing at issue. This one proves the SIZE clause
// on the largest plan actually measured: 74,548 bytes of body, which is the
// figure that SPILLED the harness as a single read at 75,059 characters.
//
// THE TWO ARE SEPARATE TESTS BECAUSE THEY FAIL FOR DIFFERENT REASONS. A tiling
// defect is an off-by-one in the range resolver; a size defect is a page that
// carries too many sections. Running them on one fixture would let a fix for
// either mask the other.
//
// THE PAGE SIZE IS ASSERTED AGAINST THE PROVEN-GOOD POINT, 44,000, and never
// against the bracket between it and the proven-spill 75,059: a figure inside
// that bracket is INDETERMINATE, and writing one down as a pass is the reading
// this whole requirement exists to prevent.
func TestSectionedPlan_LargestMeasuredPlanPagesUnderTheCap(t *testing.T) {
	// Ten sections summing to 74,548 bytes — the largest prefill body measured,
	// laid out with one oversized section so the pages are not uniform.
	const totalBody = 74548
	sizes := []int{12986, 9000, 8000, 8000, 7500, 7500, 7000, 6000, 5000, 3562}
	sum := 0
	for _, n := range sizes {
		sum += n
	}
	require.Equal(t, totalBody, sum, "the fixture must weigh exactly the measured plan")

	args := projects.PlanArgs{Name: "largest", Goal: "g", Summary: "s"}
	for i, n := range sizes {
		args.Sections = append(args.Sections, projects.SectionArgs{
			Name:    "Section " + strconv.Itoa(i),
			Body:    "BODY-" + strconv.Itoa(i) + " " + strings.Repeat("q", n-len("BODY-0 ")),
			Summary: "summary " + strconv.Itoa(i),
		})
	}
	w := newWireFixture()
	seedFromBuilder(t, w, args, "plan-big")

	// The CONTROL that makes the per-page assertions meaningful: the whole plan
	// in ONE page is over the proven-good point, so a page that fits is the
	// paging working rather than the plan being small.
	whole := handleAssemble(w, `{"id":"plan-big","section_start":0,"section_end":9}`)
	require.False(t, whole.IsError, "%s", toolText(whole))
	assert.Greater(t, renderedSize(t, whole), 44000,
		"the whole plan in one page must exceed the proven-good point, or this fixture proves nothing about paging")

	seen := map[string]bool{}
	for lo := 0; lo < 10; lo += 2 {
		hi := min(lo+1, 9)
		page := handleAssemble(w, fmt.Sprintf(`{"id":"plan-big","section_start":%d,"section_end":%d}`, lo, hi))
		require.False(t, page.IsError, "%s", toolText(page))
		assert.Less(t, renderedSize(t, page), 44000,
			"page [%d,%d] must fit under the proven-good point", lo, hi)
		bodies := toolText(page)
		at := strings.Index(bodies, "\n## Section ")
		require.GreaterOrEqual(t, at, 0)
		for i := lo; i <= hi; i++ {
			marker := "BODY-" + strconv.Itoa(i) + " "
			require.Contains(t, bodies[at:], marker, "page [%d,%d] must carry section %d", lo, hi, i)
			seen[marker] = true
		}
	}
	assert.Len(t, seen, 10, "every section of the largest measured plan is reachable in bounded pages")
}

// The plan_section arm: assemble of a SECTION id renders that section rather
// than the generic fallback body.
func TestAssemble_PlanSectionArm(t *testing.T) {
	w := newWireFixture()
	ids := seedFromBuilder(t, w, tenSectionPlan(), "plan-5")
	body := toolText(handleAssemble(w, `{"id":"`+ids[1]+`"}`))
	assert.Contains(t, body, "# Section: Section 0")
	assert.Contains(t, body, "Position: 0")
	assert.Contains(t, body, "BODY-0 ")
}

// R3-a / R3-d / R3-e / R3-f end to end: an annotation attaches to the SECTION,
// the section read returns it, the tree shows its kind and count, and it never
// becomes a tree child.
func TestSectionedPlan_AnnotationsOnSections(t *testing.T) {
	w := newWireFixture()
	ids := seedFromBuilder(t, w, tenSectionPlan(), "plan-6")
	sectionID := ids[1]
	ann := &knowledgev1.Node{
		Id: "ann-x", SymbolName: "the tier is missing on R6-d",
		Type: string(kgtypes.NodePlanAnnotation), Summary: "R6-d does not name its tier",
		Description: "REPLACEMENT: ...the exact text...",
	}
	kgtypes.SetValue(ann, kgtypes.AnnotationKindKey, kgtypes.AnnotationKindNeededChange)
	kgtypes.SetValue(ann, kgtypes.AnnotationReplacementKey, "the exact text")
	kgtypes.SetValue(ann, kgtypes.AnnotationLaneKey, "rv-plan-structure")
	w.addNode(ann)
	w.addEdge("ann-x", sectionID, string(kgtypes.EdgeRelatesTo), "", "")

	// A SECOND ANNOTATION, OF KIND finding WITH A TIER, on the same section. The
	// section read renders four fields per annotation — kind, summary, tier and
	// lane — and until this fixture carried a tier, three of the four were pinned
	// and the tier was observed only on the EDGE carrier. Dropping it from the
	// node read left this whole package green, so a regression that stopped
	// rendering an annotation's severity on the read a reviewer actually uses
	// would have shipped.
	sev := &knowledgev1.Node{
		Id: "ann-sev", SymbolName: "the caller census is short by two",
		Type: string(kgtypes.NodePlanAnnotation), Summary: "the caller census is short by two",
	}
	kgtypes.SetValue(sev, kgtypes.AnnotationKindKey, kgtypes.AnnotationKindFinding)
	kgtypes.SetValue(sev, kgtypes.AnnotationTierKey, "T2")
	kgtypes.SetValue(sev, kgtypes.AnnotationLaneKey, "acr-plan-structure")
	w.addNode(sev)
	w.addEdge("ann-sev", sectionID, string(kgtypes.EdgeRelatesTo), "", "")

	t.Run("R3-d a section read returns its annotations", func(t *testing.T) {
		body := toolText(handleAssemble(w, `{"id":"`+sectionID+`"}`))
		assert.Contains(t, body, "## Annotations (2)")
		assert.Contains(t, body, "[needed change] R6-d does not name its tier")
		assert.Contains(t, body, "lane rv-plan-structure")
		assert.Contains(t, body, "ID: ann-x")

		// THE SEVERITY ON THE READ PATH, not just on the edge. A finding with no
		// tier is a concern with no severity, and a reader who cannot see the
		// severity on the read they use cannot triage from it.
		assert.Contains(t, body, "[finding] the caller census is short by two")
		assert.Contains(t, body, "tier T2")
		assert.Contains(t, body, "lane acr-plan-structure")
		assert.Contains(t, body, "ID: ann-sev")
	})

	t.Run("R3-e the tree shows the kind and count", func(t *testing.T) {
		body := toolText(handleAssemble(w, `{"id":"plan-6"}`))
		assert.Contains(t, body, "annotations: 2 (finding 1, needed change 1)")
		// And ONLY on the annotated section: the other nine carry no line.
		assert.Equal(t, 2, strings.Count(body, "annotations: 2 (finding 1, needed change 1)"),
			"the line appears on the section's tree row and its index row, and on no other section")
	})

	t.Run("R3-f annotations are not tree children", func(t *testing.T) {
		childIndex, _, _, _ := render.AssembleSubtree(context.Background(), w, "plan-6", 4)
		for parent, children := range childIndex {
			for _, c := range children {
				assert.NotEqual(t, string(kgtypes.NodePlanAnnotation), c.Type,
					"an annotation must never enter the contains index (found under %s)", parent)
			}
		}
		// The fixture HAS an annotation, so the emptiness above is a real absence
		// rather than an empty graph.
		require.Contains(t, sortedSectionNames(w.nodes), "Section 0")
		assert.Contains(t, toolText(handleAssemble(w, `{"id":"plan-6"}`)), "annotations: 2")
	})
}

// R6-e / R6-f: a phase-and-step plan renders BYTE-IDENTICALLY beside a sectioned
// one in the same graph, through all four reads.
func TestPhasePlan_RendersUnchangedAlongsideSections(t *testing.T) {
	// ONE definition of the phase-and-step shape, shared with the parity file's
	// range cell, so the two cannot describe different plans.
	phaseArgs := phaseAndStepPlan()

	alone := newWireFixture()
	seedFromBuilder(t, alone, phaseArgs, "phase-1")

	mixed := newWireFixture()
	seedFromBuilder(t, mixed, phaseArgs, "phase-1")
	seedFromBuilder(t, mixed, tenSectionPlan(), "sect-1")

	for _, args := range []string{
		`{"id":"phase-1"}`,
		`{"id":"phase-1","format":"json"}`,
	} {
		t.Run(args, func(t *testing.T) {
			assert.Equal(t, toolText(handleAssemble(alone, args)), toolText(handleAssemble(mixed, args)),
				"a phase plan renders byte-identically whether or not sectioned plans share its graph")
		})
	}

	t.Run("no annotation line anywhere in a phase plan's render", func(t *testing.T) {
		body := toolText(handleAssemble(alone, `{"id":"phase-1"}`))
		assert.NotContains(t, body, "annotations:")
		assert.NotContains(t, body, "## Sections")
	})
}

// orderOfNames returns the wanted names in the order they first appear in body.
func orderOfNames(body string, want []string) []string {
	type hit struct {
		name string
		at   int
	}
	var hits []hit
	for _, n := range want {
		if at := strings.Index(body, n); at >= 0 {
			hits = append(hits, hit{n, at})
		}
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].at < hits[j-1].at; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.name)
	}
	return out
}

// renderedSize reads the byte count off the assemble arm's own rendered-size
// disclosure block, which is the observation R4 is written against.
func renderedSize(t *testing.T, res kgtools.ToolResult) int {
	t.Helper()
	require.NotEmpty(t, res.Content)
	last := res.Content[len(res.Content)-1].Text
	var n int
	_, err := fmt.Sscanf(last, "%d rendered bytes.", &n)
	require.NoError(t, err, "the last block must be the rendered-size disclosure, got %q", last)
	return n
}

// sliceFrom returns body from the first occurrence of marker, FAILING THE NAMED
// TEST when the marker is absent instead of slicing on a -1.
//
// WHY IT EXISTS. Four assertions in this package sliced with a bare
// strings.Index and no bound check. That is fine while they pass and disastrous
// the moment they do not: a missing marker makes the slice panic, the panic takes
// down the package binary, and the run reports the tests that had already
// finished as PASS while every test after the panic never runs at all. Under one
// seeded regression this package reported 182 PASS and 1 FAIL with the section
// render tests silently unexecuted — a regression that hides the blast radius of
// the regression.
//
// A test helper's job on bad input is to fail ITS OWN test loudly, which is what
// require.FailNow does here.
func sliceFrom(t *testing.T, body, marker string) string {
	t.Helper()
	at := strings.Index(body, marker)
	require.GreaterOrEqualf(t, at, 0,
		"the render is missing the %q block this assertion slices from — failing here rather than panicking on a "+
			"negative index, which would abort the package and hide every test after it. Rendered:\n%s", marker, body)
	return body[at:]
}

// toolText joins a result's blocks, the way a caller reads them.
func toolText(res kgtools.ToolResult) string {
	var sb strings.Builder
	for _, b := range res.Content {
		sb.WriteString(b.Text)
	}
	return sb.String()
}
