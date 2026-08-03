// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/backends/dispatch"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// linearArchiveRetryGuidance is the locked single-line operator guidance
// appended to delete-path partial-failure errors. Covers the
// failure mode where Linear's adapter treats re-archive of an
// already-archived issue as a no-op success per
// cmd/knowledge/internal/backends/linear/backend_write_ticket.go:124-125
// and …/backend_write_project.go:145-146. NO live probe at runtime; if
// future evidence contradicts this lock, file a SEPARATE ticket — do
// not silently flip this constant inside this file.
const linearArchiveRetryGuidance = "Re-running the delete will safely re-issue the Linear archive (Linear treats re-archive of already-archived issues as a no-op success — locked in-tree at cmd/knowledge/internal/backends/linear/backend_write_ticket.go:124-125)."

// mutateArgs mirrors the subset of server-side mutateRequestArgs this
// intercept reads. Unknown fields pass through via the raw
// params.Arguments forward — we never strip a field by omitting it
// from this struct.
type mutateArgs struct {
	Operation   string            `json:"operation"`
	Type        string            `json:"type,omitempty"`
	ID          string            `json:"id,omitempty"`
	IDs         []string          `json:"ids,omitempty"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Summary     string            `json:"summary,omitempty"`
	Content     string            `json:"content,omitempty"`
	Status      string            `json:"status,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Graph       string            `json:"graph,omitempty"`
	Language    string            `json:"language,omitempty"`
	Format      string            `json:"format,omitempty"`
	LinkGraph   string            `json:"link_graph,omitempty"`

	// Link fields — claimed for the intra-practice
	// cross-graph-link branch (mutate(link, graph:practice)).
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	Relationship string `json:"relationship,omitempty"`

	// Edge-metadata fields — claimed for the link_graph:linkage
	// branch (the client-owned cross-graph link carrying edge metadata onto the
	// linkage EdgeSpec). The json tags match emitLink's canonical wire (linker/
	// helpers.go) + linkWithMetaArgs (wire_persist.go). EdgeEvidence is DISTINCT
	// from the finding-Evidence field above (different concern, different tag).
	Weight        float64 `json:"weight,omitempty"`
	Confidence    float64 `json:"confidence,omitempty"`
	Method        string  `json:"method,omitempty"`
	EdgeEvidence  string  `json:"edge_evidence,omitempty"`
	LastValidated string  `json:"last_validated,omitempty"`

	// Negation-gate proof-of-work fields — read ONLY by InterceptNegationGate
	// (negation_gate.go), which runs BEFORE InterceptMutate. VerifiedQuote is a
	// verbatim substring of the contradicted node's CURRENT source the negator must
	// supply for mutate(link relationship:contradicts) / mutate(update status:
	// invalidated) to pass; CitedRange is the optional file:line-range locality hint
	// (empty → existence+currency only). GATE-only inputs: the write IGNORES them
	// (proof-of-work, never persisted), so they are not threaded into any write path.
	VerifiedQuote string `json:"verified_quote,omitempty"`
	CitedRange    string `json:"cited_range,omitempty"`

	// Extended fields claimed for create=finding/
	// research/rule + answer dispatch arms, and (Command/CriterionType/
	// Keywords) the per-type update intercept (intercept_mutate_update.go).
	// Command + CriterionType were previously only on criterionCreateArgs; the
	// update path needs them on this shared wire-mirror to route a criterion
	// update's command/criterion_type into metadata + re-derive the summary.
	Evidence      string             `json:"evidence,omitempty"`
	Source        string             `json:"source,omitempty"`
	Scope         string             `json:"scope,omitempty"`
	Enforcement   string             `json:"enforcement,omitempty"`
	Command       string             `json:"command,omitempty"`
	CriterionType string             `json:"criterion_type,omitempty"`
	Keywords      string             `json:"keywords,omitempty"`
	Alternatives  string             `json:"alternatives,omitempty"`
	Conclusion    string             `json:"conclusion,omitempty"`
	Concludes     bool               `json:"concludes,omitempty"`
	Findings      string             `json:"findings,omitempty"`
	QuestionID    string             `json:"question_id,omitempty"`
	Supports      string             `json:"supports,omitempty"`
	References    []findingReference `json:"references,omitempty"`

	// Context-linking fields: optional pass-through so a
	// finding/research/rule create is born linked to the active ticket
	// (ticket--contains-->node), grouped under a session
	// (session--contains-->node), and related to touched code/knowledge
	// nodes (node--relates-to-->target). Lowered onto the create_batch by
	// buildContextLinks (write_context_links.go); every edge is
	// fail-tolerant (an unresolvable target drops+warns, never blocks the
	// write). Session reuses the think-path get-or-create.
	TicketID string   `json:"ticket_id,omitempty"`
	Session  string   `json:"session,omitempty"`
	Links    []string `json:"links,omitempty"`

	// ExpandToDescendants is the tri-state opt-out for the completed-status
	// container rollup cascade. A *bool so json.Unmarshal can distinguish
	// three caller intents: nil (key absent) and &true both cascade (the
	// default-true contract — see cascadeToDescendants); only &false (key
	// present and explicitly false) suppresses the cascade so the update
	// touches only the named container. A plain bool would conflate absent
	// with false and silently disable the cascade for every caller that omits
	// the flag. Same explicit-false-opt-out idiom as search.go's Rerank *bool.
	ExpandToDescendants *bool `json:"expand_to_descendants,omitempty"`

	// raw is the caller's verbatim arguments payload, captured once at the
	// dispatch entry so each arm can account for exactly the params the caller
	// supplied. Never marshaled — it has no json tag and json.Unmarshal leaves
	// unexported fields untouched.
	raw json.RawMessage
}

