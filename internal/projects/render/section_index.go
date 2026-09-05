// SPDX-License-Identifier: Apache-2.0

package render

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// section_index.go renders a sectioned plan's index: every section with its
// position, its id, its SIZE and its annotation state, so a reader can choose
// which sections to page in before spending a read on any of them.
//
// THE INDEX IS NOT STORED ANYWHERE. It is computed here from the children the
// subtree traversal has already hydrated, and that is the design rather than an
// optimization: an index stored on the root would go stale the moment a section
// body changed, or would force a root write on every section edit — which is
// exactly the whole-plan rewrite the chunked shape exists to remove. The
// positioned containment edges ARE the index; this function renders it.
//
// THE SIZE IS THEREFORE ALWAYS CURRENT by construction. len(child.Description)
// on a node the traversal already returned costs nothing and cannot disagree
// with the body it measures.

// renderSectionIndex appends the `## Sections` block for a sectioned plan.
// Renders NOTHING for a plan with no sections, so a phase-and-step plan's
// assemble output is byte-identical to what it was before sections existed.
func renderSectionIndex(sb *strings.Builder, children []*knowledgev1.Node, annotations map[string][]SectionAnnotation) {
	sections := make([]*knowledgev1.Node, 0, len(children))
	for _, c := range children {
		if kgtypes.NodeType(c.GetType()) == kgtypes.NodePlanSection {
			sections = append(sections, c)
		}
	}
	if len(sections) == 0 {
		return
	}
	// The children arrive in childIndex order, which BuildChildIndex has already
	// sorted by position — the section order is not recomputed here, so the index
	// and the tree above it cannot disagree.
	sb.WriteString("\n## Sections\n\n")
	for _, s := range sections {
		fmt.Fprintf(sb, "- [%s] %s — %d bytes — ID: %s\n",
			kgtypes.Value(s, "position"), s.SymbolName, len(s.Description), s.Id)
		if line := AnnotationLine(kindsOf(annotations[s.Id])); line != "" {
			fmt.Fprintf(sb, "  %s\n", line)
		}
	}
}

// kindsOf projects the annotation rows onto their kinds for the census.
func kindsOf(annotations []SectionAnnotation) []string {
	out := make([]string, 0, len(annotations))
	for _, a := range annotations {
		out = append(out, a.Kind)
	}
	return out
}
