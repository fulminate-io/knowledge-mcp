// SPDX-License-Identifier: Apache-2.0

package render

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// renderTicketPatterns writes the `## Patterns` section to sb based on
// the ticket's outgoing EdgeUses targets (patterns) and the two
// pattern-shaped metadata keys (no_patterns_reason,
// unresolved_pattern_ids). When none of the three signals is present,
// an explicit empty-signal placeholder is rendered so the planner
// agent sees "no pattern context" rather than a silent omission.
//
// It RETURNS the ids of the style rules it referenced, for the caller's single
// bulk-read instruction line. A style rule can be attached to a ticket through
// either pattern edge, and the instruction is emitted once per assembly rather
// than once per section, so the caller — not this function — owns it.
//
// Ported from the retired server-side assemble tools, and
// no longer verbatim: a target that is a style rule renders as a reference line
// (style_rule_reference.go) instead of as a pattern.
func renderTicketPatterns(ticket *knowledgev1.Node, patterns []*knowledgev1.Node, sb *strings.Builder) []string {
	reason := kgtypes.Value(ticket, "no_patterns_reason")
	unresolved := kgtypes.Value(ticket, "unresolved_pattern_ids")
	if len(patterns) == 0 && reason == "" && unresolved == "" {
		fmt.Fprintf(sb, "\n## Patterns\n\n*(no pattern context — run /brainstorm on this ticket first, or update with no_patterns_reason)*\n")
		return nil
	}
	fmt.Fprintf(sb, "\n## Patterns\n\n")
	var styleRuleIDs []string
	for _, p := range patterns {
		if isStyleRuleNode(p) {
			sb.WriteString(styleRuleReferenceLine(p))
			styleRuleIDs = append(styleRuleIDs, p.GetId())
			continue
		}
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
	return styleRuleIDs
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
// It RETURNS the ids of the style rules it referenced, for the caller's single
// bulk-read instruction line; see renderTicketPatterns for why the caller owns
// that line rather than this function.
//
// Ported from the retired server-side assemble tools, and
// no longer verbatim: the body render below diverges deliberately, and a target
// that is a style rule leaves this render entirely for the reference line.
func renderLanguagePatternsSection(host *knowledgev1.Node, languagePatterns []*knowledgev1.Node, sb *strings.Builder) []string {
	unresolved := kgtypes.Value(host, "unresolved_language_patterns")
	if len(languagePatterns) == 0 && unresolved == "" {
		fmt.Fprintf(sb, "\n## Language patterns\n\n*No language patterns attached.*\n")
		return nil
	}
	fmt.Fprintf(sb, "\n## Language patterns\n\n")
	var styleRuleIDs []string
	for _, lp := range languagePatterns {
		// A STYLE RULE IS A REFERENCE AND LEAVES BEFORE THE BODY RENDER BELOW.
		// Its prose, its check shape and its fixtures are fetched by id when a
		// reader wants them; style_rule_reference.go carries the reasoning.
		if isStyleRuleNode(lp) {
			sb.WriteString(styleRuleReferenceLine(lp))
			styleRuleIDs = append(styleRuleIDs, lp.GetId())
			continue
		}
		fmt.Fprintf(sb, "- %s — %s\n", lp.Id, lp.SymbolName)
		if dsl := kgtypes.Value(lp, "dsl_pattern"); dsl != "" {
			// Rendered WHOLE, in a fence, rather than truncated, for a pattern
			// that is NOT a style rule. A pattern body is executable text: cut
			// at 80 characters it cannot be read, copied or run, so a reviewer
			// reading this section could not tell a real check from a
			// decorative one. A style rule never reaches here.
			fmt.Fprintf(sb, "\n```\n%s\n```\n", dsl)
		}
	}
	if unresolved != "" {
		fmt.Fprintf(sb, "\n### Warnings\n\n")
		fmt.Fprintf(sb, "⚠ **Unresolved language patterns:** %s\n", unresolved)
	}
	return styleRuleIDs
}
