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

// AnalyzeView is the whole input to RenderAnalyzeNode — a struct rather than a
// nine-argument positional list.
//
// Callers and Callees hold only the PLAIN entries: a group's candidates are
// rendered inside its group block and are removed from these slices by the
// composer, so the section counts never report the same reference as both N
// callers and one group of N.
//
// TestCallers/TestCallees are the call traffic whose SOURCE is test code, walked
// over the distinct TEST_CALLS edge. They are SEPARATE CARRIERS ON PURPOSE:
// folding test callers into Callers would re-create at the display layer exactly
// the production/test conflation the distinct edge type exists to end. Each test
// side carries its own groups on the same terms as the production sides.
type AnalyzeView struct {
	RepoLabel        string
	Subject          *knowledgev1.Node
	Callers          []*knowledgev1.Node
	Callees          []*knowledgev1.Node
	CallerGroups     []CandidateGroup
	CalleeGroups     []CandidateGroup
	TestCallers      []*knowledgev1.Node
	TestCallees      []*knowledgev1.Node
	TestCallerGroups []CandidateGroup
	TestCalleeGroups []CandidateGroup
	Candidates       map[string]*knowledgev1.Node
	Reached          map[string]bool
	IncludeSource    bool
	Incomplete       bool
}

// RenderAnalyzeNode ports the server HandleAnalyzeNode body: FormatNodeFull
// (subject) + "## Callers (n)" / "## Callees (n)" sections (FormatNodeCompact per
// result) with the no-callers/no-callees empty lines, wrapped with the repo
// label. The composer supplies the resolved subject + caller/callee node slices.
//
// Each section additionally renders its multi-candidate groups as one block per
// group, after its plain entries. THE SECTION COUNT COUNTS THE ENTRIES IT LISTS:
// candidates live in the group block and are not also plain callers/callees.
//
// The test-call sections follow their production counterpart per direction, and
// are OMITTED ENTIRELY when a side has neither entries nor groups — unlike the
// production sections, which state their emptiness. The asymmetry is deliberate:
// a graph collected before the TEST_CALLS edge existed carries no test-call edge
// at all, so "No test callers found." would report an absence as a fact when the
// truth is that test traffic is not indexed yet.
func RenderAnalyzeNode(v AnalyzeView) string {
	var sb strings.Builder
	sb.WriteString(FormatCodeNodeFull(v.Subject))
	writeAnalyzeSection(&sb, v, "Callers", "No callers found.\n", v.Callers, v.CallerGroups)
	writeAnalyzeTestSection(&sb, v, "Test Callers", v.TestCallers, v.TestCallerGroups)
	writeAnalyzeSection(&sb, v, "Callees", "No callees found.\n", v.Callees, v.CalleeGroups)
	writeAnalyzeTestSection(&sb, v, "Test Callees", v.TestCallees, v.TestCalleeGroups)
	if v.Incomplete {
		sb.WriteString("\ngroup reconstruction incomplete - some candidates or reachable nodes are not shown\n")
	}
	return FormatCodeWithRepo(v.RepoLabel, sb.String())
}

// writeAnalyzeSection writes one analyze section: the "## <label> (n)" header
// counting the PLAIN entries, those entries, the emptyLine when the section has
// neither entries nor groups, then the group blocks.
func writeAnalyzeSection(sb *strings.Builder, v AnalyzeView, label, emptyLine string, nodes []*knowledgev1.Node, groups []CandidateGroup) {
	fmt.Fprintf(sb, "\n## %s (%d)\n\n", label, len(nodes))
	for _, n := range nodes {
		sb.WriteString(FormatCodeNodeCompact(n, v.IncludeSource))
	}
	if len(nodes) == 0 && len(groups) == 0 {
		sb.WriteString(emptyLine)
	}
	writeCandidateGroups(sb, groups, v.Candidates, v.Reached)
}

// writeAnalyzeTestSection writes a test-call section, or nothing at all when the
// side is empty. See RenderAnalyzeNode for why an empty test side is silent
// where an empty production side is not.
func writeAnalyzeTestSection(sb *strings.Builder, v AnalyzeView, label string, nodes []*knowledgev1.Node, groups []CandidateGroup) {
	if len(nodes) == 0 && len(groups) == 0 {
		return
	}
	writeAnalyzeSection(sb, v, label, "", nodes, groups)
}
