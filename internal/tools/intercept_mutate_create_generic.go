// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// intercept_mutate_create_generic.go holds the TYPE-BLIND context-linked create
// path: the predicate that claims a create carrying any context-link param, and
// the handler that serves it.
//
// SPLIT BY CONCERN, not by bytes. The finding / research / rule handlers in
// intercept_mutate_create.go are a family read together — each builds a
// type-specific node body, stamps its own derived fields and renders its own
// line. This handler is the opposite of all three: it builds nothing
// type-specific, derives nothing, and exists precisely so no future node type
// needs a handler of its own. Keeping it beside them would file it as a fifth
// member of a family it is not in.

// contextParamsSupplied reports whether the caller sent any context-link param.
func contextParamsSupplied(a mutateArgs) bool {
	return a.TicketID != "" || a.Session != "" || len(a.Links) > 0
}

// handleClientMutateCreateContextLinked creates ONE knowledge node of ANY type
// and born-links it: ticket--contains-->node, session--contains-->node and
// node--relates-to-->target, exactly the three edges buildContextLinks emits for
// the finding / research / rule paths.
//
// IT IS KEYED ON TRIO PRESENCE, NEVER ON NODE TYPE. That is what closes the
// class the per-type create arms kept re-opening: a create carrying a ticket_id
// on a type nobody has written a handler for is born-linked by construction
// rather than refused with a follow-up-link hint. A create carrying none of the
// three is untouched by this arm and declines to the engine create path exactly
// as before.
//
// THE FAIL-TOLERANCE CONTRACT IS INHERITED, NOT REDESIGNED. buildContextLinks
// pre-validates every knowledge-graph endpoint and DROPS an unresolvable one
// with a warning rather than failing the write it decorates; a foreign target
// becomes a post-create LinkOne that logs and continues. This handler adds no
// error handling of its own — turning a dropped link into a failed create would
// break that contract for every type at once.
//
// FIVE THINGS IT DELIBERATELY DOES NOT DO, each of which would make a
// trio-carrying create diverge from the same create WITHOUT a trio — the one
// divergence this path may never introduce, because the only difference the
// caller asked for is edges:
//   - no validate.Name and no validate.ClampSummary. The sibling handlers call
//     them; the engine create path does not, and the server's create validator
//     gates both. Adding them here means a body the generic path accepts starts
//     failing the moment a ticket_id is added.
//   - no source stamping. The siblings stamp a client provenance in their node
//     builders; that is CLIENT POLICY of those handlers, not of this path.
//   - no summary derivation, for the same reason.
//   - metadata rides as the caller's map directly rather than through
//     copyCallerMetadata, which exists so the sibling builders' DERIVED keys do
//     not leak back into it. This builder derives nothing, and PersistBatch's
//     own projection already copies.
//   - no id, status or content handling of its own: every body field is passed
//     through verbatim for the engine and the server validator to judge.
//
// FORMAT IS HONORED rather than declared deliberately ignored, which is the one
// place this arm diverges from the four sibling create arms on purpose. Those
// arms render their own text and ignore format. If this one did too, a caller
// who already passes format:"json" would see their response silently turn to
// text the moment they added a ticket_id — a render change keyed on an
// unrelated param. So both renders are the engine's own: the json body is the
// same {"ids":[...]} object, and the text line is the same single-id line.
func handleClientMutateCreateContextLinked(ctx context.Context, deps ClientDeps, a mutateArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	node := &knowledgev1.Node{
		Type:        a.Type,
		SymbolName:  a.Name,
		Description: a.Description,
		Summary:     a.Summary,
		Content:     a.Content,
		Status:      a.Status,
		Metadata:    a.Metadata,
		Id:          a.ID,
		Source:      a.Source,
	}
	// Context links: pre-validated ticket/session/knowledge-link edges ride the
	// create_batch; code links + warnings are handled after. Node is slot 0.
	cl := buildContextLinks(ctx, gc, a.TicketID, a.Session, a.Links)
	ids, perr := PersistBatch(ctx, gc, []*knowledgev1.Node{node}, cl.batchEdges, newBundleID())
	if perr != nil {
		return errorResult("create: " + perr.Error())
	}
	if len(ids) == 0 {
		return errorResult("create: persist returned no IDs")
	}
	warnings := append(cl.warnings, applyCodeLinks(ctx, gc, ids[0], cl.codeLinks)...)
	if a.Format == "json" {
		out := map[string]any{"ids": ids}
		// The warnings key is CONDITIONAL so a clean create's body stays
		// byte-identical to the engine's; an unconditional assignment would
		// marshal a nil slice as "warnings":null on every call.
		if len(warnings) > 0 {
			out["warnings"] = warnings
		}
		return jsonResult(out)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Created → ID: %s", ids[0])
	writeClientWarningsSection(&sb, warnings)
	return textResult(sb.String())
}
