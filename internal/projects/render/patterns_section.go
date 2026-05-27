// SPDX-License-Identifier: Apache-2.0

package render

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// renderTicketPatterns writes the `## Patterns` section to sb based on
// the ticket's outgoing EdgeUses targets (patterns) and the two
// pattern-shaped metadata keys (no_patterns_reason,
// unresolved_pattern_ids). When none of the three signals is present,
// an explicit empty-signal placeholder is rendered so the planner
// agent sees "no pattern context" rather than a silent omission.
//
// Verbatim port of cmd/knowledge-server/tools/tools_assemble_containers.go:172.
func renderTicketPatterns(ticket *knowledgev1.Node, patterns []*knowledgev1.Node, sb *strings.Builder) {
	reason := kgtypes.Value(ticket, "no_patterns_reason")
	unresolved := kgtypes.Value(ticket, "unresolved_pattern_ids")
	if len(patterns) == 0 && reason == "" && unresolved == "" {
		fmt.Fprintf(sb, "\n## Patterns\n\n*(no pattern context — run /brainstorm on this ticket first, or update with no_patterns_reason)*\n")
		return
	}
	fmt.Fprintf(sb, "\n## Patterns\n\n")
	for _, p := range patterns {
		statusLabel := p.Status
		if statusLabel == "" {
			statusLabel = "active"
		}
		fmt.Fprintf(sb, "- [%s] %s — ID: %s\n", statusLabel, p.SymbolName, p.Id)
		if p.Summary != "" {
			fmt.Fprintf(sb, "  %s\n", truncate(p.Summary, 120))
		}
	}
	if reason != "" {
		fmt.Fprintf(sb, "**No patterns reason:** %s\n", reason)
	}
	if unresolved != "" {
		fmt.Fprintf(sb, "⚠ **Unresolved pattern IDs:** %s\n", unresolved)
	}
}

// renderLanguagePatternsSection writes the `## Language patterns`
// section to sb based on the host node's outgoing EdgeAudits targets
// and the unresolved_language_patterns metadata key. Used by ticket
// AND plan assembly so the planner/reviewer always sees the same
// shape.
//
// Empty render: when there are no audits edges AND no unresolved
// metadata, emit a placeholder line — silent omission would hide the
// empty-state signal from the planner.
//
// Verbatim port of cmd/knowledge-server/tools/tools_assemble_containers.go:206.
func renderLanguagePatternsSection(host *knowledgev1.Node, languagePatterns []*knowledgev1.Node, sb *strings.Builder) {
	unresolved := kgtypes.Value(host, "unresolved_language_patterns")
	if len(languagePatterns) == 0 && unresolved == "" {
		fmt.Fprintf(sb, "\n## Language patterns\n\n*No language patterns attached.*\n")
		return
	}
	fmt.Fprintf(sb, "\n## Language patterns\n\n")
	for _, lp := range languagePatterns {
		fmt.Fprintf(sb, "- %s — %s\n", lp.Id, lp.SymbolName)
		if dsl := kgtypes.Value(lp, "dsl_pattern"); dsl != "" {
			fmt.Fprintf(sb, "  %s\n", truncate(dsl, 80))
		}
	}
	if unresolved != "" {
		fmt.Fprintf(sb, "\n### Warnings\n\n")
		fmt.Fprintf(sb, "⚠ **Unresolved language patterns:** %s\n", unresolved)
	}
}