// cascadeToDescendants reports whether the completed-status container rollup
// should walk the contains tree and cascade to descendants. Default-true: the
// cascade fires when the flag is absent (nil) or explicitly true, and is
// suppressed ONLY when the caller sets expand_to_descendants:false. This keeps
// the long-standing cascade behavior for every caller that omits the flag.
func (a mutateArgs) cascadeToDescendants() bool {
	return a.ExpandToDescendants == nil || *a.ExpandToDescendants
}

// findingReference is the typed wire shape for a single finding
// reference (URL / file / node_id). Mirrors
// projects.ReferenceArgs.
type findingReference struct {
	URL    string `json:"url,omitempty"`
	File   string `json:"file,omitempty"`
	NodeID string `json:"node_id,omitempty"`
	Title  string `json:"title,omitempty"`
}

// InterceptMutate is the client-side mutate dispatch head. It claims the
// knowledge-graph arms of update, delete, create, answer and link, and declines
// everything the engine owns.
//
// EVERY arm runs accountMutateParams at its head, BEFORE any write: the arm
// declares which schema params it consumes, rejects and deliberately ignores,
// and a caller-supplied param the arm classifies as rejected fails the call with
// an error naming the field rather than being silently dropped. See
// mutate_param_accounting.go for the classification and its guarantees.
//
// For backend-backed nodes (those carrying a
// `backend` metadata key) it runs the Linear write-through INLINE
// BEFORE forwarding the knowledge-graph portion of the mutate through the
// login-routed GraphCaller (cloud when logged in). A non-backend non-rollup
// SINGLE-id update on a typed node (criterion/rule/finding) is claimed by the
// per-type router (handleClientMutateUpdateTyped) which routes its first-class
// params into metadata, re-derives the summary, and rejects unroutable params; a
// non-backend single-id node the router declines returns (false,_) to route
// through the cloud-aware engine dispatch. A MULTI-id update batch passes through
// guardBatchUpdateShape — the locked batch contract gate — which loud-rejects
// tracker-backed / per-type-param / source / container-status batches; a
// plain-local-non-container batch with universal scalars survives and returns
// (false,_) so the engine reduces it to a homogeneous Selection.Ids UPDATE.
//
// Returns (false, _) when:
//   - The tool isn't `mutate`.
//   - The graph isn't knowledge (practice / transformers fall through).
//   - The operation isn't update or delete.
//   - The lookup says the node is local-only (single id), or the multi-id batch
//     is contract-valid (passes guardBatchUpdateShape).
//
// Returns (true, errorResult) on:
//   - Any param the selected arm classifies as rejected (the accounting gate),
//     before the arm does any work.
//   - A multi-id update batch carrying any backend-backed id, a per-type param,
//     source, or a container-status (guardBatchUpdateShape rejects).
//   - Mixed delete batches containing any backend-backed node (delete-path guard).
//   - Linear push failure (no local mutation; locked design).
//   - Backend recorded on node but adapter not currently configured.
//   - Linear-succeeded-then-forward-fail desync (operator-visible message).
func InterceptMutate(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "mutate" {
		return false, kgtools.ToolResult{}
	}
	var a mutateArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		// Still DECLINES rather than claiming. A payload that will not parse
		// cannot be attributed to an operation, so this intercept has nothing
		// specific to say about it; claiming the malformed case here is a
		// separate decision with a chain-position consequence and is not made
		// by this arm.
		return false, kgtools.ToolResult{}
	}
	a.raw = params.Arguments

	// An operation outside the declared vocabulary terminates HERE, above every
	// graph-routing branch below and above the degraded-mode return: naming an
	// unknown operation needs no GraphCaller, and a degraded client answering
	// "mutate has no client intercept" is exactly as false as a healthy one
	// doing it. Placement also has to precede the cross-graph link and
	// non-knowledge-graph blocks, or mutate(graph:"practice", operation:"bogus")
	// would keep the misleading engine deny.
	if !mutateOperationDeclared(a.Operation) {
		return true, unknownOperationResult("mutate", a.Operation, mutateDeclaredOperations)
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return false, kgtools.ToolResult{}
	}

	// Cross-graph link: the universal composer is reachable for ANY
	// mutate(link), so a plain link whose FROM or TO is in a foreign graph (e.g.
	// from=code-id to=knowledge-id, no graph/link_graph set) materializes the
	// proxy client-side. handleClientCrossGraphLink is the single decision point:
	// it returns (false,_) for everything it cannot fully resolve — link_graph,
	// both-endpoints-in-knowledge bare links (its FROM-first early skip runs
	// BEFORE any listForeignGraphs Call, so a knowledge↔knowledge link costs zero
	// extra RPCs and returns (false,_) for the cloud-aware engine dispatch to
	// handle as a generic bare link), and unresolvable
	// endpoints. Checked BEFORE the knowledge-graph guard below because a
	// cross-graph link may carry graph:practice / a foreign endpoint.
	if a.Operation == "link" {
		// The composer runs FIRST and accounts its own arm on the paths it
		// CLAIMS (gateCrossGraphLink). Accounting armLinkCrossGraph here instead
		// would apply its stricter surface to every link before the claim is
		// decided, hard-rejecting shapes it never handles — a link on a
		// name-addressed graph carrying `name` routes fine through the engine LINK
		// arm, which consumes name as the Target instance for those families.
		if handled, res := handleClientCrossGraphLink(ctx, deps, a); handled {
			return true, res
		}
		// Not claimed → route through the cloud-aware engine dispatch
		// (generic MUTATION_KIND_LINK Execute) / proxy path. The declined link
		// gets its own accounting here because the engine LINK arm's param
		// surface differs from the client cross-graph composer's.
		if err := accountMutateParams(armLinkFallthrough, a); err != nil {
			return true, errorResult(err.Error())
		}
	}

	// practice/transformers create/update/delete: with no
	// link_graph these are engine-reducible (Phase-1 narrowed compileMutate to
	// link_graph-only), lowering to a Target-routed MutationPlan
	// (Target.Graph==practice/transformers). Route through engine.Dispatch so a
	// reducible op compiles→Execute→render, and a non-reducible one (e.g. a
	// link_graph proxy op) falls back to legacy. The `link` op is intentionally
	// excluded here — handleClientCrossGraphLink above owns the cross-graph link
	// decision tree (intra-practice claim + proxy/link_graph fall-through). Runs
	// BEFORE the knowledge-graph guard below so practice/transformers ops are not
	// dropped to legacy.
	if claimed, res := handleGraphPassthroughMutate(ctx, gc, a, params); claimed {
		return true, res
	}

	if a.Graph != "" && a.Graph != "knowledge" {
		// Backend-backed nodes only live in the knowledge graph.
		//
		// The link conjunct is load-bearing: the link block above does NOT return
		// when the cross-graph composer declines, so a declined link carrying a
		// non-knowledge graph reaches here too. It was already accounted upstream
		// under its own arm, and accounting it a second time under a different
		// spec would reject a call neither arm rejects on its own.
		if a.Operation != "link" {
			if err := accountMutateParams(armNonKnowledgeFallthrough, a); err != nil {
				return true, errorResult(err.Error())
			}
		}
		return false, kgtools.ToolResult{}
	}
	switch a.Operation {
	case "update":
		return handleInterceptMutateUpdate(ctx, deps, a, params)
	case "delete":
		if err := accountMutateParams(armDelete, a); err != nil {
			return true, errorResult(err.Error())
		}
		return handleInterceptMutateDelete(ctx, deps, a)
	case "create":
		return dispatchClientMutateCreate(ctx, deps, a)
	case "answer":
		// Claim mutate(answer).
		if err := accountMutateParams(armAnswer, a); err != nil {
			return true, errorResult(err.Error())
		}
		return true, handleClientMutateAnswer(ctx, deps, a)
	default:
		// This arm now carries only DECLARED operations that decline to an
		// engine arm — the head guard already rejected everything outside the
		// schema vocabulary. Of those, only the ones that decline to a KNOWN
		// engine arm are accounted here; a link was already accounted upstream.
		if err := accountDefaultBucketMutate(a); err != nil {
			return true, errorResult(err.Error())
		}
		return false, kgtools.ToolResult{}
	}
}

