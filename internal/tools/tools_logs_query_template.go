// SPDX-License-Identifier: Apache-2.0

// Package tools — template detail rendering for graph='logs' queries.
//
// Split out from tools_logs_query_format.go so each file stays comfortably
// under the 300-line soft cap. The template detail path is distinct from
// the overview/drill-down paths: it always needs to crack open chunks to
// decompress example entries, which has its own error modes (missing graph
// file, corrupted content) that are kept isolated here.
package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
)

// handleLogsTemplateDetail renders the pattern, severity, counts, affected
// labels, and a few decompressed example entries for a single template.
// st may be nil when the wire-fetch failed but the engine is still
// live — the handler degrades gracefully by skipping decompressed examples.
func handleLogsTemplateDetail(
	queryID string,
	engine *logs.QueryEngine,
	st *logState,
	templateID string,
) kgtools.ToolResult {
	labels := engine.LabelsForTemplate(templateID)
	if len(labels) == 0 {
		return kgtools.ErrorResult(fmt.Sprintf(
			"template %q not found in log graph %q (or has no linked streams)",
			templateID, queryID,
		))
	}
	var tplNode *knowledgev1.Node
	if st != nil {
		if n, ok := st.NodeByID(templateID); ok {
			tplNode = n
		}
	}
	examples := collectTemplateExamples(st, templateID, 5)
	alias := engine.AliasForTemplateID(templateID)
	return kgtools.TextResult(formatTemplateDetail(queryID, templateID, alias, tplNode, labels, examples))
}

// collectTemplateExamples pulls up to max decompressed entries from the
// chunks associated with templateID. Returns nil if the pre-fetched
// state is unavailable. Each example is rendered as a timestamp plus
// space-joined variable values.
func collectTemplateExamples(
	st *logState,
	templateID string,
	max int,
) []string {
	if st == nil || max <= 0 {
		return nil
	}
	examples := make([]string, 0, max)
	for _, n := range st.Chunks {
		if kgtypes.Value(n, "template_id") != templateID {
			continue
		}
		chunk := chunkFromNode(n)
		timestamps, vars, decodeErr := logs.DecodeChunk(chunk)
		if decodeErr != nil {
			continue
		}
		for i := range timestamps {
			if len(examples) >= max {
				return examples
			}
			examples = append(examples, fmt.Sprintf("%s %s",
				timestamps[i].UTC().Format("2006-01-02T15:04:05Z"),
				strings.Join(vars[i], " "),
			))
		}
	}
	return examples
}

// formatTemplateDetail assembles the template detail response: header,
// readable alias (when available), template metadata (when the node is
// still on disk), affected labels, and example entries. The alias line
// is omitted entirely when empty so legacy graphs without computed
// aliases don't render an empty placeholder.
func formatTemplateDetail(
	queryID, templateID, alias string,
	tpl *knowledgev1.Node,
	labels map[string][]string,
	examples []string,
) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Log template — %s\n\n", queryID)
	fmt.Fprintf(&sb, "**ID:** `%s`\n", templateID)
	if alias != "" {
		fmt.Fprintf(&sb, "**Alias:** `%s`\n", alias)
	}
	writeTemplateMetaLines(&sb, tpl)
	writeAffectedLabels(&sb, labels)
	writeExampleLines(&sb, examples)
	return sb.String()
}

// writeTemplateMetaLines appends pattern/severity/count lines for the
// template node when those metadata keys are populated.
func writeTemplateMetaLines(sb *strings.Builder, tpl *knowledgev1.Node) {
	if tpl == nil {
		return
	}
	if pat := kgtypes.Value(tpl, "pattern"); pat != "" {
		fmt.Fprintf(sb, "**Pattern:** `%s`\n", pat)
	}
	if sev := kgtypes.Value(tpl, "severity"); sev != "" {
		fmt.Fprintf(sb, "**Severity:** %s\n", sev)
	}
	if cnt := kgtypes.Value(tpl, "count"); cnt != "" {
		fmt.Fprintf(sb, "**Count:** %s\n", cnt)
	}
}

// writeAffectedLabels renders the unique label values per key for all
// streams that reference the template.
func writeAffectedLabels(sb *strings.Builder, labels map[string][]string) {
	if len(labels) == 0 {
		return
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sb.WriteString("\n### Affected labels\n\n")
	for _, k := range keys {
		fmt.Fprintf(sb, "- **%s**: %s\n", k, strings.Join(labels[k], ", "))
	}
}

// writeExampleLines renders the list of decompressed example entries, or a
// placeholder line when none could be recovered.
func writeExampleLines(sb *strings.Builder, examples []string) {
	if len(examples) == 0 {
		sb.WriteString("\n(No decompressed examples available — graph file missing or chunks empty.)\n")
		return
	}
	fmt.Fprintf(sb, "\n### Examples (%d)\n\n", len(examples))
	for _, e := range examples {
		fmt.Fprintf(sb, "- `%s`\n", e)
	}
}
