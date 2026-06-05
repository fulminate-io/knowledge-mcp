// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_charge.go — client-side claim for
// thoughts(operation:charge). The intercept LOWERS the charge into a GENERIC
// create_batch MutationPlan via the Execute carrier seam (no dedicated server
// charge handler): it reproduces handleMutateCreateCharge's invariants
// client-side — polarity/weight validation, NodeThought-only target gate, the
// charge node layout, EdgeChargedBy (thought_parent→charge) + EdgeEvidencedBy
// (charge→evidence) edges, and the 3-outcome cross-graph evidence resolve
// matching the server's ResolveOrProxy. After the create, it re-pulls charges
// via the bulk charges_for helper and computes thought properties locally — the
// identical "Charge recorded → ... Thought properties: ..." render.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// chargeArgs is the parsed thoughts(operation:charge) shape. Mirrors
// tools_thought.go:188-196 — ThoughtID alias because callers sometimes
// pass `thought_id` instead of `thought`.
type chargeArgs struct {
	Thought   string   `json:"thought"`
	ThoughtID string   `json:"thought_id"`
	Polarity  string   `json:"polarity"`
	Weight    float64  `json:"weight"`
	Reasoning string   `json:"reasoning"`
	Evidence  []string `json:"evidence"`
}

// handleChargeClient claims thoughts(operation:charge) and lowers it onto a
// generic create_batch MutationPlan (charge node + EdgeChargedBy + EdgeEvidencedBy).
//
// PERF: the create is ONE Execute (via PersistBatch); the parent-thought verify
// is ONE render.FetchNode; the cross-graph evidence resolve enumerates the
// foreign graph list ONCE (listForeignGraphs) and reuses it across all evidence
// IDs (bounded by len(evidence) probes). The property recompute is the existing
// bulk charges_for + ComputePropertiesFromCharges. These extra reads vs the old
// single server Call are accepted — the equivalent server work happened in-process.
func handleChargeClient(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("charge: graph caller unavailable")
	}

	var a chargeArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if a.Thought == "" {
		a.Thought = a.ThoughtID
	}
	if verr := validateChargeArgs(a); verr != "" {
		return errorResult(verr)
	}

	// Verify the parent exists and is a NodeThought (mirrors handleMutateCreate-
	// Charge's AddCharge guard). A missing / non-thought target is rejected with
	// the same messages.
	parent, ferr := render.FetchNode(ctx, gc, a.Thought)
	if ferr != nil {
		return errorResult(fmt.Sprintf("thought lookup failed for %s: %s", a.Thought, ferr.Error()))
	}
	if parent == nil || parent.Id == "" {
		return errorResult(fmt.Sprintf("thought %s not found", a.Thought))
	}
	if kgtypes.NodeType(parent.Type) != kgtypes.NodeThought {
		return errorResult(fmt.Sprintf("charge target %s is type %q, must be %q (charges carry valence/magnitude/consistency that only apply to thoughts)",
			a.Thought, parent.Type, kgtypes.NodeThought))
	}

	// Resolve cross-graph evidence to proxies CLIENT-SIDE (3-outcome mirror of
	// the server ResolveOrProxy). Enumerate the foreign graph list once.
	resolvedEvidence := resolveChargeEvidence(ctx, gc, a.Evidence)

	// Build the charge node + edges and lower onto the generic create_batch.
	chargeNode := knowledgev1.Node{
		Type:       string(kgtypes.NodeCharge),
		Source:     "llm:claude",
		SymbolName: truncateAtWordCreate(a.Reasoning, 60),
		Content:    a.Reasoning,
	}
	kgtypes.SetValue(&chargeNode, "polarity", a.Polarity)
	kgtypes.SetValue(&chargeNode, "weight", fmt.Sprintf("%.2f", a.Weight))

	// Edges: EdgeChargedBy thought_parent → charge(slot 0); EdgeEvidencedBy
	// charge(slot 0) → each resolved evidence id.
	edges := []kgwire.BatchEdge{
		{FromID: a.Thought, FromIdx: -1, ToIdx: 0, Type: kgtypes.EdgeChargedBy},
	}
	for _, evID := range resolvedEvidence {
		if evID == "" {
			continue
		}
		edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: -1, ToID: evID, Type: kgtypes.EdgeEvidencedBy})
	}

	ids, err := PersistBatch(ctx, gc, []*knowledgev1.Node{&chargeNode}, edges, "")
	if err != nil {
		return errorResult("charge: " + err.Error())
	}
	if len(ids) == 0 {
		return errorResult("charge: create returned no id")
	}
	chargeID := ids[0]

	// Bulk-fetch the new charge set for property computation (identical tail).
	// The GraphClient is the concrete *graphclient.GraphClient FetchChargesFor needs;
	// production always wires it. When it is unavailable (degraded boot), the
	// charge has already landed — render the bare ID rather than failing the write.
	graphCli := deps.GraphCaller()
	if graphCli == nil {
		return textResult(fmt.Sprintf("Charge recorded → ID: %s", chargeID))
	}
	chargesByThought := clientthought.FetchChargesFor(ctx, graphCli, []string{a.Thought})
	props := clientthought.ComputePropertiesFromCharges(chargesByThought[a.Thought])

	msg := fmt.Sprintf("Charge recorded → ID: %s\nThought properties:\n  Valence: %.3f\n  Magnitude: %.3f\n  Consistency: %.3f\n  Self-trust: %.3f\n  Charges: %d (positive: %.1f, negative: %.1f)",
		chargeID, props.Valence, props.Magnitude, props.Consistency, props.SelfTrust,
		props.ChargeCount, props.PositiveWeight, props.NegativeWeight)
	return textResult(msg)
}

