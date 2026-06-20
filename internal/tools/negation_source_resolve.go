// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// currentSource is one resolved unit of a contradicted node's LIVE first-party
// source text, plus where it came from. Text is the verbatim current content the
// negation gate matches a quote against; Origin records provenance as either
// "code:<nodeID>" (the cited code node, where nodeID encodes <filepath:Symbol>)
// or "thought:<id>" (the thought's own current Summary+Content). Origin is the
// ONLY locality handle the gate has — currentSource deliberately does NOT carry
// the resolved *Node, so quoteLocalToRange parses the file path out of Origin
// rather than reading a node field.
type currentSource struct {
	Text   string
	Origin string
}

// resolveThoughtCurrentSource resolves a single contradicted thought to the
// CURRENT (live, never cached) first-party source text the deterministic
// negation gate validates a verbatim quote against. It is the LLM-free ground
// truth read: pure graph reads, no embed/summarize/relevance step anywhere.
//
// Two resolution paths, in order:
//
//  1. CODE-REF PATH — CONSUME the shared thought→cited-code-node boundary
//     thought.ResolveCitedCodeNodes (cmd/knowledge/internal/thought/
//     cited_code_staleness.go). That boundary already walks the
//     thought --relates-to(code-ref)--> proxy --(repo+foreign_id)--> code node
//     chain and returns the HYDRATED code nodes; this resolver only reads
//     node.GetContent() off them. It does NOT re-compose the resolution chain —
//     that chain is the shared seam; duplicating it would be a snowflake. The
//     merged boundary returns map[string][]*knowledgev1.Node with NO error (it
//     is best-effort: any read failure or empty stage yields an empty map), so
//     there is no error to thread here — an empty result simply falls to (2).
//
//  2. REQUIRE-OWN-CONTENT FALLBACK — when the thought has NO resolvable code-ref
//     link (the boundary returns nothing for it), require a verbatim quote of the
//     thought's OWN live Summary+Content, fetched fresh via render.FetchNode
//     (current node, NOT a cached copy). This is the weaker-but-un-forgeable
//     guarantee: it proves the negator pulled the live thought node, even when no
//     code is cited. It is never exempt / no-op — a contradicted node with no
//     code-ref still must be quoted.
//
// Returns ([]currentSource, nil) on success. Returns (nil, nil) only when the
// thought node itself cannot be resolved at all (the caller treats an empty
// source set as gate-fail: no first-party basis means the negation is rejected).
//
// Perf: called for ONE thoughtID (the single contradicted node of one negation
// call), so the boundary's bulk/no-N+1 machinery resolves a single bounded set —
// a serial single resolve, no batch/parallel analog applies.
func resolveThoughtCurrentSource(ctx context.Context, gc GraphCaller, thoughtID string) ([]currentSource, error) {
	if gc == nil || thoughtID == "" {
		return nil, nil
	}

	// (1) Code-ref path: read live Content off the boundary-resolved code nodes.
	// tools.GraphCaller (Execute-only) satisfies thought.Caller structurally.
	byThought := clientthought.ResolveCitedCodeNodes(ctx, gc, []string{thoughtID})
	var sources []currentSource
	for _, n := range byThought[thoughtID] {
		if n == nil {
			continue
		}
		sources = append(sources, currentSource{
			Text:   n.GetContent(),
			Origin: "code:" + n.GetId(),
		})
	}
	if len(sources) > 0 {
		return sources, nil
	}

	// (2) REQUIRE-OWN-CONTENT fallback: no resolvable code-ref link, so require a
	// verbatim quote of the thought's own current Summary+Content. render.FetchNode
	// reads the LIVE node (one ByID Execute), so the match is against current
	// state, not a stale snapshot. A nil node means the thought itself is gone →
	// (nil, nil), which the caller treats as gate-fail.
	node, err := render.FetchNode(ctx, gc, thoughtID)
	if err != nil || node == nil {
		return nil, err
	}
	return []currentSource{{
		Text:   node.GetSummary() + "\n" + node.GetContent(),
		Origin: "thought:" + thoughtID,
	}}, nil
}
