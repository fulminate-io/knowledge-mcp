// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"

	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// debugLogWriteback logs the EXACT node ids of a writeback batch + the target
// graph name. Pairs with debugLogGapItems at discovery: comparing the gap's
// source GraphName against this writeback target reveals whether the
// vector/summary lands on the same layer the next gap-scan reads. Debug level —
// off unless debug logging is enabled.
func debugLogWriteback(axis, graphName string, items []updateBatchItem) {
	ids := make([]string, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].ID)
	}
	slog.Debug("pipeline.writeback: ids", "axis", axis, "target_graph", graphName, "count", len(ids), "ids", strings.Join(ids, " "))
}

// WireClient is the narrow surface the pipeline uses to issue RPCs against
// the graph server. *server.GraphClient satisfies this interface naturally
// (it has both PipelineScan and Execute); the package exposes only the two
// methods to keep the test seam small. Tests inject a fake WireClient with a
// call counter + canned response map.
//
// Execute is the engine seam: writeBatchUpdates compiles its update_batch
// into a MUTATION_KIND_UPDATE_ITEMS MutationPlan (engine.Compile) and runs it
// through Execute, so the per-item batch write rides the engine arm rather than
// the legacy mutate(update_batch) handler. PipelineScan is the dedicated
// index-gap-discovery RPC — gap discovery is NOT engine.Compile-
// reducible (it needs the server's per-axis dirty-gen state), so it rides its
// own typed EngineService.PipelineScan RPC rather than the Execute seam.
type WireClient interface {
	PipelineScan(ctx context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error)
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// BackendResolver is the OPTIONAL login-aware seam the Pipeline consults to
// (a) bind a CONCRETE backend per collector at RegisterGraph time and
// (b) detect a login-state transition so refreshOnce can tear down + rebind
// every collector (Hazard B). The production p.client (the bootstrap
// routedWireClient over *graphclient.Router) implements it; the Pipeline
// type-asserts p.client to this interface and falls back to p.client +
// no-flip-detection when the assertion fails (test fakes).
//
//   - Backend(ctx) returns the concrete backend WireClient the current login
//     state selects (cloud when logged in, local otherwise). The collector
//     scans through it AND stamps it on every emitted work item.
//   - LoggedIn(ctx) reports the live login state so refreshOnce can compare it
//     against the prior tick's state and force a full collector rebind on a flip.
type BackendResolver interface {
	Backend(ctx context.Context) (WireClient, error)
	LoggedIn(ctx context.Context) bool
}

// scanGaps issues a PipelineScan RPC for one (gt, name, axis, limit).
// Returns (items, dirty_gen, err). Empty items + nil err is the common
// case; the caller treats nil err as "scan succeeded; act on items".
// lastSeenGen is the dirty_gen the caller received from its previous
// scan against this (gt, name, axis); when non-zero AND equal to the
// server's current gen, the server short-circuits and returns empty
// items without iterating the node map.
func scanGaps(ctx context.Context, c WireClient, gt kgtypes.GraphType, name, axis string, limit int, lastSeenGen uint64) ([]*knowledgev1.PipelineScanItem, uint64, error) {
	resp, err := c.PipelineScan(ctx, &knowledgev1.PipelineScanRequest{
		GraphType:   string(gt),
		GraphName:   name,
		Axis:        axis,
		Limit:       int32(limit),
		LastSeenGen: lastSeenGen,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("pipeline.rpc: PipelineScan %s/%s (%s): %w", gt, name, axis, err)
	}
	return resp.GetItems(), resp.GetDirtyGen(), nil
}

// pipelineEligibleGraphTypes is the BUILTIN base of the set of graph types the
// LLM pipeline drains. The eligible-type filter is a client concern (the pipeline
// owns which types it summarizes/embeds), so it lives here. Registered custom
// GraphTypeDef types are discovered dynamically each tick
// (discoverRegisteredGraphTypes) and folded in on top of this base — the server's
// per-axis gap shims no-op a both-false custom type cheaply, so the client
// enumerates every registered type unconditionally and lets the server gate
// per-behavior (the registered behavior-config is honored server-side; the
// client applies no behavior gate).
var pipelineEligibleGraphTypes = []kgtypes.GraphType{
	kgtypes.GraphKnowledge,
	kgtypes.GraphCode,
	kgtypes.GraphPractice,
	kgtypes.GraphCloud,
	kgtypes.GraphCICD,
	kgtypes.GraphTransformers,
}

// discoverRegisteredGraphTypes issues ONE query(type:graph_type_def) Execute and
// returns the registered custom graph type names discovered this tick. It
// replicates graphtypecrud.Client.List's browse+decode shape (compile a
// type-browse query, Execute, DecodeNodes, read node.SymbolName — ToNode stores
// the registered name as both ID and SymbolName) WITHOUT importing graphtypecrud,
// whose CRUD/validation surface the pipeline does not need.
//
// Returns (discovered, rlHint, sawRateLimit). A browse error is NON-FATAL: it is
// logged and the empty slice is returned so the tick proceeds with builtins only
// (identical to the per-type modules skip below). A rate-limit on the browse is
// surfaced via (rlHint, sawRateLimit) so it can feed the SAME whole-tick-throttle
// accounting listLoadedGraphs already runs — but the CALLER must NOT add this
// browse to `succeeded`, so a registry-browse 429 alone never forces backoff when
// the builtin types enumerated fine. Builtin-named types are filtered out
// (defensive dedupe) so a registered type can never duplicate a builtin tick.
func discoverRegisteredGraphTypes(ctx context.Context, c WireClient) (discovered []kgtypes.GraphType, rlHint time.Duration, sawRateLimit bool) {
	rawArgs, err := json.Marshal(map[string]any{
		"type":  string(kgtypes.NodeGraphTypeDef),
		"limit": graphTypeDefListLimit,
	})
	if err != nil {
		slog.Warn("pipeline.rpc: marshal graph_type_def browse args failed; skipping custom types this tick", "error", err)
		return nil, 0, false
	}
	req, ok := engine.Compile("query", rawArgs)
	if !ok {
		slog.Warn("pipeline.rpc: graph_type_def browse query did not compile; skipping custom types this tick")
		return nil, 0, false
	}
	resp, err := c.Execute(ctx, req)
	if err != nil {
		if hint, isRL := rateLimitHint(err); isRL {
			rlHint = hint
			sawRateLimit = true
		}
		slog.Warn("pipeline.rpc: graph_type_def browse failed; skipping custom types this tick", "error", err)
		return nil, rlHint, sawRateLimit
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		slog.Warn("pipeline.rpc: graph_type_def browse decode failed; skipping custom types this tick", "error", derr)
		return nil, 0, false
	}
	for _, n := range nodes {
		name := n.GetSymbolName()
		if name == "" || kgtypes.IsBuiltinGraphType(name) {
			continue
		}
		discovered = append(discovered, kgtypes.GraphType(name))
	}
	return discovered, 0, false
}

// graphTypeDefListLimit caps the graph_type_def browse. Mirrors
// graphtypecrud.graphTypeListLimit — a large explicit limit so the registry
// enumeration is never silently truncated by a small default.
const graphTypeDefListLimit = 100000

// listLoadedGraphs returns every (gt, name) pair the pipeline should drain,
// composed CLIENT-SIDE over the generic RETURN_MODE_GRAPH_NAMES read (a
// pure all-types graph enumeration, not pipeline floor). It reproduces the
// server's old handlePipelineListGraphs: seed the explicit {knowledge, default}
// entry (ListGraphsLite(GraphKnowledge) enumerates only the situation-overlay
// subdir, so the root knowledge graph would otherwise be missed), then one
// query(mode:modules) Execute per eligible type, appending each decoded
// GraphInfo.Name. The eligible-type set is the builtin base
// (pipelineEligibleGraphTypes) PLUS the registered custom GraphTypeDef types
// discovered this tick (discoverRegisteredGraphTypes), so a registered custom
// graph's loaded names are drained alongside the builtins. Used by the refresh
// goroutine, which dedupes by graphKey — set membership, not order, is the
// invariant. Mirrors the D6 in-package engine.Compile+Execute+DecodeGraphNames
// template.
//
// Throttle reporting: when EVERY eligible type fails to enumerate AND at least
// one of those failures is a remote rate-limit (429), the tick made zero
// progress purely because the backend is throttling us. listLoadedGraphs
// surfaces that as (rlThrottled=true, rlHint=max Retry-After seen) so the
// discovery loop can back off instead of re-firing one query-per-type at the
// base cadence — the same #3 scan-error insurance the per-graph collector loop
// already has (collector.go runLoop). Without it a sustained 429 turns the
// CloudTick/Tick poll into a tight retry storm against the shared backend rate
// limiter (the bug class backoff.go documents for the worker pool).
func listLoadedGraphs(ctx context.Context, c WireClient) ([]GraphRef, map[kgtypes.GraphType]bool, time.Duration, bool) {
	out := []GraphRef{{GraphType: kgtypes.GraphKnowledge, GraphName: "default"}}
	// Discover registered custom GraphTypeDef types this tick and fold them onto
	// the builtin base. A discovery failure is non-fatal (custom types skipped,
	// builtins still enumerate); a discovery rate-limit seeds the SAME
	// sawRateLimit/rlHint accounting the per-type loop uses, but the browse is
	// deliberately NOT added to `succeeded` — a registry-browse 429 alone must
	// never force whole-tick backoff when the builtin types enumerated fine.
	discovered, discRLHint, discSawRateLimit := discoverRegisteredGraphTypes(ctx, c)
	graphTypes := make([]kgtypes.GraphType, 0, len(pipelineEligibleGraphTypes)+len(discovered))
	graphTypes = append(graphTypes, pipelineEligibleGraphTypes...)
	graphTypes = append(graphTypes, discovered...)

	// succeeded records which graph types this tick actually enumerated. A type's
	// per-type failure (a rollout 502, a permission_denied, a decode error) is
	// NON-FATAL: we skip that type this tick rather than abort the whole refresh —
	// one type's failure must never wedge enrichment for every other graph (the
	// resilience gap that left the pipeline stalled across a backend rollout). The
	// caller (refreshOnce) only unregisters collectors within successfully-
	// enumerated types, so a failing type's existing collectors are preserved and
	// re-converge on a later clean tick.
	succeeded := make(map[kgtypes.GraphType]bool, len(graphTypes))
	rlHint := discRLHint
	sawRateLimit := discSawRateLimit
	for _, gt := range graphTypes {
		rawArgs, err := json.Marshal(map[string]any{
			"graph":  string(gt),
			"mode":   "modules",
			"format": "json",
		})
		if err != nil {
			slog.Warn("pipeline.rpc: marshal list-graphs args failed; skipping type this tick", "graph_type", gt, "error", err)
			continue
		}
		req, ok := engine.Compile("query", rawArgs)
		if !ok {
			slog.Warn("pipeline.rpc: list-graphs query did not compile; skipping type this tick", "graph_type", gt)
			continue
		}
		resp, err := c.Execute(ctx, req)
		if err != nil {
			if hint, isRL := rateLimitHint(err); isRL {
				sawRateLimit = true
				if hint > rlHint {
					rlHint = hint
				}
			}
			slog.Warn("pipeline.rpc: list-graphs failed; skipping type this tick", "graph_type", gt, "error", err)
			continue
		}
		infos, derr := engine.DecodeGraphNames(resp)
		if derr != nil {
			slog.Warn("pipeline.rpc: list-graphs decode failed; skipping type this tick", "graph_type", gt, "error", derr)
			continue
		}
		succeeded[gt] = true
		for _, info := range infos {
			if info.Name == "" {
				continue
			}
			out = append(out, GraphRef{GraphType: gt, GraphName: info.Name})
		}
	}
	// rlThrottled only when the WHOLE tick was lost to rate-limiting — a partial
	// failure (some types enumerated) is already absorbed by the per-type skip
	// above and must not back off discovery for the healthy types.
	return out, succeeded, rlHint, len(succeeded) == 0 && sawRateLimit
}

// GraphRef is one (gt, name) pair returned by listLoadedGraphs. Public so
// the refresh goroutine can pass it back into RegisterGraph / build a diff
// set without exposing the wire-shape entry type.
type GraphRef struct {
	GraphType kgtypes.GraphType
	GraphName string
}

// updateBatchItem is one row in a mutate(update_batch) call. Mirrors the
// server-side mutateUpdateBatchItem shape exactly.
type updateBatchItem struct {
	ID           string            `json:"id"`
	Summary      *string           `json:"summary,omitempty"`
	Keywords     *string           `json:"keywords,omitempty"`
	BinaryVector []byte            `json:"binary_vector,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// updateBatchArgs is the wrapped arguments for mutate(operation:"update_batch").
// Graph/Repo/Account/Name route the write to the right backing DB —
// without them, mutate defaults to the knowledge graph and code-graph
// summary/embed writes silently land in the wrong place. Same routing
// shape as fetchNodes; mirrors server's graphSelectorFromArgs.
//
// Branch carries the overlay dimension a branch-overlay-resident write must
// target. The gap scan tags overlay-resident GapItems with the overlay-qualified
// GraphName ("repo@branch"); writeBatchUpdates splits that, routing the bare base
// to Repo and the overlay to Branch so the write lands on the SAME overlay layer
// the scan read from. Empty for base/default-branch writes.
type updateBatchArgs struct {
	Operation string            `json:"operation"`
	Graph     string            `json:"graph,omitempty"`
	Repo      string            `json:"repo,omitempty"`
	Account   string            `json:"account,omitempty"`
	Name      string            `json:"name,omitempty"`
	Language  string            `json:"language,omitempty"`
	Branch    string            `json:"branch,omitempty"`
	Items     []updateBatchItem `json:"items"`
}

// writeBatchUpdates issues exactly ONE mutate(update_batch) call per
// invocation. Empty items is a no-op (no RPC fired).
//
// graphType + graphName REQUIRED for the same reason fetchNodes requires
// them — the server's mutate dispatcher needs to resolve the right
// backing DB or the write lands on the knowledge graph default and the
// pipeline silently never makes progress.
//
// graphName may be overlay-qualified ("repo@branch") when the gap scan read
// from a branch overlay. We split it on "@": the bare base routes to the
// instance key (Repo for code) and the branch threads through args.Branch →
// the Execute Target so the write lands on the SAME overlay layer the scan
// read from. Without the split the overlay dimension is lost and the write
// resolves the base graph, failing not_found for overlay-resident nodes (and
// discarding the already-billed summary/embed on a repeat-billing loop). A
// bare base name (no "@") leaves Branch empty — base/default-branch writes are
// unchanged.
//
// Load-bearing perf criterion: this function MUST issue exactly 1 RPC per
// call. The integration test asserts the wire-client call counter equals
// the number of write groups, NOT the number of items.
func writeBatchUpdates(ctx context.Context, c WireClient, gt kgtypes.GraphType, graphName string, items []updateBatchItem) error {
	if len(items) == 0 {
		return nil
	}
	// Split the overlay-qualified graphName ("repo@branch") into its base and
	// branch. strings.Cut returns the whole string + "" branch when there is no
	// "@" (the base/default-branch case). This is the canonical established split
	// (mirrors composite_db_lifecycle.go / engine_pipeline_scan.go).
	base, branch, _ := strings.Cut(graphName, "@")
	args := updateBatchArgs{Operation: "update_batch", Graph: string(gt), Branch: branch, Items: items}
	graphsel.ApplyInstanceKey(gt, base, &args.Repo, &args.Account, &args.Name, &args.Language, true)
	// Compile the update_batch to a MUTATION_KIND_UPDATE_ITEMS MutationPlan and
	// run it through the Execute seam (the same engine.Compile+Execute shape
	// wire_persist.executeMutate uses) — NOT the legacy gc.Call(mutate) path.
	// This is the change that makes the legacy handleMutateUpdateBatch deletable
	// Still EXACTLY ONE Execute RPC per write group: N heterogeneous
	// items ride one plan in one Execute (no N+1).
	rawArgs, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("pipeline.rpc: marshal update_batch args: %w", err)
	}
	req, ok := engine.Compile("mutate", rawArgs)
	if !ok {
		// compileMutateUpdateBatch must reduce a non-empty update_batch; a false
		// here is a wiring bug, not a routing decision.
		return fmt.Errorf("pipeline.rpc: update_batch did not compile to a MutationPlan (wiring bug)")
	}
	return executeBatchWithRetry(ctx, c, req, len(items))
}

// Writeback retry bounds: a rate-limited update_batch (a remote backend 429s
// the Execute) carries summaries/vectors already computed by an LLM call, so
// discarding it wastes that spend AND forces a re-summarize next scan. Instead
// we retry within the rate-limit window — honoring Retry-After when present —
// up to maxWritebackRetries before giving up (after which the node is
// re-discovered next scan, unchanged from the old behavior, but only after a
// genuine exhaustion rather than on the first 429).
const (
	maxWritebackRetries  = 5
	writebackBackoffBase = 500 * time.Millisecond
	writebackBackoffMax  = 30 * time.Second
)

// executeBatchWithRetry runs the update_batch Execute, retrying ONLY on a
// rate-limit error (honoring Retry-After) so a remote 429 never tosses
// already-computed work. Non-rate-limit errors return immediately — the happy
// path is still exactly ONE Execute RPC, preserving the load-bearing perf
// criterion (the integration test never injects a 429, so it sees 1 RPC/group).
func executeBatchWithRetry(ctx context.Context, c WireClient, req *knowledgev1.ExecuteRequest, itemCount int) error {
	var wait time.Duration
	for attempt := 0; ; attempt++ {
		if wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		_, err := c.Execute(ctx, req)
		if err == nil {
			return nil
		}
		hint, isRateLimit := rateLimitHint(err)
		if !isRateLimit || attempt >= maxWritebackRetries {
			if isRateLimit {
				slog.Warn("pipeline.rpc: update_batch exhausted rate-limit retries; computed work not written (node re-discovered next scan)",
					"items", itemCount, "attempts", attempt+1, "error", err)
			} else {
				// Backend-tagged items reject the whole batch; log the count so
				// the operator sees it without re-reading every item.
				slog.Warn("pipeline.rpc: mutate(update_batch) failed", "items", itemCount, "error", err)
			}
			return err
		}
		wait = hint
		if wait <= 0 {
			wait = min(writebackBackoffBase<<attempt, writebackBackoffMax)
		}
		slog.Warn("pipeline.rpc: update_batch rate-limited; retrying within window",
			"items", itemCount, "attempt", attempt+1, "wait", wait, "retry_after_hint", hint, "error", err)
	}
}
