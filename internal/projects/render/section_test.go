// SPDX-License-Identifier: Apache-2.0

package render

// section_test.go covers the chunked-plan READS: the plan_section assemble arm,
// the section range on a plan assemble, and the rendered-size index.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// R4-c. The range arms, all of them, over a ten-section plan. EVERY INVALID ONE
// ERRORS NAMING THE RANGE — none silently clamps, because a clamp hands a reader
// a page they did not ask for and no way to tell.
func TestSectionRange(t *testing.T) {
	cases := []struct {
		name       string
		start, end *int
		count      int
		wantIdx    []int
		wantErrSub []string
	}{
		{name: "absent range is every section", count: 10, wantIdx: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}},
		{name: "one section", start: new(3), end: new(3), count: 10, wantIdx: []int{3}},
		{name: "several sections", start: new(2), end: new(4), count: 10, wantIdx: []int{2, 3, 4}},
		{name: "all sections explicitly", start: new(0), end: new(9), count: 10, wantIdx: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}},
		{name: "start only runs to the end", start: new(8), count: 10, wantIdx: []int{8, 9}},
		{name: "end only runs from the start", end: new(1), count: 10, wantIdx: []int{0, 1}},
		{name: "start after end", start: new(4), end: new(2), count: 10, wantErrSub: []string{"section_start", "4", "section_end", "2"}},
		{name: "start beyond the last section", start: new(10), end: new(11), count: 10, wantErrSub: []string{"section_start", "10", "10 sections"}},
		{name: "end beyond the last section", start: new(8), end: new(12), count: 10, wantErrSub: []string{"section_end", "12", "10 sections"}},
		{name: "negative start", start: new(-1), count: 10, wantErrSub: []string{"section_start", "-1"}},
		{name: "a range on a plan with no sections", start: new(0), end: new(0), count: 0, wantErrSub: []string{"no sections"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lo, hi, err := resolveSectionRange(tc.start, tc.end, tc.count)
			if len(tc.wantErrSub) > 0 {
				require.Error(t, err, "an invalid range must error, never clamp")
				for _, sub := range tc.wantErrSub {
					assert.Contains(t, err.Error(), sub)
				}
				return
			}
			require.NoError(t, err)
			var got []int
			for i := lo; i <= hi; i++ {
				got = append(got, i)
			}
			assert.Equal(t, tc.wantIdx, got)
		})
	}
}

// R4-d. The section index names every section with its SIZE, and the size is
// computed at READ time from the hydrated child — so editing a body changes the
// reported size while the ROOT NODE is byte-identical. This is what binds the
// no-stored-index rule to the one-write-per-section rule.
func TestRenderSectionIndex_SizeIsComputedAtReadTime(t *testing.T) {
	sec := func(id, name, body, pos string) *knowledgev1.Node {
		n := &knowledgev1.Node{Id: id, SymbolName: name, Type: string(kgtypes.NodePlanSection), Description: body}
		kgtypes.SetValue(n, "position", pos)
		return n
	}
	before := []*knowledgev1.Node{sec("s0", "Touch points", "12345", "0"), sec("s1", "Reuse", "1234567890", "1")}

	var sb strings.Builder
	renderSectionIndex(&sb, before, nil)
	out := sb.String()
	assert.Contains(t, out, "## Sections")
	assert.Contains(t, out, "- [0] Touch points — 5 bytes — ID: s0")
	assert.Contains(t, out, "- [1] Reuse — 10 bytes — ID: s1")

	// Edit ONE body. Nothing about the root changed — there is no root to change,
	// which is the point: the index is not stored anywhere.
	after := []*knowledgev1.Node{sec("s0", "Touch points", "12345678901234567890", "0"), sec("s1", "Reuse", "1234567890", "1")}
	var sb2 strings.Builder
	renderSectionIndex(&sb2, after, nil)
	assert.Contains(t, sb2.String(), "- [0] Touch points — 20 bytes — ID: s0",
		"the reported size follows the body with no write to any index")
	assert.Contains(t, sb2.String(), "- [1] Reuse — 10 bytes — ID: s1", "the untouched section's reported size is unchanged")
}

// A plan with NO sections renders no index block at all, so a phase-and-step
// plan's assemble output is byte-identical to what it was.
func TestRenderSectionIndex_OmittedForAPhasePlan(t *testing.T) {
	var sb strings.Builder
	renderSectionIndex(&sb, []*knowledgev1.Node{
		{Id: "ph-1", SymbolName: "phase one", Type: string(kgtypes.NodePhase)},
	}, nil)
	assert.Empty(t, sb.String(), "a plan with no sections renders no Sections block")

	// CONTROL: the same function DOES render for a section, so the empty above is
	// a real omission rather than a renderer that never emits.
	var sb2 strings.Builder
	renderSectionIndex(&sb2, []*knowledgev1.Node{
		{Id: "s0", SymbolName: "Touch points", Type: string(kgtypes.NodePlanSection)},
	}, nil)
	assert.Contains(t, sb2.String(), "## Sections")
}

// The index carries each section's annotation state, so a reader choosing pages
// can see where the review activity is before spending a read.
func TestRenderSectionIndex_CarriesAnnotationState(t *testing.T) {
	var sb strings.Builder
	renderSectionIndex(&sb, []*knowledgev1.Node{
		{Id: "s0", SymbolName: "Touch points", Type: string(kgtypes.NodePlanSection)},
		{Id: "s1", SymbolName: "Reuse", Type: string(kgtypes.NodePlanSection)},
	}, map[string][]SectionAnnotation{
		"s0": {{Kind: "finding"}, {Kind: "needed change"}},
	})
	out := sb.String()
	assert.Contains(t, out, "annotations: 2 (finding 1, needed change 1)")
	// s1 has none, so it carries NO annotation line — not a zero.
	lines := strings.SplitSeq(strings.TrimRight(out, "\n"), "\n")
	for l := range lines {
		if strings.Contains(l, "Reuse") {
			continue
		}
		if strings.Contains(l, "annotations: 0") {
			t.Errorf("a section with no annotations must carry no line, got %q", l)
		}
	}
}
