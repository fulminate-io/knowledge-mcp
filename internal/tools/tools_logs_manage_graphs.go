// SPDX-License-Identifier: Apache-2.0

// Package tools — client-side list_logs + discard_logs handlers.
//
// BCN11.3: handlers route through the GraphCaller. The server returns
// the raw GraphInfo list (list_logs) or the discarded-names list
// (discard_logs); this client adds engine-live state from the local
// logs.LookupEngine registry and renders the markdown / JSON table.

package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// logGraphSummary is the per-graph view emitted by list_logs. It
// combines the server-supplied GraphInfo (file path, size, node/edge
// counts) with the client-side QueryEngine snapshot (template/stream
// counts) so callers can see what is still actively queryable vs. what
// is just persisted.
type logGraphSummary struct {
	QueryID      string    `json:"query_id"`
	FilePath     string    `json:"file_path,omitempty"`
	FileSize     int64     `json:"file_size"`
	Nodes        int       `json:"nodes"`
	Edges        int       `json:"edges"`
	Loaded       bool      `json:"loaded"`
	EngineLive   bool      `json:"engine_live"`
	StreamCount  int       `json:"stream_count"`
	TemplateCnt  int       `json:"template_count"`
	ModifiedTime time.Time `json:"modified_time,omitzero"`
}

// handleListLogs enumerates the loaded log graphs via the generic Execute seam
// (RETURN_MODE_GRAPH_NAMES over GraphLogs) and augments each row with engine-live
// state from the client-side logs registry. The server returns the raw GraphInfo
// catalog; the client overlays the augmentation + renders the table.
func (h *Handler) handleListLogs(ctx context.Context, format string) kgtools.ToolResult {
	gc := h.graphCaller()
	if gc == nil {
		return kgtools.ErrorResult("list_logs: no GraphCaller configured")
	}
	// RETURN_MODE_GRAPH_NAMES over GraphLogs via the generic Execute seam — the
	// fetchGraphNamesOfType helper returns the full []knowledgev1.GraphInfo
	// buildLogGraphSummary needs (FilePath/FileSize/Nodes/Edges/Loaded), not just
	// names. Replaces the bespoke manage(list_logs) gc.Call.
	infos, err := fetchGraphNamesOfType(ctx, gc, string(kgtypes.GraphLogs))
	if err != nil {
		return kgtools.ErrorResult("list_logs: " + err.Error())
	}
	summaries := make([]logGraphSummary, 0, len(infos))
	for _, gi := range infos {
		summaries = append(summaries, buildLogGraphSummary(gi))
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].QueryID < summaries[j].QueryID
	})
	if format == "json" {
		return jsonResult(summaries)
	}
	return kgtools.TextResult(formatLogGraphsTable(summaries))
}

// buildLogGraphSummary combines GraphInfo + any registered engine into
// the per-row response struct. File modification time comes from
// FilePath (or zero when the graph is in-memory-only). Takes a pointer to
// the proto GraphInfo — the proto carries a noCopy MessageState, so a
// by-value param would copylocks.
func buildLogGraphSummary(gi *knowledgev1.GraphInfo) logGraphSummary {
	s := logGraphSummary{
		QueryID:  gi.GetName(),
		FilePath: gi.GetFilePath(),
		FileSize: gi.GetFileSize(),
		Nodes:    int(gi.GetNodes()),
		Edges:    int(gi.GetEdges()),
		Loaded:   gi.GetLoaded(),
	}
	if engine, ok := logs.LookupEngine(gi.GetName()); ok && engine != nil {
		s.EngineLive = true
		s.StreamCount = engine.StreamCount()
		s.TemplateCnt = engine.TemplateCount()
	}
	s.ModifiedTime = fileModTime(gi.GetFilePath())
	return s
}

// fileModTime returns the on-disk modification time for path. A missing
// file or an I/O error returns the zero Time — list callers treat that
// as "not yet persisted", which matches the in-memory-only case.
//
// NOTE: this stat happens client-side, so it works only when the file
// is locally accessible (single-host dev). Remote-server mode degrades
// to zero-time (no annotation) — file-mod info is local-only by
// definition. The server-supplied FilePath in GraphInfo still surfaces
// in the JSON projection.
func fileModTime(path string) time.Time {
	if path == "" {
		return time.Time{}
	}
	info, err := os.Stat(path)
	if err != nil || info == nil {
		return time.Time{}
	}
	return info.ModTime()
}

