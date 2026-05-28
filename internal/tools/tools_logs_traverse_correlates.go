// SPDX-License-Identifier: Apache-2.0

// Package tools — stream → correlated-template rendering for log
// traverse. Factored out of tools_logs_traverse_stream.go so stream
// traversal adds a "Correlated templates" section without ballooning
// the parent file past the 300-line cap.
//
// The data model: CORRELATES_WITH edges connect templates, not
// streams. A stream "correlates with" another stream when it shares a
// template that has an outgoing or incoming CORRELATES_WITH edge. So
// for each chunk the stream owns, we look up the template, iterate
// both directions of CORRELATES_WITH, and emit a row per unique peer
// template. Rows are sorted by score desc, then alias.
package tools

import (
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// streamCorrelation is a single row in the "Correlated templates"
// section. MyTemplate is the template the starting stream references;
// PeerTemplate is the other side of the CORRELATES_WITH edge.
type streamCorrelation struct {
	MyTemplate   *knowledgev1.Node
	PeerTemplate *knowledgev1.Node
	Score        float64
	ServiceA     string
	ServiceB     string
}

// writeStreamCorrelations renders the "Correlated with" section for
// stream traverse output. Two sub-sections:
//
//  1. Direct: correlations whose Evidence service pair includes this
//     stream's service — the answer to "what specifically co-failed
//     with this stream."
//  2. Other (via shared template): correlations whose endpoint templates
//     overlap ours but whose services are different streams'. Useful
//     context ("NodeNotReady is correlated with lots of things")
//     without claiming this specific stream is involved.
//
// Silent when the stream has no chunks or its templates have no
// CORRELATES_WITH edges.
func writeStreamCorrelations(
	sb *strings.Builder,
	st *logState,
	stream *knowledgev1.Node,
	chunks []*knowledgev1.Node,
) {
	tmplIDs := uniqueTemplateIDsFromChunks(chunks)
	if len(tmplIDs) == 0 {
		return
	}
	rows := collectStreamCorrelations(st, tmplIDs)
	if len(rows) == 0 {
		return
	}
	sortStreamCorrelations(rows)
	myService := kgtypes.Value(stream, "label:service")
	direct, indirect := partitionCorrelationsByStream(rows, myService)
	if len(direct) > 0 {
		fmt.Fprintf(sb, "### Correlated with (%d direct)\n\n", len(direct))
		sb.WriteString("Correlations whose Evidence service pair involves this stream's service (score desc):\n\n")
		for _, r := range direct {
			fmt.Fprintf(sb, "- %s ↔ %s — `%s` ↔ `%s` (score %.2f)\n",
				templateRefShort(r.MyTemplate),
				templateRefShort(r.PeerTemplate),
				serviceOrDash(r.ServiceA),
				serviceOrDash(r.ServiceB),
				r.Score)
		}
		sb.WriteString("\n")
	}
	if len(indirect) > 0 {
		fmt.Fprintf(sb, "### Shared-template correlations (%d)\n\n", len(indirect))
		sb.WriteString("Other correlations that involve one of this stream's templates but a different stream's service (score desc):\n\n")
		for _, r := range indirect {
			fmt.Fprintf(sb, "- %s ↔ %s — services `%s` ↔ `%s` (score %.2f)\n",
				templateRefShort(r.MyTemplate),
				templateRefShort(r.PeerTemplate),
				serviceOrDash(r.ServiceA),
				serviceOrDash(r.ServiceB),
				r.Score)
		}
		sb.WriteString("\n")
	}
}

// partitionCorrelationsByStream splits rows into direct (this stream's
// service appears on either side of the Evidence service pair) vs
// indirect (the correlation happens to involve a sibling stream using
// the same template). Empty myService → all rows are indirect.
func partitionCorrelationsByStream(
	rows []streamCorrelation,
	myService string,
) (direct, indirect []streamCorrelation) {
	if myService == "" {
		return nil, rows
	}
	for _, r := range rows {
		if r.ServiceA == myService || r.ServiceB == myService {
			direct = append(direct, r)
			continue
		}
		indirect = append(indirect, r)
	}
	return direct, indirect
}

// uniqueTemplateIDsFromChunks scans chunk nodes' template_id meta and
// returns each distinct ID. Chunks without the meta are skipped rather
// than erroring — legacy graphs may omit it.
func uniqueTemplateIDsFromChunks(chunks []*knowledgev1.Node) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, c := range chunks {
		tid := kgtypes.Value(c, "template_id")
		if tid == "" {
			continue
		}
		if _, ok := seen[tid]; ok {
			continue
		}
		seen[tid] = struct{}{}
		out = append(out, tid)
	}
	return out
}

