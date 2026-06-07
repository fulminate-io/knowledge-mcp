// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/backends/dispatch"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
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

	// Extended fields claimed for create=finding/
	// research/rule + answer dispatch arms.
	Evidence     string             `json:"evidence,omitempty"`
	Source       string             `json:"source,omitempty"`
	Scope        string             `json:"scope,omitempty"`
	Enforcement  string             `json:"enforcement,omitempty"`
	Alternatives string             `json:"alternatives,omitempty"`
	Conclusion   string             `json:"conclusion,omitempty"`
	Concludes    bool               `json:"concludes,omitempty"`
	Findings     string             `json:"findings,omitempty"`
	QuestionID   string             `json:"question_id,omitempty"`
	Supports     string             `json:"supports,omitempty"`
	References   []findingReference `json:"references,omitempty"`
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

// InterceptMutate intercepts mutate(update) and mutate(delete) on
// knowledge-graph nodes. For backend-backed nodes (those carrying a
// `backend` metadata key) it runs the Linear write-through INLINE
// BEFORE forwarding the knowledge-graph portion of the mutate through the
// login-routed GraphCaller (cloud when logged in). Non-backend nodes are
// not claimed here — they return (false,_) and route through the
// cloud-aware engine dispatch.
//
// Returns (false, _) when:
//   - The tool isn't `mutate`.
//   - The graph isn't knowledge (practice / transformers fall through).
//   - The operation isn't update or delete.
//   - The lookup says the node is local-only.
//
// Returns (true, errorResult) on:
//   - Mixed batches containing any backend-backed node (per-OQ1' guard).
//   - Linear push failure (no local mutation; locked design).
//   - Backend recorded on node but adapter not currently configured.
//   - Linear-succeeded-then-forward-fail desync (operator-visible message).
func InterceptMutate(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "mutate" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return false, kgtools.ToolResult{}
	}

	var a mutateArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		// Server will surface its own parse error — don't double-error.
		return false, kgtools.ToolResult{}
	}

	ctx := context.Background()

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
		if handled, res := handleClientCrossGraphLink(ctx, deps, a); handled {
			return true, res
		}
		// Not claimed → route through the cloud-aware engine dispatch
		// (generic MUTATION_KIND_LINK Execute) / proxy path.
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
	if (a.Graph == "practice" || a.Graph == "transformers") && a.LinkGraph == "" &&
		(a.Operation == "create" || a.Operation == "create_batch" || a.Operation == "update" || a.Operation == "delete") {
		ex, eerr := persistExecutor(gc)
		if eerr != nil {
			return true, errorResult("mutate(" + a.Operation + "): " + eerr.Error())
		}
		res, err := engine.Dispatch(ctx, ex.Execute, "mutate", params.Arguments)
		if err != nil {
			return true, errorResult("mutate(" + a.Operation + "): " + err.Error())
		}
		return true, res
	}

	if a.Graph != "" && a.Graph != "knowledge" {
		// Backend-backed nodes only live in the knowledge graph.
		return false, kgtools.ToolResult{}
	}
	switch a.Operation {
	case "update":
		return handleInterceptMutateUpdate(ctx, deps, a, params)
	case "delete":
		return handleInterceptMutateDelete(ctx, deps, a)
	case "create":
		// Claim create=finding/research/rule. Other
		// create types (criterion, knowledge nodes, etc.) fall through —
		// InterceptAddCriterion fires earlier in the chain for criterion;
		// generic create flows to the server.
		switch a.Type {
		case "finding":
			return true, handleClientMutateCreateFinding(ctx, deps, a)
		case "research":
			return true, handleClientMutateCreateResearch(ctx, deps, a)
		case "rule":
			return true, handleClientMutateCreateRule(ctx, deps, a)
		}
		return false, kgtools.ToolResult{}
	case "answer":
		// Claim mutate(answer).
		return true, handleClientMutateAnswer(ctx, deps, a)
	default:
		return false, kgtools.ToolResult{}
	}
}

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
	if err := guardBatchHasNoBackendBacked(ctx, gc, a.IDs); err != nil {
		return true, errorResult(err.Error())
	}
	if a.ID == "" {
		// Non-backend multi-id batch — return (false,_) to route through the
		// cloud-aware engine dispatch.
		return false, kgtools.ToolResult{}
	}

	node, backendName, _, _, _, lookupErr := lookupNodeBackend(ctx, gc, a.ID)
	if lookupErr != nil {
		return true, errorResult("mutate(update): " + lookupErr.Error())
	}
	if backendName == "" {
		// Claim closure-rollup for local-only container updates
		// (status=completed on plan/phase/ticket/project). The client owns the
		// cascade.
		if a.Status == kgtypes.StatusCompleted && node != nil && isClientRollupContainer(kgtypes.NodeType(node.Type)) {
			return true, handleClientUpdateStatusRollup(ctx, gc, a, node)
		}
		// Non-backend non-rollup update — return (false,_) to route through
		// the cloud-aware engine dispatch.
		return false, kgtools.ToolResult{}
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

func handleInterceptMutateDelete(
	ctx context.Context,
	deps ClientDeps,
	a mutateArgs,
) (bool, kgtools.ToolResult) {
	if a.ID == "" && len(a.IDs) == 0 {
		// Server will emit its own "delete requires id=..." error.
		return false, kgtools.ToolResult{}
	}
	ids := a.IDs
	if len(ids) == 0 {
		ids = []string{a.ID}
	}

	gc := deps.GraphCaller()
	if err := guardBatchHasNoBackendBacked(ctx, gc, a.IDs); err != nil {
		return true, errorResult(err.Error())
	}

	var archived []string
	for _, id := range ids {
		node, backendName, _, _, _, lookupErr := lookupNodeBackend(ctx, gc, id)
		if lookupErr != nil {
			return true, errorResult("mutate(delete): " + lookupErr.Error())
		}
		if backendName == "" {
			// Non-backend id — skip; the final routed forward will tombstone it.
			continue
		}
		backend := deps.BackendResolver().ByName(backendName)
		if backend == nil {
			msg := fmt.Sprintf(
				"mutate(delete): backend %q recorded on node %s but not currently configured",
				backendName, id,
			)
			if len(archived) > 0 {
				msg = fmt.Sprintf(
					"%s; Linear archive succeeded for %d node(s) (%s) before this failure. %s",
					msg, len(archived), strings.Join(archived, ","), linearArchiveRetryGuidance,
				)
			}
			return true, errorResult(msg)
		}
		if err := dispatch.Archive(ctx, node, backendName, backend, dispatch.DeleteArgs{NodeID: id}); err != nil {
			msg := fmt.Sprintf("mutate(delete): %v", err)
			if len(archived) > 0 {
				msg = fmt.Sprintf(
					"%s; Linear archive succeeded for %d prior node(s) (%s) before this failure. %s",
					msg, len(archived), strings.Join(archived, ","), linearArchiveRetryGuidance,
				)
			}
			return true, errorResult(msg)
		}
		archived = append(archived, id)
	}

	// Forward the tombstone — the knowledge graph tombstones every id regardless
	// of Linear's involvement — routed through the login-aware Execute carrier
	// seam (by-id DELETE, cloud when logged in). The engine DELETE arm selects
	// via Selection.Ids, so the forward
	// carries the normalized PLURAL ids[] (a singular caller `id` was folded
	// into ids above); the caller's graph/format are preserved. Reuses
	// params.Arguments-equivalent intent without the singular-id wire shape the
	// generic delete arm does not reduce.
	forwardedDelete, derr := json.Marshal(struct {
		Operation string   `json:"operation"`
		IDs       []string `json:"ids"`
		Graph     string   `json:"graph,omitempty"`
		Language  string   `json:"language,omitempty"`
	}{Operation: "delete", IDs: ids, Graph: a.Graph, Language: a.Language})
	if derr != nil {
		return true, errorResult("mutate(delete): marshal forward: " + derr.Error())
	}
	if _, err := executeMutate(ctx, gc, forwardedDelete); err != nil {
		if len(archived) > 0 {
			return true, errorResult(fmt.Sprintf(
				"Linear archive succeeded for %d node(s) (%s), but local delete failed: %v. %s",
				len(archived), strings.Join(archived, ","), err, linearArchiveRetryGuidance,
			))
		}
		return true, errorResult(fmt.Sprintf("mutate(delete): local delete failed: %v", err))
	}
	return true, textResult(fmt.Sprintf(
		"mutate(delete): archived %d node(s) in the external tracker + tombstoned %d node(s) in the knowledge graph",
		len(archived), len(ids),
	))
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
		Metadata:    stripBackendPrivateMetadata(a.Metadata, backendName),
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
	Metadata    map[string]string `json:"metadata,omitempty"`
}
