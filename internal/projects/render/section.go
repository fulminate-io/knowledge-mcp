// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// section.go holds the chunked-plan reads: the plan_section assemble arm and the
// section-range resolver a plan assemble pages with.
//
// WHY PAGING EXISTS AT ALL. The whole point of the chunked shape is that a
// reader spends a read on the parts they need. The root with its index is a few
// hundred bytes whatever the plan's size; one section with its annotation
// summaries is a few tens of kilobytes at the sizes real plans reach; the whole
// plan is neither, and no single read is required to return it. A caller reads
// the index, chooses, and pages.

// resolveSectionRange turns the caller's optional [start, end] into a closed
// index range over count sections, or an ERROR NAMING THE RANGE.
//
// NO INVALID RANGE IS CLAMPED. Clamping hands the reader a page they did not ask
// for, with nothing in the result to say so — a caller paging by twos who asks
// for [8,12] over ten sections would receive [8,9] and have no way to tell
// whether the plan ended or their arithmetic did. Every out-of-bounds, inverted
// or negative range errors and names the offending bound and the section count,
// which is the "bad input always errors" rule applied to a read.
//
// AN ABSENT BOUND IS NOT AN INVALID ONE: no start means from the first section,
// no end means to the last, and neither supplied means the whole plan. That is
// an omission with one obvious reading, not bad input.
func resolveSectionRange(start, end *int, count int) (lo, hi int, err error) {
	if count == 0 {
		if start == nil && end == nil {
			return 0, -1, nil // nothing to page; the caller renders no sections.
		}
		return 0, 0, fmt.Errorf(
			"assemble: a section range was supplied but this plan has no sections — " +
				"section_start/section_end apply to a chunked plan, and a phase-and-step plan has no sections to range over")
	}
	lo, hi = 0, count-1
	if start != nil {
		lo = *start
	}
	if end != nil {
		hi = *end
	}
	switch {
	case lo < 0:
		return 0, 0, fmt.Errorf("assemble: section_start is %d — a section index is zero-based and cannot be negative", lo)
	case hi < 0:
		return 0, 0, fmt.Errorf("assemble: section_end is %d — a section index is zero-based and cannot be negative", hi)
	case lo >= count:
		return 0, 0, fmt.Errorf("assemble: section_start is %d but this plan has %d sections (valid indices 0..%d)", lo, count, count-1)
	case hi >= count:
		return 0, 0, fmt.Errorf("assemble: section_end is %d but this plan has %d sections (valid indices 0..%d)", hi, count, count-1)
	case lo > hi:
		return 0, 0, fmt.Errorf("assemble: section_start is %d and section_end is %d — the range is inverted; start must not exceed end", lo, hi)
	}
	return lo, hi, nil
}

// assembleSection renders ONE plan section: its header, its position, its body
// in full, and its annotations as SUMMARIES.
//
// THE ANNOTATIONS ARE SUMMARIES WITH THEIR IDS, KINDS, TIERS AND LANES — never
// full bodies — and that is what the measurement decided rather than a
// preference. On a real reviewed plan the annotations concentrate on the largest
// section: eight of nine named one 13 KB section, and those eight weigh 27 KB of
// body against 3 KB of summary. Inlining bodies fits that plan once and spills
// as soon as it grows; summaries leave room at several times the measured size.
// A reader who wants one reviewer's full reasoning fetches that annotation by the
// id printed beside it.
func assembleSection(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Section: %s\n\n", node.SymbolName)
	if pos := kgtypes.Value(node, "position"); pos != "" {
		fmt.Fprintf(&sb, "Position: %s\n", pos)
	}
	fmt.Fprintf(&sb, "ID: %s%s\n\n", node.Id, updatedSuffix(node))
	sb.WriteString(node.Description)
	if node.Description != "" && !strings.HasSuffix(node.Description, "\n") {
		sb.WriteString("\n")
	}

	annotations, truncated, err := FetchSectionAnnotations(ctx, gc, []string{node.Id})
	if err != nil {
		// Degrade rather than lose the section body the caller asked for, but
		// raise the verdict so "no annotations" and "the read did not reach them"
		// stay distinguishable.
		//
		// AND LOG THE DISCARDED ERROR, as the plan arm does. The verdict tells a
		// caller the render is incomplete; it cannot tell an operator WHY, and an
		// error that is swallowed with no record is the one failure nobody can
		// investigate after the fact.
		slog.Warn("assemble section: annotation read failed; rendering without annotations",
			"id", node.Id, "error", err)
		annotations = nil
	}
	writeAnnotationSection(&sb, annotations[node.Id])
	// The failure gets its OWN notice rather than riding the truncation one,
	// whose text names a row ceiling that did not engage and a `limit` parameter
	// this tool does not accept.
	out := AppendTruncationNotice(kgtools.TextResult(sb.String()), truncated, len(annotations[node.Id]))
	return AppendAnnotationReadFailureNotice(out, err)
}

// writeAnnotationSection appends the `## Annotations` block. A section with none
// gets NO BLOCK, matching the tree's own omit-the-line rule: an empty heading
// asserts a review happened and found nothing, which is not what an absence
// means.
func writeAnnotationSection(sb *strings.Builder, annotations []SectionAnnotation) {
	if len(annotations) == 0 {
		return
	}
	fmt.Fprintf(sb, "\n## Annotations (%d)\n\n", len(annotations))
	for _, a := range annotations {
		fmt.Fprintf(sb, "- [%s] %s\n", a.Kind, a.Summary)
		detail := make([]string, 0, 3)
		if a.Tier != "" {
			detail = append(detail, "tier "+a.Tier)
		}
		if a.Lane != "" {
			detail = append(detail, "lane "+a.Lane)
		}
		detail = append(detail, "ID: "+a.ID)
		fmt.Fprintf(sb, "  %s\n", strings.Join(detail, " — "))
	}
	sb.WriteString("\nFull annotation bodies are fetched by id: query({\"id\":\"<annotation id>\"}).\n")
}

// writeSectionRange appends the section BODIES the caller's range selects, in
// section order, with their annotations inline as summaries.
//
// THE RANGE INDEXES THE SECTION SEQUENCE, not the child list: a plan's children
// are its sections plus its open questions, and a range that counted questions
// would shift under a plan that has them. The sections arrive in childIndex
// order, which is already position-sorted, so the range and the index above it
// cannot disagree.
func writeSectionRange(
	sb *strings.Builder,
	children []*knowledgev1.Node,
	annotations map[string][]SectionAnnotation,
	start, end *int,
) error {
	sections := make([]*knowledgev1.Node, 0, len(children))
	for _, c := range children {
		if kgtypes.NodeType(c.GetType()) == kgtypes.NodePlanSection {
			sections = append(sections, c)
		}
	}
	lo, hi, err := resolveSectionRange(start, end, len(sections))
	if err != nil {
		return err
	}
	for i := lo; i <= hi; i++ {
		s := sections[i]
		fmt.Fprintf(sb, "\n## Section %s: %s\n\nID: %s\n\n", kgtypes.Value(s, "position"), s.SymbolName, s.Id)
		sb.WriteString(s.Description)
		if s.Description != "" && !strings.HasSuffix(s.Description, "\n") {
			sb.WriteString("\n")
		}
		writeAnnotationSection(sb, annotations[s.Id])
	}
	return nil
}
