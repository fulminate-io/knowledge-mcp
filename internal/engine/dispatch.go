// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"syscall"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// ExecuteFn issues one Engine.Execute RPC. Satisfied by GraphClient.Execute.
type ExecuteFn func(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)

// Dispatch is the compile-OR-DENY chokepoint shared by the bootstrap caller, the
// bare forwarder, and the InterceptSearch/Query tails. It is the ONE neutral place
// all three import.
//
// Contract (compile-OR-DENY — there is no wire fallback):
//   - Compile returns ok=true → call exec EXACTLY ONCE (one Execute per tool
//     call — bounded-constant), then Render the response. An engine error is
//     mapped to the LLM-facing rendered message (CodeInvalidArgument →
//     validation text; CodeNotFound → not-found text).
//   - Compile returns ok=false → return an EXPLICIT DENY naming the tool
//     (decision 7fc2ff59). Every LLM-facing tool either compiles to Engine.Execute
//     here OR is claimed by a client intercept BEFORE this dispatcher runs (the
//     InterceptChain); a shape that reaches Dispatch and does not compile is a
//     genuine unrecognized request, so it is denied legibly.
//
// Type-aware mutate(create) body validation is NO LONGER prechecked client-side
// (FUL-306): the create path flows straight to Compile→exec, and the server
// engine enforces the system-managed/step/embed-only-summary rules in
// decodeCreate, returning invalidMutation (CodeInvalidArgument) that
// renderEngineError relays to the LLM verbatim. The query precheck seam below
// (precheckQuery) is the remaining pre-Compile validation hook.
func Dispatch(ctx context.Context, exec ExecuteFn, tool string, args json.RawMessage) (kgtools.ToolResult, error) {
	// Special-shape pre-Compile seam (T-GTB2 site (d)): a query(id) carrying
	// include_edges / include_cross_links is NOT a single-plan compile — the
	// engine does not absorb those carriers. dispatchQueryByID composes the
	// edge-summary / cross-link sections via bounded multi-call orchestration
	// (RETURN_MODE_EDGES + bulk peer hydrate + linkage queries) and renders. It
	// is a NAMED helper invoked BEFORE the generic Compile/exec/Render flow; when
	// the shape is not an absorption read it returns handled=false and Dispatch
	// proceeds normally.
	if tool == "query" {
		// Special-shape pre-Compile seam (GAP-B): a text-required query mode
		// (text/graph_reach/recent) with EMPTY text is a validation failure, not a
		// generic deny. precheckQuery returns the specific requires-text error
		// BEFORE Compile so the LLM sees a legible message rather than the
		// post-cutover "not a recognized engine-reducible shape" fall-through. A
		// non-nil error is rendered without issuing an Execute RPC.
		if verr := precheckQuery(args); verr != nil {
			return errorResult(verr.Error()), nil
		}
		if out, handled := dispatchQueryByID(ctx, exec, args); handled {
			return out, nil
		}
	}
	// Special-shape pre-Compile seam (T-GTB1e #5): a start-less traverse is the
	// graph-wide-edges fast path — a two-step (enumerate all nodes, then union
	// their edges) that no single compiled plan can express. dispatchGraphWideEdges
	// composes it from a Match-all enumeration + the RETURN_MODE_EDGES ids[]→union
	// carrier; it returns handled=false for a from_id walk / logs traverse so
	// Dispatch proceeds to the generic compileTraverse flow.
	if tool == "traverse" {
		if out, handled := dispatchGraphWideEdges(ctx, exec, args); handled {
			return out, nil
		}
	}
	// Special-shape pre-Compile seam: a delete(dry_run:true) must NEVER compile to
	// a MUTATION_KIND_DELETE (the by-ids compile path ignored dry_run and really
	// deleted — finding f95119710b). dispatchDeletePreview claims the dry-run here
	// and issues a READ against the same selection the real delete would target,
	// rendering a "would delete N" preview. It returns handled=false for a non-
	// dry-run delete so Dispatch proceeds to the generic compile→exec→Render
	// (real-delete) flow.
	if tool == "delete" {
		if out, handled := dispatchDeletePreview(ctx, exec, args); handled {
			return out, nil
		}
	}
	req, ok := Compile(tool, args)
	if !ok {
		// Deny flip (decision 7fc2ff59): a Compile-miss is an explicit deny. Any
		// tool that should run here either compiles or is intercept-claimed upstream.
		return errorResult(fmt.Sprintf(
			"engine: tool %q is not a recognized engine-reducible shape and has no client intercept — request denied (no legacy dispatch fallback exists post-cutover)",
			tool)), nil
	}
	resp, err := exec(ctx, req)
	if err != nil {
		return renderEngineError(err), nil
	}
	return Render(tool, args, resp)
}

