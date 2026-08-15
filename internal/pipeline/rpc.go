// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

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
// (it has PipelineGenPoll, PipelineScan, and Execute); the package exposes only
// these methods to keep the test seam small. Tests inject a fake WireClient with
// a call counter + canned response map.
//
// Execute is the engine seam: writeBatchUpdates compiles its update_batch
// into a MUTATION_KIND_UPDATE_ITEMS MutationPlan (engine.Compile) and runs it
// through Execute, so the per-item batch write rides the engine arm rather than
// the legacy mutate(update_batch) handler. The two gap-discovery RPCs are NOT
// engine.Compile-reducible (they need the server's per-axis dirty-gen state), so
// each rides its own typed EngineService RPC rather than the Execute seam:
//   - PipelineGenPoll is the bulk Phase-1 poll — ONE call per poll returns the
//     per-(graph,axis) dirty-gen for every loaded graph (no gap walk). The
//     central gen-poll loop diffs each gen against its watermark.
//   - PipelineScan is the Phase-2 detail fetch — the central loop fires it only
//     for the (graph,axis) pairs whose gen advanced, to pull the gap items.
type WireClient interface {
	PipelineGenPoll(ctx context.Context, req *knowledgev1.PipelineGenPollRequest) (*knowledgev1.PipelineGenPollResponse, error)
	PipelineScan(ctx context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error)
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// BackendResolver is the OPTIONAL login-aware seam the Pipeline consults to
// (a) bind a CONCRETE backend per collector at RegisterGraph time and
// (b) detect a login-state transition so CheckLoginFlip can tear down + rebind
// every collector (Hazard B). The production p.client (the bootstrap
// routedWireClient over *graphclient.Router) implements it; the Pipeline
// type-asserts p.client to this interface and falls back to p.client +
// no-flip-detection when the assertion fails (test fakes).
//
//   - Backend(ctx) returns the concrete backend WireClient the current login
//     state selects (cloud when logged in, local otherwise). The collector
//     scans through it AND stamps it on every emitted work item.
//   - LoggedIn(ctx) reports the live login state so CheckLoginFlip can compare
//     it against the previously observed state and force a full collector
//     rebind on a flip. It stays a separate boolean because cadenceFor needs
//     it to choose the cloud vs local poll cadence.
//   - SelectedAccountID(ctx) reports the Fulminate account cloud calls are
//     routed to ("" when none is selected). It is the SECOND half of the
//     backend identity: an account switch is cloud->cloud, so collectors bound
//     to the previous account must be torn down exactly as a login flip does.
type BackendResolver interface {
	Backend(ctx context.Context) (WireClient, error)
	LoggedIn(ctx context.Context) bool
	SelectedAccountID(ctx context.Context) string
}

// scanGaps issues a PipelineScan RPC for one (gt, name, axis, limit).
// Returns (items, dirty_gen, err). Empty items + nil err is the common
// case; the caller treats nil err as "scan succeeded; act on items".
// lastSeenGen is the dirty_gen the caller received from its previous
// scan against this (gt, name, axis); when non-zero AND equal to the
// server's current gen, the server short-circuits and returns empty
// items without iterating the node map.
func scanGaps(ctx context.Context, c WireClient, gt kgtypes.GraphType, name, axis string, limit int, lastSeenGen uint64) ([]*knowledgev1.PipelineScanItem, uint64, error) {
	// Background loop with no originating tool call — it stamps its own
	// query-origin operation so its share of the load is attributable rather
	// than arriving unlabeled.
	ctx = graphclient.WithOperation(ctx, graphclient.OpPipelineGapScan)
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
// owns which types it summarizes/embeds), so it lives here.
//
// It is a FILTER over working-set members, never an enumeration of what to go
// looking for: the catalog pass reads the graphs this client has interacted with
// and drops the ones whose type this pipeline does not enrich. The distinction is
// the point — filtering members costs nothing, whereas enumerating these types
// against the backend is exactly the per-type read that put collectors behind
// graphs this machine had never touched.
var pipelineEligibleGraphTypes = []kgtypes.GraphType{
	kgtypes.GraphKnowledge,
	kgtypes.GraphCode,
	kgtypes.GraphPractice,
	kgtypes.GraphCloud,
	kgtypes.GraphCICD,
	kgtypes.GraphTransformers,
}

// pipelineDrainsType reports whether the pipeline enriches graphs of type gt.
//
// Two admitting cases. A BUILTIN type qualifies only by being listed in
// pipelineEligibleGraphTypes above, which keeps builtins the pipeline does not
// enrich (linkage, logs, and the raw ingest types) out. A NON-builtin type is a
// registered custom GraphTypeDef and qualifies unconditionally: the server's
// per-axis gap shims no-op a both-false custom type cheaply, so the client
// applies no behavior gate and lets the server honor the registered
// behavior-config.
//
// An empty type names no graph and qualifies as neither.
func pipelineDrainsType(gt kgtypes.GraphType) bool {
	if gt == "" {
		return false
	}
	if !kgtypes.IsBuiltinGraphType(string(gt)) {
		return true
	}
	return slices.Contains(pipelineEligibleGraphTypes, gt)
}

// GraphRef is one (gt, name) pair in the pipeline's wanted set, produced by the
// catalog pass's working-set read (wantedGraphs, pipeline_refresh.go) and passed
// into RegisterGraph / the diff set. Public so the refresh pass can hand these
// around without exposing a wire-shape entry type.
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
// graphName may be overlay-qualified ("repo@branch", or "default@session-x" for
// a knowledge session overlay) when the gap scan read from an overlay. We split
// it on "@" for EVERY family: the bare base routes to the instance key (Repo for
// code, Name for knowledge, and so on). For the CODE family the branch then
// threads through args.Branch → the Execute Target so the write lands on the SAME
// overlay layer the scan read from. Without the split the overlay dimension is
// lost and the write resolves the base graph, failing not_found for
// overlay-resident nodes (and discarding the already-billed summary/embed on a
// repeat-billing loop). A bare base name (no "@") leaves Branch empty —
// base/default-branch writes are unchanged.
//
// Load-bearing perf criterion: this function MUST issue exactly 1 RPC per
// call. The integration test asserts the wire-client call counter equals
// the number of write groups, NOT the number of items.
func writeBatchUpdates(ctx context.Context, c WireClient, gt kgtypes.GraphType, graphName string, items []updateBatchItem) error {
	if len(items) == 0 {
		return nil
	}
	// The summary/embed writeback is the pipeline's WRITE half and a materially
	// different load shape from its gap scans, so it carries its own term.
	ctx = graphclient.WithOperation(ctx, graphclient.OpPipelineEmbedWriteback)
	// Split the overlay-qualified graphName ("repo@branch") into its base and
	// branch. strings.Cut returns the whole string + "" branch when there is no
	// "@" (the base/default-branch case). This is the canonical established split
	// (mirrors composite_db_lifecycle.go / engine_pipeline_scan.go).
	base, branch, _ := strings.Cut(graphName, "@")
	args := updateBatchArgs{Operation: "update_batch", Graph: string(gt), Items: items}
	// Branch is threaded for the CODE family only. resolveCode is the single resolver
	// arm that reads sel.Branch (Scope(repo@branch) with a base fallback); every other
	// family discards it, and the server's per-family selector partition rejects a field
	// the family cannot honor. The Cut above stays unconditional — it is what strips the
	// "@overlay" suffix off the INSTANCE KEY for every family.
	if gt == kgtypes.GraphCode {
		args.Branch = branch
	}
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
