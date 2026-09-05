// SPDX-License-Identifier: Apache-2.0

package projects

// builders_section.go holds the CHUNKED-PLAN half of the plan builder: the one
// section subtree and the position payload its containment edge carries.
//
// IT IS A SEPARATE FILE because builders.go is at the repository's 500-line
// per-file budget, and because the sectioned shape's rules read as one set — the
// two position carriers, the edge type, and the depends-on edge that must NOT be
// emitted.

import (
	"encoding/json"
	"fmt"
	"strconv"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// sectionEdgeMethod tags a root→section containment edge with how it was
// derived, mirroring what the raw collectors stamp on their positioned document
// edges. It is descriptive metadata: nothing reads it to decide ordering, which
// is read from the position carriers alone.
const sectionEdgeMethod = "plan-section"

// appendSectionSubtree appends ONE plan section: a plan_section node carrying the
// section body as its description, and a root→section `contains` edge carrying
// the section's zero-based position.
//
// THE POSITION RIDES TWO CARRIERS WITH ONE MEANING — the edge's Evidence as
// {"position":"N"} and the node's own `position` metadata key — because that is
// the shape the raw collectors already stamp and the shape the order key already
// reads, node first. Writing only one carrier would work today and diverge the
// moment a reader consults the other.
//
// THE EDGE TYPE IS kgtypes.EdgeKGContains, the LOWERCASE knowledge `contains`.
// The subtree traversal every tree render rides asks for that type by name, so
// the uppercase code-graph CONTAINS would persist happily and make every section
// invisible in every tree.
//
// NO DEPENDS-ON EDGE IS EMITTED, deliberately, and this is the one line a reader
// copying appendPhaseSubtree or appendStepSubtree would get wrong. Both of those
// chain their children with a depends-on edge for sequential ordering. The tree
// renderer's topological sort runs AFTER the child index is read and a
// depends-on chain outranks the index order, so a chained section list would
// silently override every position — no error, no warning, just a tree in a
// different order from the one the caller wrote.
func appendSectionSubtree(planIdx, arrayIdx int, section SectionArgs, nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) ([]*knowledgev1.Node, []kgwire.BatchEdge, error) {
	position := arrayIdx
	if section.Position != nil {
		position = *section.Position
	}
	posText := strconv.Itoa(position)
	evidence, err := sectionPositionEvidence(posText)
	if err != nil {
		return nodes, edges, fmt.Errorf("section %q: %w", section.Name, err)
	}

	sectionIdx := len(nodes)
	sectionNode := &knowledgev1.Node{
		Type:        string(kgtypes.NodePlanSection),
		Source:      "llm:claude",
		SymbolName:  section.Name,
		Description: section.Body,
		Summary:     section.Summary,
		Status:      kgtypes.StatusActive,
	}
	kgtypes.SetValue(sectionNode, "position", posText)
	nodes = append(nodes, sectionNode)

	edges = append(edges, kgwire.BatchEdge{
		FromIdx:  planIdx,
		ToIdx:    sectionIdx,
		Type:     kgtypes.EdgeKGContains,
		Method:   sectionEdgeMethod,
		Evidence: evidence,
	})
	return nodes, edges, nil
}

// sectionPositionEvidence renders the containment edge's position payload.
//
// IT IS BUILT BY THE JSON ENCODER rather than by string concatenation so the
// payload cannot be malformed by a section index that is somehow not a plain
// integer, and so the shape stays the one the readers parse.
//
// IT RETURNS AN ERROR RATHER THAN AN EMPTY STRING, and the distinction is the
// whole reason it has a second result. An empty Evidence is not a degraded
// position — it is INDISTINGUISHABLE from "this edge carries no position", which
// is a legal state every unpositioned tree is in. A failed marshal absorbed into
// "" would therefore write a section that renders in arrival order with nothing
// anywhere reporting that its position was lost. The raw collectors' own emitter
// draws the same line: a failed evidence marshal NAMES the edge and FAILS the
// emit rather than writing the edge without it.
func sectionPositionEvidence(position string) (string, error) {
	blob, err := json.Marshal(struct {
		Position string `json:"position"`
	}{Position: position})
	if err != nil {
		return "", fmt.Errorf("marshal position evidence for position %q: %w", position, err)
	}
	return string(blob), nil
}
