// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_edge_metadata.go holds the three client-side emitters for per-edge
// metadata on the traverse surfaces, split out of render_misc.go to keep that
// file under the repo's file-length cap. Edge-metadata rendering is owned here;
// there is no server-side twin to keep in step with.
//
// Edge.Method POPULATIONS ARE KEYED BY EDGE TYPE.
//
// Two of the three emitters withhold Method for a multi-candidate GROUP MEMBER,
// because its group block already states it. That suppression is about group
// members specifically and says nothing about what Method means generally: the
// field holds several populations keyed by the edge type carrying them, kgtypes
// (edge_types.go) is the vocabulary source, and every value outside the two
// group constants renders here as an ordinary method. So this surface prints a
// bound reference edge's resolution rung, and will print whatever population a
// further edge type gains, with no edit to this file. See IsCandidateEdge for
// why the suppression keys on an equality against the group constants rather
// than on Method being non-empty.

// writeEdgeMetadataSection renders the per-edge metadata block client-side,
// reading the []knowledgev1.Edge fields directly. A no-op when there are no
// edges (the carrier was empty / include_edge_metadata was unset).
func writeEdgeMetadataSection(sb *strings.Builder, edges []knowledgev1.Edge, direction string) {
	if len(edges) == 0 {
		return
	}
	fmt.Fprintf(sb, "\n### Edges (%d) with metadata (direction=%s)\n\n", len(edges), direction)
	for i := range edges {
		e := &edges[i]
		fmt.Fprintf(sb, "- `%s` → `%s` [edge_type=%s]", e.FromId, e.ToId, e.Type)
		if annot := edgeMetadataAnnotation(e); annot != "" {
			fmt.Fprintf(sb, "\n    %s", annot)
		}
		if !nanosToTime(e.LastValidated).IsZero() {
			fmt.Fprintf(sb, "\n    last_validated: %s", nanosToTime(e.LastValidated).UTC().Format("2006-01-02T15:04:05Z"))
		}
		sb.WriteString("\n")
	}
}

// edgeMetadataAnnotation builds a compact "confidence=0.92 · method=manual ·
// weight=0.9 · evidence=..." annotation from the non-empty edge fields.
//
// For a MULTI-CANDIDATE GROUP MEMBER the method and the raw group key are both
// suppressed: the group block already states the method as its semantics, and
// the key is an internal join identifier this plan renders opaquely — printing it
// raw is noise that also invites a reader to parse it. Every other edge is
// byte-for-byte unchanged, which matters because a cloud or linkage edge's
// Evidence is a genuine human-readable citation ("Dockerfile:14 COPY src").
//
// BELT-AND-BRACES BY DESIGN, NOT DEAD CODE: callers now pass only the ungrouped
// remainder, so a member should never arrive here. The guards exist because this
// is a render primitive a future surface may call with an unfiltered slice, and
// the defect they prevent is silent.
func edgeMetadataAnnotation(e *knowledgev1.Edge) string {
	group := IsCandidateEdge(e)
	var parts []string
	if e.Confidence > 0 {
		parts = append(parts, fmt.Sprintf("confidence=%.2f", e.Confidence))
	}
	if e.Method != "" && !group {
		parts = append(parts, fmt.Sprintf("method=%s", e.Method))
	}
	if e.Weight > 0 {
		parts = append(parts, fmt.Sprintf("weight=%g", e.Weight))
	}
	if e.Evidence != "" && !group {
		parts = append(parts, fmt.Sprintf("evidence=%q", e.Evidence))
	}
	return strings.Join(parts, " · ")
}

// edgeMetadataJSON renders the edge-metadata rows for the JSON traversal output:
// one row per edge with from/to/edge_type plus the populated metadata fields.
//
// IT APPLIES NO GROUP GUARD, and that omission is deliberate rather than an
// oversight: a JSON consumer is joining rows rather than reading prose, so it is
// served by the raw fields including a group member's own method and key.
func edgeMetadataJSON(edges []knowledgev1.Edge) []map[string]any {
	rows := make([]map[string]any, 0, len(edges))
	for i := range edges {
		e := &edges[i]
		row := map[string]any{
			"from":      e.FromId,
			"to":        e.ToId,
			"edge_type": e.Type,
		}
		if e.Weight != 0 {
			row["weight"] = e.Weight
		}
		if e.Confidence != 0 {
			row["confidence"] = e.Confidence
		}
		if e.Method != "" {
			row["method"] = e.Method
		}
		if e.Evidence != "" {
			row["evidence"] = e.Evidence
		}
		if !nanosToTime(e.LastValidated).IsZero() {
			row["last_validated"] = nanosToTime(e.LastValidated).UTC().Format("2006-01-02T15:04:05Z")
		}
		rows = append(rows, row)
	}
	return rows
}
