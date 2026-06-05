// SPDX-License-Identifier: Apache-2.0

// intercept_manage_promote.go — client-side composer for
// manage(operation:promote_metadata). The client OWNS the whole operation
// it reads the per-graph metadata stats via the generic
// query(metadata_stats) carrier, computes the per-key promote/demote decision
// with the SAME pure engine.RecommendAction the server executor uses, dispatches
// one MIGRATE_META_REPR Execute per non-noop key (N2), builds the report, emits
// the batch-level narrative think(), and renders the text-format output. No
// server-side promote_metadata handler is involved — the decision + the policy
// (parseMetadataGraphTypeForBackfill graph-type rejections) are client concerns.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// ticketIDPromoteMetadata is the T5 ticket node ID the batch-level
// narrative think() links to (locked decision 3: batch-level,
// ticket-linked, non-dry-run only). Stable across plan revisions
// because changesets carry the linkage forward.
const ticketIDPromoteMetadata = "31811c79a6c14eb19eede0bc595a1686"

// metadataStatsCaller is the narrow MetadataStats RPC seam the promote_metadata
// composer type-asserts the GraphCaller to (the production graphClientCaller
// implements it, mirroring the render.Executor / tools.Indexer narrow seams). It
// reads the per-graph stats + override-config carriers without widening the
// Call-only GraphCaller interface.
type metadataStatsCaller interface {
	MetadataStats(ctx context.Context, req *knowledgev1.MetadataStatsRequest) (*knowledgev1.MetadataStatsResponse, error)
}

// promoteMetadataReport mirrors the server-side struct shape. It is a
// client-local copy — never imported from the server package.
type promoteMetadataReport struct {
	Graph         string
	Name          string
	DryRun        bool
	Force         bool
	StatsKeyCount int
	Actions       map[string][]string
	Counters      promoteMetadataCounters
	Duration      time.Duration
	Errors        []string
}

// promoteMetadataCounters mirrors the server-side counter struct.
type promoteMetadataCounters struct {
	ValueNodesCreated int
	EdgesEmitted      int
	MapEntriesCleared int
	ScalarsRestored   int
	EdgesRemoved      int
	ValueNodesDeleted int
	ChangesetBundles  int
}

// handleManagePromoteMetadata is the client-side composer for
// manage(operation:promote_metadata). It reads the per-graph metadata stats,
// computes the per-key promote/demote decision with the pure engine.RecommendAction
// (the SAME function the server executor's Apply() uses), dispatches one
// MIGRATE_META_REPR Execute per non-noop key (N2), builds the report client-side,
// fires the narrative think(), and renders text-format output. Mirrors the
// server's indexPromoteMetadata loop (engine_index.go) but with the decision +
// graph-type policy held client-side.
func handleManagePromoteMetadata(ctx context.Context, deps ClientDeps, a manageArgs, rawParams json.RawMessage) kgtools.ToolResult {
	// (1) Graph-type policy, CLIENT-SIDE. cloud|cicd|practice|logs only —
	// code/knowledge/linkage/empty rejected with the operator-facing message,
	// BEFORE any stats read or dispatch touches the server.
	gt, perr := parseMetadataGraphTypeForBackfill(a.Graph)
	if perr != nil {
		return errorResult(perr.Error())
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return errorResult("promote_metadata: name=<graph identifier> is required")
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("manage(promote_metadata): GraphCaller is unavailable — the client is running in degraded mode")
	}
	statsCaller, ok := gc.(metadataStatsCaller)
	if !ok {
		return errorResult("manage(promote_metadata): graph client does not expose the MetadataStats seam")
	}

	// (2) Read the per-graph metadata stats + operator override config via the
	// generic MetadataStats RPC (BOTH carriers — the override config drives the
	// FORCE_* precedence in RecommendAction).
	target := metadataBackfillTarget(a.Graph, name)
	statsResp, serr := statsCaller.MetadataStats(ctx, &knowledgev1.MetadataStatsRequest{Target: target})
	if serr != nil {
		return errorResult(fmt.Sprintf("promote_metadata: load stats failed: %v", serr))
	}
	// The typed MetadataStats / OverrideConfig ride the response carriers directly
	// (nil-safe getters) — no client-side decode step. A nil MetadataStats yields an
	// empty key set; a nil OverrideConfig means no FORCE_* pins.
	stats := statsResp.GetMetadataStats()
	override := statsResp.GetOverrideConfig()

	// (3) Key set: caller-supplied params["keys"] (CSV) or every observed key.
	keys := promoteMetadataKeySet(rawParams, stats)

	// (4-5) Per-key decision + dispatch. Build the report client-side keyed by the
	// Recommendation string (matching the server's per-reason buckets).
	report := promoteMetadataReport{
		Graph: string(gt), Name: name, DryRun: a.DryRun, Force: a.Force,
		StatsKeyCount: len(keys), Actions: map[string][]string{},
	}
	t0 := time.Now()
	for _, key := range keys {
		rec := engine.RecommendAction(stats.GetKeys()[key], override, key)
		report.Actions[string(rec)] = append(report.Actions[string(rec)], key)
		toEdge, dispatch := migrateDirectionForRecommendation(rec)
		if !dispatch || a.DryRun {
			continue
		}
		if err := dispatchMigrateMetaRepr(ctx, gc, target, key, toEdge); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %s", key, err.Error()))
		}
	}
	report.Duration = time.Since(t0)

	// Narrative emission: only on non-dry-run with keys processed.
	if !report.DryRun && report.StatsKeyCount > 0 {
		emitPromoteMetadataNarrative(ctx, gc, &report)
	}

	if a.Format == "json" {
		return jsonResult(promoteMetadataReportJSON(&report))
	}
	return textResult(renderPromoteMetadataReportText(&report))
}