// validationErr is a client-side pre-Compile validation failure (the
// precheckQuery requires-text gate). It carries the LLM-facing validation
// message verbatim; Dispatch renders it as an errorResult (the explicit-error
// contract).
type validationErr struct{ msg string }

func (e validationErr) Error() string { return e.msg }

// validationError wraps a validation message as a validationErr error.
func validationError(msg string) error { return validationErr{msg: msg} }

// renderEngineError maps an error returned by the Engine.Execute transport
// into an LLM-facing error ToolResult. The mapping ladder, in order:
//
//  1. graphclient.ErrNoBackend (FUL-323) — Router.pick returned no backend
//     (local==nil AND not logged in). Surfaces "no backend available — run
//     `knowledge install` ... `knowledge login`" so the LLM has an
//     actionable hint rather than the raw sentinel text.
//  2. Local-server-unreachable transport failure (FUL-323) — connect-go's
//     CodeUnavailable, or a wrapped syscall.ECONNREFUSED in a net.OpError
//     chain. The local server was named (Router.pick chose it) but the
//     underlying HTTP/2 dial failed. Surfaces "local server unreachable —
//     run `knowledge start` ... `knowledge login`" so the user sees the
//     restart hint, not a raw "connect: connection refused" leak.
//  3. CodeInvalidArgument / CodeNotFound — engine validation / missing-node
//     errors. Message relayed verbatim (legacy behavior).
//  4. Any other connect.Error — generic "engine: <message>" fallback.
//  5. Non-connect errors — generic "engine: <error>" fallback.
//
// Ordering matters: branches (1) and (2) MUST fire before the generic connect
// switch because CodeUnavailable would otherwise land in the default arm with
// the raw "connect: connection refused" leak.
func renderEngineError(err error) kgtools.ToolResult {
	if errors.Is(err, graphclient.ErrNoBackend) {
		return errorResult("no backend available — run `knowledge install` to start the local server, or `knowledge login` to use Fulminate Cloud.")
	}
	if isLocalServerUnreachable(err) {
		return errorResult("local server unreachable — run `knowledge start` to restart it, or `knowledge login` to use Fulminate Cloud.")
	}
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		switch ce.Code() {
		case connect.CodeInvalidArgument, connect.CodeNotFound:
			return errorResult(ce.Message())
		default:
			return errorResult("engine: " + ce.Message())
		}
	}
	return errorResult("engine: " + err.Error())
}

// isLocalServerUnreachable reports whether err carries the FUL-323
// local-server-unreachable signature: either a connect.Error with
// CodeUnavailable (the connect-go transport surfaces this when the underlying
// HTTP/2 dial fails) OR a wrapped syscall.ECONNREFUSED (the underlying connect
// error sometimes presents as a net.OpError carrying ECONNREFUSED — errors.Is
// catches the wrapped case). Mirrors the retryable-transport classification in
// graphclient/retry.go for the dispatcher's render path.
func isLocalServerUnreachable(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce.Code() == connect.CodeUnavailable
	}
	return false
}

// isNotFound reports whether err is a connect CodeNotFound error — the by-id
// result miss the engine returns. dispatchQueryByID treats it as not-found
// (surfacing the not-found message) rather than a hard error.
func isNotFound(err error) bool {
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce.Code() == connect.CodeNotFound
	}
	return false
}

// Render re-derives the render kind from tool+args and renders the engine
// response into the LLM-facing output. It re-parses the args (cheap) to recover
// the render context (graph label, search mode, node type, offset, format,
// fields, meta keys, traverse start/direction, mutation kind) — the same intent
// Compile lowered into the plan.
func Render(tool string, args json.RawMessage, resp *knowledgev1.ExecuteResponse) (kgtools.ToolResult, error) {
	switch tool {
	case "search":
		return renderSearchTool(args, resp)
	case "query":
		return renderQueryTool(args, resp)
	case "traverse":
		return renderTraverseTool(args, resp)
	case "mutate":
		return renderMutateTool(args, resp)
	case "delete":
		return renderDeleteTool(args, resp)
	default:
		return kgtools.ToolResult{}, fmt.Errorf("Render: unrenderable tool %q", tool)
	}
}