// handleInterceptMutateUpdate routes a mutate(update). A multi-id batch (a.ID==""
// after the single-id-as-list normalize) goes through guardBatchUpdateShape — the
// locked batch-shape gate that loud-rejects tracker-backed / per-type-param /
// source / container-status batches and lets a contract-valid plain-local
// batch fall through to the engine reduction. A SINGLE-id update routes in
// precedence order: backend-backed (Linear write-through) → status=completed
// container rollup → per-type first-class-param router (typed knowledge nodes) →
// generic engine dispatch fall-through. Each later arm fires only when the earlier
// ones decline.
//
// The update arms are separately accounted because their param surfaces differ:
// each calls accountMutateParams with its OWN armID once selected, so a param
// routable on one update arm can still be rejected on another. The batch arm is
// the one place accounting runs BEFORE the sibling contract gate rather than
// instead of it — both reject the same set, and the contract gate carries the
// actionable split-into-per-id remedy.
func handleInterceptMutateUpdate(
	ctx context.Context,
	deps ClientDeps,
	a mutateArgs,
	params kgtools.CallToolParams,
) (bool, kgtools.ToolResult) {
	// Normalize single-id-as-list — same shape the server's local
	// routing expects.
	if a.ID == "" && len(a.IDs) == 1 {
		a.ID = a.IDs[0]
		a.IDs = nil
	}

	gc := deps.GraphCaller()
	if a.ID == "" {
		// Multi-id batch path. The single batch-shape gate enforces the locked
		// contract (plain-local-non-container, universal scalars only): it
		// loud-rejects tracker-backed / per-type-param / source / container-status
		// batches BEFORE the engine fall-through. A batch that passes the gate is
		// contract-valid, so it returns (false,_) and the cloud-aware engine
		// dispatch reduces it via compileMutateByIDUpdate to a homogeneous
		// MUTATION_KIND_UPDATE over Selection.Ids and Executes it.
		// The contract gate runs FIRST: it rejects the same params this arm's
		// accounting does, and its message names the per-id remedy the generic
		// accounting error cannot. Its per-id lookups are reads, so a reject from
		// either gate still leaves every node byte-identical.
		if err := guardBatchUpdateShape(ctx, gc, a); err != nil {
			return true, errorResult(err.Error())
		}
		if err := accountMutateParams(armUpdateBatchIDs, a); err != nil {
			return true, errorResult(err.Error())
		}
		return false, kgtools.ToolResult{}
	}

	node, backendName, _, _, _, lookupErr := lookupNodeBackend(ctx, gc, a.ID)
	if lookupErr != nil {
		return true, errorResult("mutate(update): " + lookupErr.Error())
	}
	if backendName == "" {
		return handleLocalOnlyMutateUpdate(ctx, deps, gc, a, node)
	}
	if err := accountMutateParams(armUpdateBackend, a); err != nil {
		return true, errorResult(err.Error())
	}
	// Clear-to-blank has no meaning on a tracker-backed node: the work item lives
	// in an external tracker whose status vocabulary has no blank state, so the
	// write could not be represented there even though it is legal locally.
	// Rejected BEFORE any tracker write, so the item is left untouched.
	if statusExplicitlySupplied(a.raw) && a.Status == "" {
		return true, errorResult(fmt.Sprintf(
			"mutate(update): status cannot be cleared to blank — node %s is backed by the %s tracker, "+
				"which has no blank state; set an explicit status instead",
			a.ID, backendName,
		))
	}
	backend := deps.BackendResolver().ByName(backendName)
	if backend == nil {
		return true, errorResult(fmt.Sprintf(
			"mutate(update): backend %q recorded on node %s but not currently configured",
			backendName, a.ID,
		))
	}

	updateArgs := dispatch.UpdateArgs{NodeID: a.ID}
	if a.Name != "" {
		v := a.Name
		updateArgs.Name = &v
	}
	if a.Description != "" {
		v := a.Description
		updateArgs.Description = &v
	}
	if a.Status != "" {
		v := a.Status
		updateArgs.Status = &v
	}
	if pri, ok := a.Metadata["priority"]; ok && pri != "" {
		v := parsePriority(pri)
		updateArgs.Priority = &v
	}
	if labels, ok := a.Metadata["labels"]; ok && labels != "" {
		v := labels
		updateArgs.Labels = &v
	}

	if err := dispatch.Update(ctx, node, backendName, backend, updateArgs); err != nil {
		return true, errorResult("mutate(update): " + err.Error())
	}

	// Build the forwarded args from a FRESH map (NOT in-place mutation
	// of caller's metadata). stripBackendPrivateMetadata is pure. The
	// knowledge-graph forward runs AFTER the backend dispatch.Update above,
	// routed through the login-aware Execute carrier seam (by-id UPDATE, cloud
	// when logged in) — the ordering + the desync message below are preserved
	// byte-for-byte.
	forwardedArgs := marshalForwardedMutateUpdateArgs(a, backendName)
	if _, err := executeMutate(ctx, gc, forwardedArgs); err != nil {
		return true, errorResult(fmt.Sprintf(
			"Linear update succeeded for %s, but local update failed: %v; the next manual update will not be a no-op until local catches up",
			a.ID, err,
		))
	}
	// Surface a thin success message — the local mutate's result would
	// usually be empty for an update path, and the user typically cares
	// only that both halves landed.
	_ = params // reserved for future passthrough use
	return true, textResult(fmt.Sprintf("mutate(update): backend %q + local update succeeded for %s", backendName, a.ID))
}

