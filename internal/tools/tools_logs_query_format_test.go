// SPDX-License-Identifier: Apache-2.0

// Package tools — tests for log graph alias rendering in formatLogsDrillDown.
//
// These exercise the alias presentation contract: every stream and
// template line in drill-down output must carry both a readable alias
// and an 8-character short-hash suffix so operators can locate the
// object by either form.
package tools

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogsDrillDown_RendersStreamAliasAndShortHash verifies the
// drill-down output includes both an alias-style identifier and a
// short hash for each rendered stream.
func TestLogsDrillDown_RendersStreamAliasAndShortHash(t *testing.T) {
	queryID := "q-drill-aliases-streams"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Text: "service=api",
	})
	require.False(t, result.IsError, "drill-down: %s", resultText(result))
	text := resultText(result)

	streamLines := extractListLines(text, "Streams")
	require.NotEmpty(t, streamLines, "expected at least one stream line")

	// Every stream line must match `- <alias> (<8hex>) ...` — alias is
	// inside backticks, hash is inside parens.
	rx := regexp.MustCompile("^- `([^`]+)` \\(([0-9a-f]{8})\\) ")
	for _, line := range streamLines {
		m := rx.FindStringSubmatch(line)
		require.NotNil(t, m, "stream line missing alias+hash: %q", line)
		alias, hash := m[1], m[2]
		assert.NotEmpty(t, alias, "alias must be non-empty in %q", line)
		assert.Len(t, hash, 8, "short hash must be 8 hex chars in %q", line)
	}
}

// TestLogsDrillDown_RendersTemplateAliasAndShortHash verifies the same
// rendering contract for templates: alias + 8-char hash for every line.
func TestLogsDrillDown_RendersTemplateAliasAndShortHash(t *testing.T) {
	queryID := "q-drill-aliases-templates"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Text: "service=api",
	})
	require.False(t, result.IsError, "drill-down: %s", resultText(result))
	text := resultText(result)

	tplLines := extractListLines(text, "Templates")
	require.NotEmpty(t, tplLines, "expected at least one template line")

	// Template lines render as `- <alias> (<hash>) [<sev>] <pattern>`.
	rx := regexp.MustCompile("^- `([^`]+)` \\(([0-9a-f]{8})\\) \\[")
	for _, line := range tplLines {
		m := rx.FindStringSubmatch(line)
		require.NotNil(t, m, "template line missing alias+hash: %q", line)
		alias, hash := m[1], m[2]
		assert.NotEmpty(t, alias, "alias must be non-empty in %q", line)
		assert.Len(t, hash, 8, "short hash must be 8 hex chars in %q", line)
	}
}

// extractListLines pulls the bullet-list lines from a markdown section
// titled `### <heading>` (e.g. "Streams" or "Templates"). Stops at the
// next blank line followed by another `###` header so successive
// sections don't bleed into each other.
func extractListLines(text, heading string) []string {
	lines := strings.Split(text, "\n")
	var out []string
	inSection := false
	for _, line := range lines {
		if strings.HasPrefix(line, "### "+heading) {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if strings.HasPrefix(line, "### ") {
			// Hit the next heading.
			break
		}
		if strings.HasPrefix(line, "- ") {
			out = append(out, line)
		}
	}
	return out
}