// renderDeleteTool renders the standalone `delete` tool's compiled DELETE
// response. The standalone tool carries NO `operation` field (unlike
// mutate(operation:delete)), so it cannot reuse renderMutateTool's
// mutationKindFor(a.Operation) path — it parses ONLY the format field and calls
// renderMutationResponse with MUTATION_KIND_DELETE directly (mutationVerb maps
// DELETE → "Deleted", render_misc.go), rendering the "Deleted N node(s)"
// affected-count line.
func renderDeleteTool(args json.RawMessage, resp *knowledgev1.ExecuteResponse) (kgtools.ToolResult, error) {
	var a struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return kgtools.ToolResult{}, err
	}
	return renderMutationResponse(resp, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, a.Format), nil
}

// renderSearchTool renders a compiled search response with the mode-label
// suffix. The search tool is always RETURN_MODE_SEARCH; the mode is hybrid
// (no suffix) for the `search` tool. A cloud/cicd resource_type is post-filtered
// here on the client (T-GTB2 site (c) — OP_PREFIX is inert on a QSearch, so the
// trim moved from the engine post-rank to this render path).
func renderSearchTool(args json.RawMessage, resp *knowledgev1.ExecuteResponse) (kgtools.ToolResult, error) {
	var a searchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return kgtools.ToolResult{}, err
	}
	query := firstQueryLabel(a.Query, a.Queries)
	// Per-request embed signal: a present query_vector ⟺ a vector search ran
	// (the server never embeds). rerankRan is always false on this arm —
	// the rerank branch renders via applyClientRerank, not Dispatch.
	mode := searchModeLabel(a.QueryVector != "", false)
	return renderSearchResponseFiltered(resp, query, a.Format, a.Fields, knowledgev1.SearchMode_SEARCH_MODE_HYBRID, a.ResourceType, mode)
}

// renderQueryTool renders a compiled query response, branching on the same
// shape Compile recognized: search modes (text/graph_reach/recent) → search
// render with the mode-label; ids[]-bulk → {nodes:[]}; single id → bare node;
// type-browse / meta-only → browse.
func renderQueryTool(args json.RawMessage, resp *knowledgev1.ExecuteResponse) (kgtools.ToolResult, error) {
	var a queryArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return kgtools.ToolResult{}, err
	}
	label := queryGraphLabelFor(a)
	// Per-request embed signal: a present query_vector ⟺ a vector search ran.
	// InterceptQuery embeds only the "" / "hybrid" modes, so graph_reach /
	// recent / text and the bare default arrive with no query_vector and are
	// BM25-only EVEN with a Voyage key — keying off config would mislabel them.
	mode := searchModeLabel(a.QueryVector != "", false)

	switch a.Mode {
	case "graph_reach":
		return renderSearchResponse(resp, a.Text, a.Format, a.Fields, knowledgev1.SearchMode_SEARCH_MODE_PPR, mode)
	case "recent":
		return renderSearchResponse(resp, a.Text, a.Format, a.Fields, knowledgev1.SearchMode_SEARCH_MODE_TEMPORAL, mode)
	case "text":
		return renderSearchResponse(resp, a.Text, a.Format, a.Fields, knowledgev1.SearchMode_SEARCH_MODE_HYBRID, mode)
	case "modules":
		return renderGraphNamesResponse(resp)
	}
	// Default mode dispatch mirrors buildDefaultModePlan's precedence. Every arm
	// threads a.Fields so the tool-wide `fields` projection reaches the live
	// render path (the bug was each arm passing nil / omitting Fields, forcing
	// full-node hydration regardless of the requested projection).
	switch {
	case len(a.IDs) > 0:
		return renderNodesByIDsResponse(resp, label, a.Format, a.Fields)
	case a.ID != "":
		return renderNodeResponse(resp, label, a.ID, a.Graph == "" || a.Graph == "knowledge", a.Fields)
	case a.Text != "":
		return renderSearchResponse(resp, a.Text, a.Format, a.Fields, knowledgev1.SearchMode_SEARCH_MODE_HYBRID, mode)
	default:
		// type-browse or meta-only.
		return renderBrowseResponse(resp, browseContext{
			Label:    label,
			NodeType: a.Type,
			Offset:   a.Offset,
			Format:   a.Format,
			Fields:   a.Fields,
			MetaKeys: metaKeys(a.Meta),
		})
	}
}