// promoteMetadataReportJSON renders the report into the snake_case wire shape the
// server-side promoteMetadataReportJSON helper produced, so the format=json
// output is unchanged across the client-side composition. Counters are left at
// their (zero) report values — N2 dispatches return only NodesProcessed, not the
// per-flip value-node/edge/map breakdown, so the granular counters are not
// recoverable client-side; the buckets + key set + duration carry the report.
func promoteMetadataReportJSON(r *promoteMetadataReport) map[string]any {
	return map[string]any{
		"graph":           r.Graph,
		"name":            r.Name,
		"dry_run":         r.DryRun,
		"force":           r.Force,
		"stats_key_count": r.StatsKeyCount,
		"actions":         r.Actions,
		"counters": map[string]any{
			"value_nodes_created": r.Counters.ValueNodesCreated,
			"edges_emitted":       r.Counters.EdgesEmitted,
			"map_entries_cleared": r.Counters.MapEntriesCleared,
			"scalars_restored":    r.Counters.ScalarsRestored,
			"edges_removed":       r.Counters.EdgesRemoved,
			"value_nodes_deleted": r.Counters.ValueNodesDeleted,
			"changeset_bundles":   r.Counters.ChangesetBundles,
		},
		"duration_ms": r.Duration.Milliseconds(),
		"errors":      r.Errors,
	}
}

// migrateDirectionForRecommendation maps an engine.Recommendation to the
// MIGRATE_META_REPR direction: PROMOTE / FORCE_EDGE → edge (true); DEMOTE /
// FORCE_SCALAR → scalar (false); KEEP (scalar) / KEEP (edge) → no dispatch.
// The FORCE_* directions ARE dispatched (the operator pinned the key to a
// representation the live data may not yet match — a flip reconciles it),
// mirroring the server executor's force-aware ApplyDecision.
func migrateDirectionForRecommendation(rec engine.Recommendation) (toEdge, dispatch bool) {
	switch rec {
	case engine.RecommendPromote, engine.RecommendForceEdge:
		return true, true
	case engine.RecommendDemote, engine.RecommendForceScalar:
		return false, true
	default: // RecommendKeepScalar / RecommendKeepEdge
		return false, false
	}
}

// dispatchMigrateMetaRepr issues one MIGRATE_META_REPR Execute (N2) for the key
// in the given direction, over the per-graph Target. The plan is built directly
// (NOT via engine.Compile — the client compiler has no migrate_meta_repr arm),
// mirroring dropLogGraph's explicit-plan dispatch.
func dispatchMigrateMetaRepr(ctx context.Context, gc GraphCaller, target *knowledgev1.GraphSelector, key string, toEdge bool) error {
	ex, err := persistExecutor(gc)
	if err != nil {
		return err
	}
	repr := knowledgev1.MigrateMetaReprSpec_TARGET_REPR_SCALAR
	if toEdge {
		repr = knowledgev1.MigrateMetaReprSpec_TARGET_REPR_EDGE
	}
	_, err = ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Target: target,
		Plan: &knowledgev1.ExecuteRequest_Mutation{
			Mutation: &knowledgev1.MutationPlan{
				Kind: knowledgev1.MutationPlan_MUTATION_KIND_MIGRATE_META_REPR,
				MigrateMetaRepr: &knowledgev1.MigrateMetaReprSpec{
					MetadataKey: key,
					TargetRepr:  repr,
				},
			},
		},
	})
	return err
}