// collectStreamCorrelations iterates CORRELATES_WITH edges both ways
// out of each of the stream's templates, dedups by (myID, peerID), and
// returns rows ready for sorting. Peer template lookup goes through
// the pre-fetched logState so callers don't need to plumb the engine
// through — keeps the traverse path orthogonal to query mode.
func collectStreamCorrelations(
	st *logState,
	myTmplIDs []string,
) []streamCorrelation {
	seen := make(map[string]struct{})
	var rows []streamCorrelation
	for _, myID := range myTmplIDs {
		myNode, ok := st.NodeByID(myID)
		if !ok {
			continue
		}
		rows = appendCorrelationEdges(st, myNode,
			kgwire.OutgoingEdges, true, seen, rows)
		rows = appendCorrelationEdges(st, myNode,
			kgwire.IncomingEdges, false, seen, rows)
	}
	return rows
}

// appendCorrelationEdges walks one direction of CORRELATES_WITH edges
// for a single template, looks up the peer node, and appends one
// streamCorrelation row per unique pair. mineIsSideA controls which
// side of the Evidence service pair is treated as "my" service so the
// displayed services line up correctly when the stream's template is
// TemplateA vs TemplateB in the underlying correlation record.
func appendCorrelationEdges(
	st *logState,
	myNode *knowledgev1.Node,
	dir kgwire.EdgeDirection,
	mineIsSideA bool,
	seen map[string]struct{},
	rows []streamCorrelation,
) []streamCorrelation {
	edges := st.EdgesOf(myNode.Id, dir, []kgtypes.EdgeType{kgtypes.EdgeCorrelatesWith})
	for i := range edges {
		e := &edges[i]
		peerID := e.ToId
		if dir == kgwire.IncomingEdges {
			peerID = e.FromId
		}
		pairKey := myNode.Id + "↔" + peerID
		if _, ok := seen[pairKey]; ok {
			continue
		}
		seen[pairKey] = struct{}{}
		peerNode, ok := st.NodeByID(peerID)
		if !ok {
			continue
		}
		svcA, svcB, _, _ := parseCorrelationEvidence(e.Evidence)
		mine, peer := svcA, svcB
		if !mineIsSideA {
			mine, peer = svcB, svcA
		}
		rows = append(rows, streamCorrelation{
			MyTemplate:   myNode,
			PeerTemplate: peerNode,
			Score:        e.Confidence,
			ServiceA:     mine,
			ServiceB:     peer,
		})
	}
	return rows
}

// sortStreamCorrelations orders rows by score desc, with alias
// tie-break so repeated runs produce stable output.
func sortStreamCorrelations(rows []streamCorrelation) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Score != rows[j].Score {
			return rows[i].Score > rows[j].Score
		}
		return templateNodeSortKey(rows[i].PeerTemplate) <
			templateNodeSortKey(rows[j].PeerTemplate)
	})
}

// templateRefShort renders a template node as "`alias`" (or the
// short hex ID when no alias is recorded). Used for the compact
// correlation list.
func templateRefShort(n *knowledgev1.Node) string {
	if a := kgtypes.Value(n, "alias"); a != "" {
		return fmt.Sprintf("`%s`", a)
	}
	if n.SymbolName != "" {
		return fmt.Sprintf("`%s`", n.SymbolName)
	}
	return fmt.Sprintf("`%s`", shortHex(n.Id))
}

// templateNodeSortKey returns the alias when present, else the
// SymbolName, else the hex ID — for stable secondary sort.
func templateNodeSortKey(n *knowledgev1.Node) string {
	if a := kgtypes.Value(n, "alias"); a != "" {
		return a
	}
	if n.SymbolName != "" {
		return n.SymbolName
	}
	return n.Id
}

// serviceOrDash returns "—" for empty strings so the table is visually
// aligned when the Evidence lacked a service.
func serviceOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