// validateChargeArgs reproduces the charge validation gate. Returns "" when
// valid, else the LLM-facing error message. thought/polarity/weight checks
// mirror the thoughts(charge) contract (thought-required) + handleMutateCreate-
// Charge (polarity/weight).
func validateChargeArgs(a chargeArgs) string {
	if a.Thought == "" {
		return "charge requires 'thought' (the thought node ID)"
	}
	if a.Polarity != "positive" && a.Polarity != "negative" {
		return fmt.Sprintf("charge: polarity must be 'positive' or 'negative', got %q", a.Polarity)
	}
	if a.Weight <= 0 {
		return fmt.Sprintf("charge: weight must be > 0, got %f (recommended range 1-10; zero records a no-op charge)", a.Weight)
	}
	return ""
}

// resolveChargeEvidence resolves each evidence id to the id the EdgeEvidencedBy
// edge should target, reproducing the server ResolveOrProxy's 3 outcomes
// (routing.go:234-250): (a) in knowledge → raw id; (b) in a scan-eligible
// foreign graph (code/practice/cloud/cicd) → build+upsert proxy, use proxy id;
// (c) NOT found anywhere → raw id AS-IS (best-effort dangling, NOT dropped —
// this composer has no legacy fall-through). The foreign graph list is
// enumerated ONCE and reused across all evidence ids.
func resolveChargeEvidence(ctx context.Context, gc GraphCaller, evidence []string) []string {
	if len(evidence) == 0 {
		return nil
	}
	// Enumerate foreign graphs once (best-effort: on failure, every id falls back
	// to the knowledge-or-raw outcome, never dropped). A missing Execute seam →
	// no foreign graphs → every id resolves knowledge-or-raw.
	ex, _ := persistExecutor(gc)
	var graphs []crossgraph.ForeignGraph
	if ex != nil {
		graphs, _ = crossgraph.ListForeignGraphs(ctx, ex)
	}
	out := make([]string, 0, len(evidence))
	for _, e := range evidence {
		out = append(out, resolveCrossGraphID(ctx, gc, ex, graphs, e))
	}
	return out
}

// resolveCrossGraphID is the per-id ResolveOrProxy mirror (routing.go:234-250),
// shared by the charge-evidence and think-links composers: knowledge hit → raw;
// foreign hit → build+upsert proxy → proxy id; no hit (or proxy failure) → raw
// id AS-IS (server outcome c — best-effort dangling, NOT dropped). An empty id
// returns "" so callers can skip it (mirrors the server's empty-entry skip). The
// proxy materialization rides the single-owner crossgraph package; a nil ex (no
// Execute seam) skips the foreign-proxy arm.
func resolveCrossGraphID(ctx context.Context, gc GraphCaller, ex render.Executor, graphs []crossgraph.ForeignGraph, id string) string {
	if id == "" {
		return ""
	}
	// (a) Knowledge → raw id.
	if known, ferr := render.FetchNodeIn(ctx, gc, id, "knowledge", ""); ferr == nil && known != nil && known.Id != "" {
		return id
	}
	// (b) Foreign → deterministic proxy id (knowledge-target proxy).
	if ex != nil {
		gt, name, node, found := crossgraph.LocateForeignNode(ctx, gc, graphs, id)
		if found {
			if proxy, uerr := crossgraph.UpsertForeignProxy(ctx, ex, "knowledge", gt, name, id, node); uerr == nil {
				return proxy.Id
			}
			// proxy build/upsert failed → fall through to raw (best-effort).
		}
	}
	// (c) No hit (or proxy failure) → raw id as-is (server outcome c).
	return id
}

// truncateAtWordCreate truncates s to at most maxLen characters at a word
// boundary. Verbatim from the server-side truncateAtWordCreate
// (tools_mutate_create_thought.go:170) — the charge/think composers need the
// SAME SymbolName the server produced; the client cannot import the server-side
// package (import boundary), so this is the design-locked transitional
// duplication (cf. canonicalEdgeCasing).
func truncateAtWordCreate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	idx := strings.LastIndex(s[:maxLen], " ")
	if idx <= 0 {
		return s[:maxLen]
	}
	return s[:idx]
}
