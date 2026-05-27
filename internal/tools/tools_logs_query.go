// SPDX-License-Identifier: Apache-2.0

// Package tools — log graph query dispatcher.
//
// This file wires `query({ graph: "logs", name: "<queryID>" })` into the
// in-memory QueryEngine that the logs pipeline leaves behind after a
// collection run. Three shapes are supported:
//
//   - Overview: no text, no id → engine.Overview() ranked by error count.
//   - Drill-down: text → parsed label filter (AND-only) + severity range.
//   - Template detail: id → template node + decompressed example entries.
//
// The engine registry is process-local, so restarts lose it. When
// LookupEngine misses we rebuild the engine by reading back the persisted
// log graph (templates/streams/chunks) and re-indexing via NewQueryEngine.
// The rebuilt engine is stashed back into the registry so subsequent calls
// take the fast path.
package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// handleLogsQuery is the graph='logs' entry point. Dispatches between the
// supported shapes based on which of (mode, id, text) is set.
//
// BCN11.3: refactored to take a pre-fetched *logState instead of a
// store.DB. The wire-fetch orchestrator getOrFetchLogState bulk-loads
// every template/stream/chunk/label/proxy and every edge of interest in
// four RPCs before any formatter runs.
func (h *Handler) handleLogsQuery(ctx context.Context, a queryArgs) kgtools.ToolResult {
	queryID := strings.TrimSpace(a.Name)
	if queryID == "" {
		return kgtools.ErrorResult("graph='logs' requires 'name' (the query_id). " +
			"Use `query({ graph: 'logs' })` without a name via the manage tool's list_logs to see available IDs.")
	}
	engine, st, err := h.getOrFetchLogState(ctx, queryID)
	if err != nil {
		return kgtools.ErrorResult(fmt.Sprintf("logs query %q: %s", queryID, err.Error()))
	}
	// Stats mode works off the pre-fetched state directly — no engine
	// needed. Run this BEFORE the engine-nil guard so an empty log graph
	// still reports a well-formed (zero-count) stats body.
	if a.Mode == "stats" {
		return h.handleLogsStats(ctx, queryID, st, a.Samples)
	}
	if engine == nil {
		return kgtools.ErrorResult(fmt.Sprintf("logs query %q: no engine and no persisted graph", queryID))
	}
	if a.Mode == "pivot" {
		return handleLogsPivot(queryID, engine, a.Rows, a.Cols)
	}
	if a.Mode == "correlations" {
		return handleLogsCorrelations(queryID, engine, st)
	}
	if a.Mode == "timeline" {
		return handleLogsTimeline(queryID, engine, a.Extra["bucket"])
	}
	if a.Mode == "explain" {
		return handleLogsExplain(queryID, engine, st, a.ID, a.Extra)
	}
	if a.Mode == "resolver" {
		return handleLogsResolverTrace(queryID, st)
	}
	if a.ID != "" {
		return dispatchLogsByID(queryID, engine, st, a.ID)
	}
	if strings.TrimSpace(a.Text) != "" {
		return handleLogsDrillDown(queryID, engine, st, a.Text)
	}
	return handleLogsOverview(queryID, engine, st)
}

// (loadLogsFromGraph + getOrRebuildLogsEngine + the per-type query
// helpers are deleted in BCN11.3 — superseded by getOrFetchLogState in
// tools_logs_handler.go and the wire-fetch helpers in
// tools_logs_wire_fetch.go.)

// templateFromNode / streamFromNode / chunkFromNode / parseMetaTime
// live in tools_logs_query_rebuild.go to keep this file under the
// 300-line soft cap.

// parseLogFilters tokenizes a drill-down expression into (labelFilters, minSeverity).
// Grammar (AND-only MVP):
//
//	expr      = filter (WS filter)*
//	filter    = label_filter | severity_filter
//	label_filter    = key "=" value
//	severity_filter = "severity" cmp LEVEL
//	cmp       = "=" | ">=" | ">" | "<=" | "<"
//	LEVEL     = TRACE | DEBUG | INFO | WARN | ERROR | CRITICAL
//
// For the comparators ">" and ">=" the LogIndex has a single range helper
// (SeverityRange) that matches "at-or-above". Strict ">" bumps one level up;
// "<" / "<=" are rejected because the underlying index only exposes the
// at-or-above primitive. The rejection produces an error rather than silently
// degrading so callers can see the gap.
func parseLogFilters(text string) (map[string]string, string, error) {
	labels := map[string]string{}
	minSev := ""
	for tok := range strings.FieldsSeq(text) {
		if sev, matched, err := matchSeverityToken(tok); err != nil {
			return nil, "", err
		} else if matched {
			if minSev != "" && minSev != sev {
				return nil, "", fmt.Errorf("conflicting severity filters: %q and %q", minSev, sev)
			}
			minSev = sev
			continue
		}
		k, v, ok := strings.Cut(tok, "=")
		if !ok || k == "" || v == "" {
			return nil, "", fmt.Errorf("invalid filter token %q (expected key=value or severity>=LEVEL)", tok)
		}
		labels[k] = v
	}
	return labels, minSev, nil
}

// matchSeverityToken recognizes "severity<cmp><LEVEL>" tokens and returns
// the normalized minimum severity. For "=" it's the exact level; for ">=",
// the level itself; for ">", the next level up. "<" and "<=" are rejected.
// Returns (minSev, true, nil) on match, (_, false, nil) when the token is
// not a severity filter, and (_, _, err) for malformed severity filters.
func matchSeverityToken(tok string) (string, bool, error) {
	rest, ok := strings.CutPrefix(tok, "severity")
	if !ok {
		return "", false, nil
	}
	for _, cmp := range []string{">=", "<=", ">", "<", "="} {
		if level, hit := strings.CutPrefix(rest, cmp); hit {
			lvl := strings.ToUpper(strings.TrimSpace(level))
			if _, known := severityLevelIndex(lvl); !known {
				return "", true, fmt.Errorf("unknown severity level %q", lvl)
			}
			switch cmp {
			case "=", ">=":
				return lvl, true, nil
			case ">":
				return nextSeverityUp(lvl)
			default: // "<", "<="
				return "", true, fmt.Errorf("comparator %q not supported (only >= > = on severity)", cmp)
			}
		}
	}
	return "", false, fmt.Errorf("invalid severity filter %q (need severity=/>=/>LEVEL)", tok)
}

// severityLevels lists the canonical severities in ascending order. Kept
// local to the tools package so the handler can do "next level up"
// arithmetic without poking at logs internals.
var severityLevels = []string{
	logwire.SeverityTrace,
	logwire.SeverityDebug,
	logwire.SeverityInfo,
	logwire.SeverityWarn,
	logwire.SeverityError,
	logwire.SeverityCritical,
}

// severityLevelIndex returns (idx, true) if level is a canonical severity.
func severityLevelIndex(level string) (int, bool) {
	for i, s := range severityLevels {
		if s == level {
			return i, true
		}
	}
	return 0, false
}

// nextSeverityUp advances one slot in the severity table so "severity>INFO"
// becomes "min WARN". Returns an error when asked to go above CRITICAL (no
// streams can match strictly-above-critical).
func nextSeverityUp(level string) (string, bool, error) {
	idx, ok := severityLevelIndex(level)
	if !ok {
		return "", true, fmt.Errorf("unknown severity level %q", level)
	}
	if idx+1 >= len(severityLevels) {
		return "", true, fmt.Errorf("no severity above %s; use >= instead of >", level)
	}
	return severityLevels[idx+1], true, nil
}