// renderGraphNamesResponse renders the query(mode:modules) catalog enumeration:
// it decodes the RETURN_MODE_GRAPH_NAMES carrier into []*knowledgev1.GraphInfo
// and emits the {graphs:[{name,loaded,file_path,file_size,...}]} envelope (the
// proto GraphInfo's JSON shape). T-GTB1e ships THIS object shape + its test only;
// the legacy fetchGraphNames ([]string) + listPracticeGraphs ([{name}]) consumers
// stay on their unchanged legacy paths (they are T-GTB6 repoint targets).
func renderGraphNamesResponse(resp *knowledgev1.ExecuteResponse) (kgtools.ToolResult, error) {
	infos, err := DecodeGraphNames(resp)
	if err != nil {
		return kgtools.ToolResult{}, err
	}
	return jsonResult(map[string]any{"graphs": infos}), nil
}

// renderTraverseTool renders a compiled traverse response (the engine's merged
// both-union TraversalList, rendered flat).
func renderTraverseTool(args json.RawMessage, resp *knowledgev1.ExecuteResponse) (kgtools.ToolResult, error) {
	var a traverseArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return kgtools.ToolResult{}, err
	}
	dir := a.Direction
	if dir == "" {
		dir = "out" // validateDirection default.
	}
	return renderTraversalResponse(resp, traverseContext{
		Start:     a.Start,
		GraphName: a.Graph,
		Direction: dir,
		Format:    a.Format,
	})
}

// renderMutateTool renders a compiled mutation response, deriving the
// MutationKind from the operation Compile recognized.
func renderMutateTool(args json.RawMessage, resp *knowledgev1.ExecuteResponse) (kgtools.ToolResult, error) {
	var a mutateArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return kgtools.ToolResult{}, err
	}
	kind := mutationKindFor(a.Operation)
	return renderMutationResponse(resp, kind, a.Format), nil
}

// mutationKindFor maps the mutate operation onto the MutationKind Compile used.
func mutationKindFor(op string) knowledgev1.MutationPlan_MutationKind {
	switch op {
	case "create", "create_batch":
		return knowledgev1.MutationPlan_MUTATION_KIND_CREATE
	case "update", "update_batch", "bulk_update_metadata":
		// update_batch + bulk_update_metadata compile to UPDATE_ITEMS, but the
		// render only needs the affected-count "Updated N node(s)" line — the UPDATE
		// verb covers both (mutationVerb has no UPDATE_ITEMS verb; it shares UPDATE's).
		return knowledgev1.MutationPlan_MUTATION_KIND_UPDATE
	case "delete":
		return knowledgev1.MutationPlan_MUTATION_KIND_DELETE
	case "link":
		return knowledgev1.MutationPlan_MUTATION_KIND_LINK
	case "unlink":
		return knowledgev1.MutationPlan_MUTATION_KIND_UNLINK
	default:
		return knowledgev1.MutationPlan_MUTATION_KIND_UNSPECIFIED
	}
}

// firstQueryLabel returns the label for a search render: the single query, or
// the first of the merged batch.
func firstQueryLabel(query string, queries []string) string {
	merged := mergeQueries(query, queries)
	if len(merged) > 0 {
		return merged[0]
	}
	return query
}

// queryGraphLabelFor mirrors the server queryGraphLabel (tools_query_dispatch.go:149):
// the short graph identifier for the render header. Empty graph → "knowledge".
func queryGraphLabelFor(a queryArgs) string {
	switch a.Graph {
	case "", "knowledge":
		return "knowledge"
	case "practice":
		if a.Language != "" {
			return "practice:" + a.Language
		}
		return "practice"
	case "cloud":
		if a.Account != "" {
			return "cloud:" + a.Account
		}
		return "cloud"
	case "cicd":
		if a.Account != "" {
			return "cicd:" + a.Account
		}
		return "cicd"
	case "web":
		if a.Name != "" {
			return "web:" + a.Name
		}
		return "web"
	case "pdf":
		if a.Name != "" {
			return "pdf:" + a.Name
		}
		return "pdf"
	default:
		return a.Graph
	}
}

// metaKeys returns the keys of the meta filter map for inline surfacing in the
// browse render.
func metaKeys(meta map[string]string) []string {
	if len(meta) == 0 {
		return nil
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	return keys
}
