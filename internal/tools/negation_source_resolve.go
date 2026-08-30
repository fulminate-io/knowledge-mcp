// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"

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
//     node.GetContent() off them, and admits a node as a source ONLY when that
//     content is non-blank. It does NOT re-compose the resolution chain — that
//     chain is the shared seam; duplicating it would be a snowflake. The merged
//     boundary returns map[string][]*knowledgev1.Node with NO error (it is
//     best-effort: any read failure or empty stage yields an empty map), so there
//     is no error to thread here — an empty result falls to (2), and so does a
//     result whose nodes ALL carry blank content.
//
//  2. REQUIRE-OWN-CONTENT FALLBACK — when the thought has no cited code node
//     carrying live source (the boundary returns nothing for it, or everything it
//     returns is content-less), require a verbatim quote of the
//     thought's OWN live Summary+Content, fetched fresh via render.FetchNode
//     (current node, NOT a cached copy). This is the weaker-but-un-forgeable
//     guarantee: it proves the negator pulled the live thought node, even when no
//     code is cited. It is never exempt / no-op — a contradicted node with no
//     code-ref still must be quoted.
//
// Returns ([]currentSource, contentLessCitations, nil) on success. The second
// return is the CODE NODE IDs that reached resolution but were excluded from the
// comparison set for carrying no content — reported so a rejection can name them
// (see the exclusion comment below); it is not part of the comparison and never
// affects whether a quote matches. Returns (nil, contentLess, nil) only when the
// thought node itself cannot be resolved at all (the caller treats an empty source
// set as gate-fail: no first-party basis means the negation is rejected).
//
// Perf: called for ONE thoughtID (the single contradicted node of one negation
// call), so the boundary's bulk/no-N+1 machinery resolves a single bounded set —
// a serial single resolve, no batch/parallel analog applies.
func resolveThoughtCurrentSource(ctx context.Context, gc GraphCaller, thoughtID string) ([]currentSource, []string, error) {
	if gc == nil || thoughtID == "" {
		return nil, nil, nil
	}

	// (1) Code-ref path: read live Content off the boundary-resolved code nodes.
	// tools.GraphCaller (Execute-only) satisfies thought.Caller structurally.
	//
	// The trailing nil read-memo is DELIBERATE. This is a single-thought on-demand
	// resolution with no propagation pass in hand, so there is no per-pass memo to
	// consult and it takes the narrow relates-to read exactly as it always has.
	// Do not "helpfully" thread a source here.
	byThought := clientthought.ResolveCitedCodeNodes(ctx, gc, []string{thoughtID}, nil)
	var sources []currentSource
	var contentLess []string
	for _, n := range byThought[thoughtID] {
		if n == nil {
			continue
		}
		text := n.GetContent()
		if strings.TrimSpace(text) == "" {
			// CONTENT-LESS CITATION — excluded from the comparison set, applying the
			// same TrimSpace test validateNegationQuote already applies to the quote
			// itself. The governing rule is that an empty resolution must never be
			// silently compared: because the quote is guaranteed non-blank by the
			// time the comparison runs, an empty source could never match, and
			// admitting it would do nothing but suppress the require-own-content path
			// below. Skipping it is what lets a citation set that is ENTIRELY
			// content-less fall through to (2).
			//
			// THE EXCLUSION IS UNCONDITIONAL, AND THAT IS DELIBERATE RATHER THAN A
			// LIMITATION OF THE CHECK. The ordinary case is a file-level node, which
			// carries no content by design because live source hangs off the symbol
			// nodes beneath it. A SYMBOL node with blank content is a different
			// animal — an indexing gap, not a designed shape — and this guard treats
			// the two identically, which lowers that one thought's bar from quoting
			// cited code to quoting its own live Summary+Content. The exposure is
			// bounded by the un-forgeable baseline every thought citing no code
			// already meets: the negator must still quote the live node.
			//
			// THE EXCLUSION IS NOT SILENT ANY MORE (CEO amendment, 2026-08-28). The
			// id is recorded here and returned to the gate, which names it and its
			// reason in the rejection. Only the REPORTING changed: the resolution rule
			// above is untouched, still unqualified, and a content-less citation still
			// contributes no source and still lowers the bar to own content. Do not
			// read the rejection's file-versus-symbol wording back into this guard as
			// a license to narrow it into a loud failure — that direction remains
			// settled, and reopening it is a separate decision.
			contentLess = append(contentLess, n.GetId())
			continue
		}
		sources = append(sources, currentSource{
			Text:   text,
			Origin: "code:" + n.GetId(),
		})
	}
	if len(sources) > 0 {
		return sources, contentLess, nil
	}

	// (2) REQUIRE-OWN-CONTENT fallback: no cited code node carries live source —
	// either there is no resolvable code-ref link at all, or every one that resolved
	// was content-less and skipped above — so require a verbatim quote of the
	// thought's own current Summary+Content. render.FetchNode
	// reads the LIVE node (one ByID Execute), so the match is against current
	// state, not a stale snapshot. A nil node means the thought itself is gone →
	// no sources, which the caller treats as gate-fail. The content-less ids ride
	// out on every path, including that one: they record what was excluded, which
	// is true regardless of what the fallback then found.
	node, err := render.FetchNode(ctx, gc, thoughtID)
	if err != nil || node == nil {
		return nil, contentLess, err
	}
	return []currentSource{{
		Text:   node.GetSummary() + "\n" + node.GetContent(),
		Origin: "thought:" + thoughtID,
	}}, contentLess, nil
}

// resolveMethodlessCitations names the code citations that never reached the
// resolution above because their relates-to edge carried no code-ref method — the
// shape mutate's links param mints. It is a thin pass-through to the shared
// thought-package boundary, co-located here with the other half of the exclusion
// picture so both graph reads behind the rejection's excluded-citations clause sit
// in the resolver file rather than in the message file.
//
// CALLED ONLY ON THE REJECTION PATH. It costs an edge read plus a proxy hydrate
// that the accept path has no use for, and the boundary's own doc records why it is
// not folded into ResolveCitedCodeNodes.
func resolveMethodlessCitations(ctx context.Context, gc GraphCaller, thoughtID string) []string {
	return clientthought.MethodlessCodeCitations(ctx, gc, thoughtID)
}