// formatLogGraphsTable renders list_logs as a markdown table. The empty
// case returns a message rather than an empty table so callers can tell
// "no log graphs" from "formatting failed".
func formatLogGraphsTable(summaries []logGraphSummary) string {
	if len(summaries) == 0 {
		return "No active log graphs. Run a log query (e.g. log_collect) to populate ~/.knowledge/logs/."
	}
	var sb strings.Builder
	sb.WriteString("| query_id | nodes | edges | streams | templates | size | modified | engine |\n")
	sb.WriteString("|----------|-------|-------|---------|-----------|------|----------|--------|\n")
	for _, s := range summaries {
		fmt.Fprintf(&sb, "| %s | %d | %d | %d | %d | %s | %s | %s |\n",
			s.QueryID, s.Nodes, s.Edges, s.StreamCount, s.TemplateCnt,
			formatBytes(s.FileSize), formatModTime(s.ModifiedTime),
			engineStatus(s.EngineLive),
		)
	}
	return sb.String()
}

// formatModTime renders the modification time in a table-friendly form.
// The zero time (in-memory-only graphs) is rendered as "-".
func formatModTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

// engineStatus renders the live-engine column in the list_logs table.
func engineStatus(live bool) string {
	if live {
		return "live"
	}
	return "persisted"
}

// handleDiscardLogs tears down the named log graph (or every loaded one when no
// name is given) via the generic DROP_GRAPH Execute seam (N1). The server-side
// store.DeleteGraph deletes the persisted graph; the client unregisters any
// matching in-process QueryEngine cached locally so concurrent readers can't
// latch onto a freshly-deleted graph.
func (h *Handler) handleDiscardLogs(ctx context.Context, name string) kgtools.ToolResult {
	gc := h.graphCaller()
	if gc == nil {
		return kgtools.ErrorResult("discard_logs: no GraphCaller configured")
	}

	// Resolve the target name set: a single named graph, or — when no name is
	// given — every loaded log graph, enumerated via the RETURN_MODE_GRAPH_NAMES
	// helper. This replaces the server's manage(discard_logs) all-graphs fan-out
	// with a client-side list read + one DROP_GRAPH Execute per name.
	var names []string
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		names = []string{trimmed}
	} else {
		infos, err := fetchGraphNamesOfType(ctx, gc, string(kgtypes.GraphLogs))
		if err != nil {
			return kgtools.ErrorResult("discard_logs: " + err.Error())
		}
		for _, gi := range infos {
			if gi.Name != "" {
				names = append(names, gi.Name)
			}
		}
	}

	var discarded, errs []string
	for _, n := range names {
		if err := dropLogGraph(ctx, gc, n); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", n, err.Error()))
			continue
		}
		discarded = append(discarded, n)
	}

	// Local engine cleanup — the DROP_GRAPH is durable on the server, but the
	// client-side logs registry is process-local, so any cached engine here would
	// outlive the persisted graph without this unregister.
	for _, n := range discarded {
		logs.UnregisterEngine(n)
	}
	return kgtools.TextResult(formatDiscardSummary(discarded, errs))
}

// dropLogGraph issues one MUTATION_KIND_DROP_GRAPH Execute whose envelope target
// names the (logs, qid) graph — the selector-driven whole-graph teardown (N1).
// The server resolves the target to (GraphLogs, qid) and calls store.DeleteGraph
// (unload + os.Remove(binPath)). Mirrors intercept_mutate_link.go's explicit-Target
// MutationPlan dispatch.
func dropLogGraph(ctx context.Context, gc GraphCaller, qid string) error {
	ex, err := persistExecutor(gc)
	if err != nil {
		return err
	}
	_, err = ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Mutation{
			Mutation: &knowledgev1.MutationPlan{
				Kind: knowledgev1.MutationPlan_MUTATION_KIND_DROP_GRAPH,
			},
		},
		Target: &knowledgev1.GraphSelector{Graph: "logs", Name: qid},
	})
	return err
}

// formatDiscardSummary renders the discard report. Single-name and
// all-graph forms share one renderer — the only difference is what the
// server returned in the Discarded slice.
func formatDiscardSummary(discarded, errs []string) string {
	if len(discarded) == 0 && len(errs) == 0 {
		return "No log graphs to discard."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Discarded %d log graph(s).\n", len(discarded))
	for _, n := range discarded {
		fmt.Fprintf(&sb, "  - %s\n", n)
	}
	if len(errs) > 0 {
		sb.WriteString("\nErrors:\n")
		for _, e := range errs {
			fmt.Fprintf(&sb, "  - %s\n", e)
		}
	}
	return sb.String()
}
