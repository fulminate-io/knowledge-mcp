// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// stripBackendPrivateMetadata returns a copy of m with backend-private
// keys removed: the literal `backend` key, every key prefixed with
// `external_`, and every key prefixed with `<backendName>_`. The
// original map is NOT mutated — callers may pass the caller's
// args.Metadata directly without disturbing it.
//
// Caller-arg-safety contract: this helper is idempotent under retry.
// Linear-succeeds-then-forward-fails desync recovery depends on the
// caller's original arguments being byte-identical between attempts;
// mutating in place would break that contract.
func stripBackendPrivateMetadata(m map[string]string, backendName string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	prefix := backendName + "_"
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch {
		case k == "backend":
			continue
		case strings.HasPrefix(k, "external_"):
			continue
		case backendName != "" && strings.HasPrefix(k, prefix):
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// guardBatchHasNoBackendBacked rejects mutate batches that mix
// backend-backed nodes with local-only nodes. The original OQ1' design
// rejected ANY backend-backed id in a batch; v2 relaxes to mixed only
// because all-backend batches can be safely re-driven on retry (Linear
// treats re-archive of already-archived issues as a no-op success). A
// MIXED batch cannot be cleanly retried — partial Linear state plus
// partial local state is unrecoverable without operator manual work.
//
// Skips ids whose lookup fails or returns no node — local-only ids
// that never resolved produce their own not-found error in the
// forwarded mutate path.
func guardBatchHasNoBackendBacked(ctx context.Context, gc GraphCaller, ids []string) error {
	if len(ids) <= 1 || gc == nil {
		return nil
	}
	var sawLocal, sawBackend bool
	var firstBackendID string
	for _, id := range ids {
		_, backendName, _, _, _, lookupErr := lookupNodeBackend(ctx, gc, id)
		if lookupErr != nil {
			// Defensive: a transport error on lookup means we can't
			// prove the node is backend-backed. Fall through and let
			// the forwarded mutate produce its own not-found / transport
			// error.
			continue
		}
		if backendName == "" {
			sawLocal = true
		} else {
			sawBackend = true
			if firstBackendID == "" {
				firstBackendID = id
			}
		}
		if sawLocal && sawBackend {
			return fmt.Errorf(
				"mixed batches not supported: cannot combine backend-backed nodes with local-only nodes in one mutate; split the call (offending backend-backed id: %s)",
				firstBackendID,
			)
		}
	}
	return nil
}

// guardBatchUpdateShape is the SINGLE batch-shape gate on the mutate(update)
// ids[] path. It enforces the locked batch contract: an ids[] update executes
// ONLY a homogeneous status / universal-scalar update over PLAIN LOCAL
// NON-CONTAINER nodes. Every other batch shape is rejected LOUDLY here — before
// the (false,_) engine fall-through — so no contract-violating batch ever reaches
// the contract-blind compileMutateByIDUpdate reduction.
//
// It runs at most ONE per-id lookup pass (lookupNodeBackend, the same helper +
// loop shape guardBatchHasNoBackendBacked uses), reusing the full *knowledgev1.Node
// each lookup returns so node.Type and the backend tag both come from the one read.
//
// Reject arms (all evaluated in this ONE function — the presence checks
// short-circuit BEFORE the per-id lookup loop, deterministic order):
//   - ARM 2  per-type first-class param (command/criterion_type/scope/
//     enforcement/evidence): the engine compile-local mutateArgs does not declare
//     them and the typed router is single-id-only, so on the ids[] batch they
//     silently vanish → reject. Detected via hasFirstClassUpdateParam (no lookup).
//   - ARM 2b source: NOT universal — finding source lands in metadata, other
//     types in the node field; the engine batch reduction would route it through
//     updateSetFields to the node Source FIELD for every id, a silent wrong-write
//     for finding ids. Excluded from the batch contract entirely → reject on
//     presence (type-independent, no lookup). hasFirstClassUpdateParam deliberately
//     EXCLUDES source, so this is a separate check.
//   - ARM 1  ANY backend-backed id: the batch engine path fires ZERO
//     dispatch.Update calls (the Linear write-through in handleInterceptMutateUpdate
//     requires a.ID != "" and is unreachable for the ids[] batch), so an
//     all-backend batch would silently update knowledge nodes and skip every
//     tracker sync. This SUPERSEDES guardBatchHasNoBackendBacked's mixed-only
//     relaxation on the UPDATE path (the delete path keeps the old guard).
//   - ARM 3  container-status: handleClientUpdateStatusRollup owns the descendant
//     cascade and is unreachable for the ids[] batch, so a status batch over
//     container ids would complete the containers and ORPHAN their descendants.
//     Reject a status update of ANY value touching an isClientRollupContainer id
//     (more conservative than the single-id rollup, which only cascades on
//     terminal status — intentional for the batch path).
//
// ARMs 1 + 3 share the SAME per-id lookup pass (lookupNodeBackend returns the full
// *knowledgev1.Node, so backendName AND node.Type both come from one read). The
// gate does NOT pre-check id existence or empty payload — the engine owns missing-id
// atomicity (whole-batch CodeNotFound) and the empty-payload deny.
func guardBatchUpdateShape(ctx context.Context, gc GraphCaller, a mutateArgs) error {
	if gc == nil {
		return nil
	}
	// Presence-only arms — no per-id lookup needed, so short-circuit first.
	if hasFirstClassUpdateParam(a) {
		return fmt.Errorf(
			"mutate(update, ids): per-type params (command/criterion_type/scope/enforcement/evidence) are not supported on a batch — issue a per-id update",
		)
	}
	if a.Source != "" {
		return fmt.Errorf(
			"mutate(update, ids): source must be updated per-id (finding source lands in metadata; other types in the node field) — split this batch",
		)
	}
	// ONE lookup pass collects each id's backing + type (no N+1). The arms are
	// then evaluated in priority order across the whole batch: arm 1 (backend)
	// DOMINATES arm 3 (container-status) because a tracker-backed node is often
	// ALSO a container (a Linear ticket is both), and the tracker-sync skip is the
	// more severe silent failure — so a backend-backed id anywhere in the batch is
	// reported as the backend reject even if an earlier id is a local container.
	type resolved struct {
		id          string
		backendName string
		nodeType    kgtypes.NodeType
	}
	rs := make([]resolved, 0, len(a.IDs))
	for _, id := range a.IDs {
		node, backendName, _, _, _, lookupErr := lookupNodeBackend(ctx, gc, id)
		if lookupErr != nil {
			// Defensive: a transport error on lookup means we can't prove the
			// node's backing/type — skip it and let the forwarded mutate produce
			// its own not-found / transport error (mirrors guardBatchHasNoBackendBacked).
			continue
		}
		var nt kgtypes.NodeType
		if node != nil {
			nt = kgtypes.NodeType(node.GetType())
		}
		rs = append(rs, resolved{id: id, backendName: backendName, nodeType: nt})
	}
	// ARM 1 — backend-backed (dominates).
	for _, r := range rs {
		if r.backendName != "" {
			return fmt.Errorf(
				"mutate(update, ids): tracker-backed nodes must be updated per-id so tracker (Linear) sync runs — split this batch and issue a per-id update for %s",
				r.id,
			)
		}
	}
	// ARM 3 — container-status (any status value).
	if a.Status != "" {
		for _, r := range rs {
			if isClientRollupContainer(r.nodeType) {
				return fmt.Errorf(
					"mutate(update, ids): status updates on container nodes (project/ticket/plan/phase/step) must be issued per-id so the rollup cascade runs — split this batch and issue a per-id update for %s",
					r.id,
				)
			}
		}
	}
	return nil
}

// lookupNodeBackend issues a single client-side `query` MCP call to
// resolve the node by ID and extract its `backend` metadata + Linear
// identifiers. Returns zero values + nil error when the node has no
// `backend` metadata (caller proceeds local-only).
//
// Wire-shape contract (verified against server-side handleGetNode +
// knowledgeGetNodeEnriched): the response body is a SINGLE JSON object
// — the marshaled knowledgev1.Node, NOT wrapped, NOT an array. We never
// request edges so the response shape stays flat.
//
// include_tombstones:true because delete intercepts can re-archive an
// already-tombstoned node on operator replay; the server-side handler
// was widened in Phase 1 step 0.5 to thread this flag through.
//
// Returns (node, backendName, externalURL, backendID, metadata, err)
// where the first four are extracted from the parsed node's metadata
// and metadata is the full string-map (for caller's downstream use,
// e.g. group_id / group_key lookup).
func lookupNodeBackend(
	ctx context.Context,
	gc GraphCaller,
	nodeID string,
) (*knowledgev1.Node, string, string, string, map[string]string, error) {
	if gc == nil || nodeID == "" {
		return nil, "", "", "", nil, nil
	}
	// render.FetchNode resolves the by-id read over the Execute carrier seam
	// (include_tombstones), returning the fully-hydrated knowledgev1.Node (Metadata
	// populated). A miss / transport-degraded fetch returns nil →
	// caller treats as local-only.
	node, err := render.FetchNode(ctx, gc, nodeID)
	if err != nil {
		return nil, "", "", "", nil, fmt.Errorf("lookup node %q: %w", nodeID, err)
	}
	if node == nil {
		return nil, "", "", "", nil, nil
	}
	metadata := nodeMetadataMap(node)
	backendName := kgtypes.Value(node, "backend")
	if backendName == "" {
		return node, "", "", "", metadata, nil
	}
	return node,
		backendName,
		kgtypes.Value(node, "external_url"),
		kgtypes.Value(node, backendName+"_id"),
		metadata,
		nil
}

// marshalQueryByIDArgs was promoted to render.MarshalQueryByID — see
// cmd/knowledge/internal/projects/render/wire_fetch.go. The render
// package owns this helper now so both backend lookup and assemble
// rendering share one wire shape.

// parsePriority maps the on-disk priority string to the backend int
// codes Linear uses (0=none, 1=urgent, 2=high, 3=medium, 4=low).
// Mirrors the historical server-side parser byte-for-byte. Unknown
// strings become 0 ("no priority").
func parsePriority(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "no priority", "none":
		return 0
	case "urgent":
		return 1
	case "high":
		return 2
	case "medium", "normal":
		return 3
	case "low":
		return 4
	}
	if len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
		return int(s[0] - '0')
	}
	return 0
}

// toolResultText extracts the first text block from a kgtools.ToolResult.
// Most MCP tools emit exactly one text block; we concatenate every text
// block found so the helper is total even when the tool emits multiple.
func toolResultText(res kgtools.ToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}
