// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"strings"

	workers "github.com/fulminate-io/knowledge-mcp/internal/workers"
)

// workersAsJSON returns a compact marshaling-friendly view of the
// Worker list for worker:list with format=json. Mirrors the field set
// callers see in the text-table view.
func workersAsJSON(workers []workers.Worker) []map[string]any {
	out := make([]map[string]any, 0, len(workers))
	for _, w := range workers {
		out = append(out, map[string]any{
			"name":                  w.Name,
			"description":           w.Description,
			"system_prompt":         w.SystemPrompt,
			"provider":              string(w.Provider),
			"model":                 w.Model,
			"tool_allowlist":        w.ToolAllowlist,
			"triggers":              w.Triggers,
			"max_iterations":        w.MaxIterations,
			"max_wallclock_seconds": w.MaxWallclockSeconds,
			"enabled":               w.Enabled,
		})
	}
	return out
}

// formatWorkersTable renders the list as a markdown table. Same empty-
// case messaging shape as log_backend:list so the operator-facing tone
// is consistent across config-record tools.
func formatWorkersTable(workers []workers.Worker) string {
	if len(workers) == 0 {
		return "No workers registered. Use worker(operation: \"create\", ...) to add one."
	}
	var sb strings.Builder
	sb.WriteString("| name | provider | model | enabled | tools | triggers |\n")
	sb.WriteString("|------|----------|-------|---------|-------|----------|\n")
	for _, w := range workers {
		fmt.Fprintf(&sb, "| %s | %s | %s | %v | %s | %s |\n",
			w.Name,
			emptyDash(string(w.Provider)),
			emptyDash(w.Model),
			w.Enabled,
			emptyDash(strings.Join(w.ToolAllowlist, ", ")),
			emptyDash(triggersSummary(w.Triggers)),
		)
	}
	return sb.String()
}

// triggersSummary renders a Trigger slice as a comma-separated list of
// event names so the worker:list table stays readable. Filters and
// schedules are elided here; worker:status has the full picture.
func triggersSummary(triggers []workers.Trigger) string {
	if len(triggers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(triggers))
	for _, t := range triggers {
		parts = append(parts, t.Event)
	}
	return strings.Join(parts, ", ")
}

// emptyDash returns "-" for empty strings so the markdown table cells
// stay visually balanced. Mirrors orDash() in tools_logs_manage_backend.go
// — kept package-private because the two callers live in different
// files and the shared helper would add cross-file coupling without
// pulling its weight.
func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
