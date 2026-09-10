// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// Three one-purpose text helpers used across the tool handlers. THEY SIT IN A
// FILE NAMED FOR WHAT THEY ARE because their previous homes were named for a
// feature that no longer exists: resultText lived in the logs collector's
// dispatcher, pluralSuffix in the logs search renderer and formatBytes in the
// logs tool handler, while every one of them has callers that never had
// anything to do with logs. A helper filed under a feature is a helper the
// next deletion of that feature silently takes with it.

// resultText extracts the first text content block from a tool result. Tests
// and the cross-graph collector use it to read a refusal message back out of
// a ToolResult without re-deriving the content-block walk each time.
func resultText(r kgtools.ToolResult) string {
	for _, c := range r.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}

// pluralSuffix returns "s" when n != 1. Keeps result headers grammatical
// without sprinkling ternary expressions through the format code.
func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// formatBytes mirrors the server-side helper in tools_branch.go. Duplicated
// because cmd/knowledge-server cannot import cmd/knowledge/internal.
func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