// promoteMetadataKeySet resolves the key set to process: the caller-supplied
// params["keys"] (comma-split, trimmed, non-empty) when present, else every key
// the stats snapshot observed. Mirrors the server indexPromoteMetadata key
// resolution (splitNormalizeCSV(params["keys"]) || stats.SnapshotKeys()).
func promoteMetadataKeySet(rawParams json.RawMessage, stats *knowledgev1.MetadataStats) []string {
	var p struct {
		Keys string `json:"keys"`
	}
	_ = json.Unmarshal(rawParams, &p)
	if keys := splitTrimCSV(p.Keys); len(keys) > 0 {
		return keys
	}
	all := stats.GetKeys()
	if len(all) == 0 {
		return nil
	}
	out := make([]string, 0, len(all))
	for k := range all {
		out = append(out, k)
	}
	return out
}

// splitTrimCSV splits a comma-separated value, trimming whitespace and dropping
// empty entries (operators commonly send trailing commas through CLI glue).
func splitTrimCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// metadataBackfillTarget builds the GraphSelector for a promote_metadata target,
// placing name in the per-type instance-key field resolveTargetDB reads:
// practice → Language, cloud/cicd → Account, logs (and the rest) → Name.
func metadataBackfillTarget(graph, name string) *knowledgev1.GraphSelector {
	switch graph {
	case "practice":
		return &knowledgev1.GraphSelector{Graph: graph, Language: name}
	case "cloud", "cicd":
		return &knowledgev1.GraphSelector{Graph: graph, Account: name}
	default:
		return &knowledgev1.GraphSelector{Graph: graph, Name: name}
	}
}

// parseMetadataGraphTypeForBackfill ports the server graph-type policy
// (tools_manage_promote_metadata.go) CLIENT-SIDE: promote_metadata accepts
// cloud|cicd|practice|logs only. code (T6 path), knowledge (out of scope), and
// linkage (no promotable proxy metadata) are rejected — as is an empty graph, so
// an operator must always supply an explicit one. The rejection is the
// operator-facing message returned without touching the server.
func parseMetadataGraphTypeForBackfill(s string) (kgtypes.GraphType, error) {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "":
		return "", errors.New(
			"promote_metadata: empty graph parameter — expected one of cloud|cicd|practice|logs (knowledge/code/linkage are not supported here)")
	case "code":
		return "", errors.New(
			"promote_metadata: code graphs use the T6 path; not supported here")
	case "knowledge":
		return "", errors.New(
			"promote_metadata: knowledge graph is out of scope (per-node structured fields, low metadata reuse); not supported here")
	case "linkage":
		return "", errors.New(
			"promote_metadata: linkage graph carries no promotable metadata on its cross-graph proxies; not supported here")
	case "cloud":
		return kgtypes.GraphCloud, nil
	case "cicd":
		return kgtypes.GraphCICD, nil
	case "practice":
		return kgtypes.GraphPractice, nil
	case "logs":
		return kgtypes.GraphLogs, nil
	default:
		return "", fmt.Errorf(
			"promote_metadata: unsupported graph %q — expected one of cloud|cicd|practice|logs", s)
	}
}

// emitPromoteMetadataNarrative records the batch-level think() summarizing
// the backfill outcome via the reusable composeThoughtCreate composition —
// equivalent to the legacy mutate(create,type:thought,session:"T5-backfill",
// links:[ticket]) it replaces: it creates the thought, its NodeThoughtSession
// membership (EdgeKGContains + EdgeNext), and the EdgeRelatesTo link to the
// ticket. Failure is log-not-return — the backfill itself succeeded, narrative
// telemetry is observability not correctness.
func emitPromoteMetadataNarrative(ctx context.Context, gc GraphCaller, report *promoteMetadataReport) {
	if _, err := composeThoughtCreate(ctx, gc, composeThoughtArgs{
		Content: promoteMetadataThoughtContent(report),
		// Summary is REQUIRED on every composeThoughtCreate path (the thought is
		// carved out of server summary-validation, but the auto-created session is
		// not, and we set the author summary on the thought too). This internal
		// caller mints a deliberate search-optimized one-liner from the report.
		Summary: fmt.Sprintf("Metadata backfill narrative: %s/%s refreshed %d keys", report.Graph, report.Name, report.StatsKeyCount),
		Session: "T5-backfill",
		Links:   []string{ticketIDPromoteMetadata},
	}); err != nil {
		slog.Warn("promote_metadata: failed to record narrative think", "error", err)
	}
}

// promoteMetadataThoughtContent renders the one-line narrative the
// batch-level think() emits. Mirrors the rendered report's per-bucket
// counts so a reader scanning recall results sees the same story the
// operator saw at the CLI.
func promoteMetadataThoughtContent(report *promoteMetadataReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Backfill on %s/%s: refreshed %d keys",
		report.Graph, report.Name, report.StatsKeyCount)

	promoted := keysByReason(report, "PROMOTE")
	demoted := keysByReason(report, "DEMOTE")
	keptCount := report.StatsKeyCount - len(promoted) - len(demoted)

	if len(promoted) > 0 {
		fmt.Fprintf(&sb, ", promoted %d (%s)", len(promoted), strings.Join(promoted, ", "))
	}
	if len(demoted) > 0 {
		fmt.Fprintf(&sb, ", demoted %d (%s)", len(demoted), strings.Join(demoted, ", "))
	}
	if keptCount > 0 {
		fmt.Fprintf(&sb, ", kept %d", keptCount)
	}
	fmt.Fprintf(&sb, ", took %s", report.Duration.Round(time.Millisecond))
	if len(report.Errors) > 0 {
		fmt.Fprintf(&sb, ". %d errors.", len(report.Errors))
	} else {
		sb.WriteString(". No errors.")
	}
	return sb.String()
}

