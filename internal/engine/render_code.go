// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_code.go ports the codegraph node formatters the Phase-5 client code
// composers (analyze / file_symbols / search / stats) consume. Direct ports of
// the server cmd/knowledge-server/internal/codegraph/format.go — the codegraph
// surface relocates client-side (the ASTs + analysis are client-only; the server
// keeps only the generic graph). DisplayName / FormatNodeFull / FormatNodeCompact
// / FormatWithRepo are byte-for-byte ports.

// CodeDisplayName returns the node display name: SymbolName → Summary → ID. Port
// of the server DisplayName.
func CodeDisplayName(n *knowledgev1.Node) string {
	if n.SymbolName != "" {
		return n.SymbolName
	}
	if n.Summary != "" {
		return n.Summary
	}
	return n.Id
}

// FormatCodeNodeFull ports the server FormatNodeFull: the "# <name> (<type>) —
// <file>:<start>-<end>" header + Summary/Signature/Content block.
func FormatCodeNodeFull(node *knowledgev1.Node) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s (%s) — %s:%d-%d\n\n", CodeDisplayName(node), node.Type, node.FilePath, node.StartLine, node.EndLine)
	if node.SymbolName == "" && node.Summary != "" {
		// Summary already used as display name.
	} else if node.Summary != "" {
		fmt.Fprintf(&sb, "**Summary:** %s\n", node.Summary)
	}
	if node.Signature != "" {
		fmt.Fprintf(&sb, "**Signature:** `%s`\n", node.Signature)
	}
	if node.Content != "" {
		fmt.Fprintf(&sb, "\n```%s\n%s\n```\n", node.Language, node.Content)
	}
	return sb.String()
}

// FormatCodeNodeCompact ports the server FormatNodeCompact: the "### <name>
// (<type>) — <file>:<start>" line + optional Summary/Signature/source.
func FormatCodeNodeCompact(n *knowledgev1.Node, includeSource bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "### %s (%s) — %s:%d\n", CodeDisplayName(n), n.Type, n.FilePath, n.StartLine)
	if n.SymbolName == "" && n.Summary != "" {
		// Summary already used as display name.
	} else if n.Summary != "" {
		fmt.Fprintf(&sb, "Summary: %s\n", n.Summary)
	}
	if n.Signature != "" {
		fmt.Fprintf(&sb, "Signature: `%s`\n", n.Signature)
	}
	if includeSource && n.Content != "" {
		fmt.Fprintf(&sb, "\n```%s\n%s\n```\n\n", n.Language, n.Content)
	} else {
		sb.WriteString("\n")
	}
	return sb.String()
}

// FormatCodeWithRepo prepends the repo label (port of FormatWithRepo).
func FormatCodeWithRepo(repo, text string) string {
	return fmt.Sprintf("[%s] %s", repo, text)
}

// RenderAnalyzeNode ports the server HandleAnalyzeNode body: FormatNodeFull
// (subject) + "## Callers (n)" / "## Callees (n)" sections (FormatNodeCompact per
// result) with the no-callers/no-callees empty lines, wrapped with the repo
// label. The composer supplies the resolved subject + caller/callee node slices.
func RenderAnalyzeNode(repoLabel string, subject *knowledgev1.Node, callers, callees []*knowledgev1.Node, includeSource bool) string {
	var sb strings.Builder
	sb.WriteString(FormatCodeNodeFull(subject))
	fmt.Fprintf(&sb, "\n## Callers (%d)\n\n", len(callers))
	for _, n := range callers {
		sb.WriteString(FormatCodeNodeCompact(n, includeSource))
	}
	if len(callers) == 0 {
		sb.WriteString("No callers found.\n")
	}
	fmt.Fprintf(&sb, "\n## Callees (%d)\n\n", len(callees))
	for _, n := range callees {
		sb.WriteString(FormatCodeNodeCompact(n, includeSource))
	}
	if len(callees) == 0 {
		sb.WriteString("No callees found.\n")
	}
	return FormatCodeWithRepo(repoLabel, sb.String())
}