// marshalForwardedMutateUpdateArgs builds a fresh JSON payload for the
// forwarded local-only mutate(update). Strips backend-private metadata
// keys from a copy of a.Metadata so the caller's struct is untouched
// (caller-arg-safety for retry idempotency). Typed (vs map[string]any)
// so errchkjson is satisfied.
func marshalForwardedMutateUpdateArgs(a mutateArgs, backendName string) json.RawMessage {
	payload := forwardedMutateUpdatePayload{
		Operation:   "update",
		ID:          a.ID,
		Name:        a.Name,
		Description: a.Description,
		Summary:     a.Summary,
		Content:     a.Content,
		Status:      a.Status,
		Keywords:    a.Keywords,
		// Top-level source is correct HERE even though the per-type router strips
		// it for findings (whose source lives in metadata): a backend-backed node
		// is a tracker-backed work item, never a finding.
		Source:   a.Source,
		Metadata: stripBackendPrivateMetadata(a.Metadata, backendName),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// Cannot fail: typed struct of strings + a string-string map.
		// Defensive return for errchkjson.
		return json.RawMessage("{}")
	}
	return b
}

// forwardedMutateUpdatePayload is the typed wire shape sent to the
// server's handleMutate after a successful Linear update.
type forwardedMutateUpdatePayload struct {
	Operation   string            `json:"operation"`
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Summary     string            `json:"summary,omitempty"`
	Content     string            `json:"content,omitempty"`
	Status      string            `json:"status,omitempty"`
	Keywords    string            `json:"keywords,omitempty"`
	Source      string            `json:"source,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}