// keysByReason returns the sorted list of keys whose Reason bucket
// matches the supplied name.
func keysByReason(report *promoteMetadataReport, reason string) []string {
	keys := append([]string(nil), report.Actions[reason]...)
	sort.Strings(keys)
	return keys
}

// renderPromoteMetadataReportText returns the operator-facing markdown
// report for the promote_metadata call. Mirrors the example shape from
// the original server-side renderer: header, decisions block, executed
// block, duration. Sorted bucket lists guarantee byte-identical output.
func renderPromoteMetadataReportText(report *promoteMetadataReport) string {
	var sb strings.Builder

	header := fmt.Sprintf("Promotion pass on %s/%s (dry_run=%t",
		report.Graph, report.Name, report.DryRun)
	if report.Force {
		header += ", force=true"
	}
	header += "):"
	sb.WriteString(header)
	sb.WriteByte('\n')
	fmt.Fprintf(&sb, "  Stats refreshed: %d keys\n", report.StatsKeyCount)

	sb.WriteString("  Decisions:\n")
	renderActionBuckets(&sb, report.Actions)

	if !report.DryRun {
		sb.WriteString("  Executed:\n")
		renderExecutedCounters(&sb, report.Counters)
	}

	fmt.Fprintf(&sb, "  Duration: %s\n", report.Duration.Round(time.Millisecond))

	if len(report.Errors) > 0 {
		sb.WriteString("  Errors:\n")
		for _, e := range report.Errors {
			fmt.Fprintf(&sb, "    - %s\n", e)
		}
	}
	return sb.String()
}

// renderActionBuckets writes one line per non-empty bucket in the
// "REASON: key1, key2, ... (N keys)" shape.
func renderActionBuckets(sb *strings.Builder, actions map[string][]string) {
	if len(actions) == 0 {
		sb.WriteString("    (no keys observed)\n")
		return
	}
	bucketNames := make([]string, 0, len(actions))
	for name := range actions {
		bucketNames = append(bucketNames, name)
	}
	sort.Strings(bucketNames)
	for _, name := range bucketNames {
		keys := append([]string(nil), actions[name]...)
		sort.Strings(keys)
		fmt.Fprintf(sb, "    %s: %s (%d %s)\n",
			name, strings.Join(keys, ", "), len(keys), pluralKeys(len(keys)))
	}
}

// renderExecutedCounters writes the aggregate "what happened" block.
// Counters surface only when non-zero per the locked rendering decision.
func renderExecutedCounters(sb *strings.Builder, c promoteMetadataCounters) {
	if c.ValueNodesCreated > 0 {
		fmt.Fprintf(sb, "    Value nodes created: %d\n", c.ValueNodesCreated)
	}
	if c.EdgesEmitted > 0 {
		fmt.Fprintf(sb, "    Edges emitted: %d\n", c.EdgesEmitted)
	}
	if c.MapEntriesCleared > 0 {
		fmt.Fprintf(sb, "    Map entries cleared: %d\n", c.MapEntriesCleared)
	}
	if c.ScalarsRestored > 0 {
		fmt.Fprintf(sb, "    Scalars restored: %d\n", c.ScalarsRestored)
	}
	if c.EdgesRemoved > 0 {
		fmt.Fprintf(sb, "    Edges removed: %d\n", c.EdgesRemoved)
	}
	if c.ValueNodesDeleted > 0 {
		fmt.Fprintf(sb, "    Value nodes deleted: %d\n", c.ValueNodesDeleted)
	}
	if c.ChangesetBundles > 0 {
		fmt.Fprintf(sb, "    Changeset bundles written: %d\n", c.ChangesetBundles)
	}
	if c.ValueNodesCreated == 0 && c.EdgesEmitted == 0 && c.MapEntriesCleared == 0 &&
		c.ScalarsRestored == 0 && c.EdgesRemoved == 0 && c.ValueNodesDeleted == 0 {
		sb.WriteString("    (no writes — all keys NoOp)\n")
	}
}

// pluralKeys returns "key" or "keys" based on count.
func pluralKeys(n int) string {
	if n == 1 {
		return "key"
	}
	return "keys"
}
