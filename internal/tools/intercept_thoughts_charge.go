// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_charge.go — client-side claim for
// thoughts(operation:charge). The intercept LOWERS the charge into a GENERIC
// create_batch MutationPlan via the Execute carrier seam (no dedicated server
// charge handler): it reproduces handleMutateCreateCharge's invariants
// client-side — polarity/weight validation, a thought/finding/research target
// gate, the charge node layout, EdgeChargedBy (thought_parent→charge) + EdgeEvidencedBy
// (charge→evidence) edges, and the 3-outcome cross-graph evidence resolve
// matching the server's ResolveOrProxy. After the create, it re-pulls charges
// via the bulk charges_for helper and computes thought properties locally.
//
// The render is NO LONGER identical to the server's: it adds a
// "Charged: <id> (<type>)" line naming the RESOLVED target, immediately after
// the charge-id line. A caller who charged by prefix would otherwise have no way
// to learn the full id the write and the readout were keyed on.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
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
	Summary   string   `json:"summary"`
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
		return errorResult("invalid arguments: " + decodeArgsError(params.Arguments, err))
	}
	if a.Thought == "" {
		a.Thought = a.ThoughtID
	}
	if verr := validateChargeArgs(a); verr != "" {
		return errorResult(verr)
	}
	clampedSummary, summaryWarn, serr := validate.ClampSummary("thoughts(charge)", "summary", a.Summary)
	if serr != nil {
		return errorResult(serr.Error())
	}

	// Verify the parent exists and is a chargeable claim node — thought, finding,
	// or research. A missing / non-chargeable target is rejected with the same
	// messages. The downstream charge-node create + property recompute are
	// node-agnostic, so a charged finding/research accrues the same
	// valence/magnitude a thought does.
	parent, ferr := render.FetchNode(ctx, gc, a.Thought)
	if ferr != nil {
		return errorResult(fmt.Sprintf("thought lookup failed for %s - no charge was recorded: %s", a.Thought, ferr.Error()))
	}
	if parent == nil || parent.Id == "" {
		return errorResult(fmt.Sprintf("thought %s not found - no charge was recorded", a.Thought))
	}
	switch kgtypes.NodeType(parent.Type) {
	case kgtypes.NodeThought, kgtypes.NodeFinding, kgtypes.NodeResearch:
		// chargeable claim node — accept.
	default:
		return errorResult(fmt.Sprintf("charge target %s is type %q, must be one of thought/finding/research (charges carry valence/magnitude that apply to chargeable claim nodes)",
			a.Thought, parent.Type))
	}

	// THE SINGLE RESOLUTION POINT. The lookup above is a ById plan, which the
	// server resolves through its prefix resolver, so for a unique >=8-char
	// prefix parent.Id is the full 32-char ID. Everything downstream keys on
	// this — the written edge, the readout and its map key — so the write and
	// the report can never disagree about which node was charged. The
	// caller-typed form survives only in the error messages above, which should
	// echo what the caller actually sent.
	target := parent.Id

	// Resolve cross-graph evidence to proxies CLIENT-SIDE (3-outcome mirror of
	// the server ResolveOrProxy). Enumerate the foreign graph list once.
	resolvedEvidence := resolveChargeEvidence(ctx, gc, a.Evidence)

	// Build the charge node + edges and lower onto the generic create_batch.
	// SymbolName stays a truncation of the reasoning: that is a NAME derivation,
	// which is out of scope. Summary is the author's.
	chargeNode := knowledgev1.Node{
		Type:       string(kgtypes.NodeCharge),
		Source:     "llm:claude",
		SymbolName: truncateAtWordCreate(a.Reasoning, 60),
		Summary:    clampedSummary,
		Content:    a.Reasoning,
	}
	kgtypes.SetValue(&chargeNode, "polarity", a.Polarity)
	kgtypes.SetValue(&chargeNode, "weight", fmt.Sprintf("%.2f", a.Weight))

	// Edges: EdgeChargedBy thought_parent → charge(slot 0); EdgeEvidencedBy
	// charge(slot 0) → each resolved evidence id.
	edges := []kgwire.BatchEdge{
		{FromID: target, FromIdx: -1, ToIdx: 0, Type: kgtypes.EdgeChargedBy},
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

	// Bulk-fetch the charge set for the property recompute, keyed on the RESOLVED
	// target so the readout looks up the same node the edge above was written
	// against. Keying this on the caller's raw string is what made a
	// prefix-charged thought report an all-zero property block.
	chargesByThought := clientthought.FetchChargesFor(ctx, gc, []string{target})
	now := time.Now()
	props := clientthought.ComputePropertiesFromCharges(chargesByThought[target], now)

	// The charge-id line stays first among the BODY lines: the bench harness
	// extracts the id with `ID:\s*([0-9a-f]{16,})` (harness_battery.go
	// idAfterMarkerPattern), so what it keys on is the "ID:" MARKER, and the
	// Charged: line below carries none. The clamp advisory renders ABOVE the body
	// through the shared warnings renderer like every other arm, and cannot be
	// mistaken for the id because it carries no such marker. The Charged: line
	// follows the id and names the resolved node, so a caller who passed a prefix
	// can copy the full id instead of guessing and re-charging.
	var sb strings.Builder
	fmt.Fprintf(&sb, "Charge recorded → ID: %s\nCharged: %s (%s)\nThought properties:\n  Valence: %.3f\n  Magnitude: %.3f\n  Consistency: %.3f\n  Self-trust: %.3f\n  Charges: %d (positive: %.1f, negative: %.1f)",
		chargeID, target, parent.Type, props.Valence, props.Magnitude, props.Consistency, props.SelfTrust,
		props.ChargeCount, props.PositiveWeight, props.NegativeWeight)
	if summaryWarn != "" {
		writeClientWarningsSection(&sb, []string{summaryWarn})
	}
	return textResult(sb.String())
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
// (routing.go:234-250): (a) in knowledge → the RESOLVED node id; (b) in a scan-eligible
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
// shared by the charge-evidence and think-links composers: knowledge hit → the
// RESOLVED node id (so a prefix endpoint rides the wire already resolved, matching
// the charged-by endpoint rather than disagreeing with it);
// foreign hit → build+upsert proxy → proxy id; no hit (or proxy failure) → raw
// id AS-IS (server outcome c — best-effort dangling, NOT dropped). An empty id
// returns "" so callers can skip it (mirrors the server's empty-entry skip). The
// proxy materialization rides the single-owner crossgraph package; a nil ex (no
// Execute seam) skips the foreign-proxy arm.
func resolveCrossGraphID(ctx context.Context, gc GraphCaller, ex render.Executor, graphs []crossgraph.ForeignGraph, id string) string {
	out, _ := resolveCrossGraphIDOutcome(ctx, gc, ex, graphs, id)
	return out
}

// resolveCrossGraphIDOutcome is resolveCrossGraphID plus the outcome bit: true
// when the id resolved (outcome a or b), false for outcome c — the raw
// best-effort fall-through — and for an empty id. The bit CANNOT be recovered
// from the returned string: outcome (a) on an already-full knowledge id returns
// that same id, which is byte-identical to the outcome-(c) fall-through. Callers
// that report resolution to the user (the think write receipt) need the bit; the
// charge-evidence composer, which acts identically either way, uses the wrapper.
func resolveCrossGraphIDOutcome(ctx context.Context, gc GraphCaller, ex render.Executor, graphs []crossgraph.ForeignGraph, id string) (string, bool) {
	if id == "" {
		return "", false
	}
	// (a) Knowledge → the RESOLVED node id (the lookup already resolved a prefix).
	if known, ferr := render.FetchNodeIn(ctx, gc, id, "knowledge", ""); ferr == nil && known != nil && known.Id != "" {
		return known.Id, true
	}
	// (b) Foreign → deterministic proxy id (knowledge-target proxy).
	if ex != nil {
		gt, name, node, found := crossgraph.LocateForeignNode(ctx, gc, graphs, id)
		if found {
			if proxy, uerr := crossgraph.UpsertForeignProxy(ctx, ex, "knowledge", gt, name, id, node); uerr == nil {
				return proxy.Id, true
			}
			// proxy build/upsert failed → fall through to raw (best-effort).
		}
	}
	// (c) No hit (or proxy failure) → raw id as-is (server outcome c).
	return id, false
}

// truncateAtWordCreate truncates s to at most maxLen RUNES at a word
// boundary, never splitting a multibyte UTF-8 sequence (so the result is
// always valid UTF-8). Adapted from the server-side truncateAtWordCreate
// (tools_mutate_create_thought.go:170) — the charge/think composers need the
// SAME SymbolName the server produced; the client cannot import the server-side
// package (import boundary), so this is the design-locked transitional
// duplication — the same justification the engine test fixtures record for
// re-declaring the store's exact edge-type comparison across the same
// boundary. Made rune-correct here: maxLen counts
// runes, not bytes, and the word-boundary cut operates on the rune-bounded
// prefix.
func truncateAtWordCreate(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	prefix := string([]rune(s)[:maxLen])
	idx := strings.LastIndex(prefix, " ")
	if idx <= 0 {
		return prefix
	}
	return prefix[:idx]
}
