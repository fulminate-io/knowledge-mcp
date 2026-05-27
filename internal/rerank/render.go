// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// renderForRerank builds the per-candidate document text given to the
// cross-encoder reranker. The reranker sees `query + this string` per
// candidate. We pack the most discriminating fields per node-type so the
// reranker can score on category + identity + body in one shot.
//
// Dispatch is per-graph-type — every graph that flows through the search
// fan-in gets its own branch so cloud/cicd nodes (which have empty
// SymbolName/FilePath/Signature in the code-render shape) still produce
// useful doc text.
//
// Code-graph nodes use the canonical IsCodeType() classifier rather than
// an enumeration of node-type strings: per-language tree-sitter chunk
// types (arrow_function, method_declaration, lexical_declaration, etc.)
// enter the graph as dynamic NodeType strings, and an enum-based switch
// would silently drop new chunk types into the default branch.
//
// The renderer is package-private — only voyageReranker.Rerank calls it.
func renderForRerank(n *knowledgev1.Node) string {
	switch {
	case kgtypes.NodeType(n.Type) == kgtypes.NodeCloudResource:
		return renderCloudForRerank(n)
	case kgtypes.NodeType(n.Type) == kgtypes.NodeCICDResource:
		return renderCICDForRerank(n)
	case kgtypes.NodeType(n.Type) == kgtypes.NodePattern || kgtypes.NodeType(n.Type) == kgtypes.NodeUseCase || kgtypes.NodeType(n.Type) == kgtypes.NodeExample:
		return renderPracticeForRerank(n)
	case kgtypes.NodeType(n.Type).IsCodeType():
		return renderCodeForRerank(n)
	default:
		return renderKnowledgeForRerank(n)
	}
}

// renderCodeForRerank packs SymbolName + Signature + FilePath + Summary +
// Description + Keywords. Excluded: Content (often hundreds of lines,
// blows the token budget). Description carries the source-code doc comment
// (folded into the field by the c086ac9e collector refactor) and is
// load-bearing for natural-language queries that match the function's
// own narrative more than the LLM-generated Summary.
func renderCodeForRerank(n *knowledgev1.Node) string {
	var b strings.Builder
	if n.SymbolName != "" {
		b.WriteString(n.SymbolName)
		b.WriteByte('\n')
	}
	if n.Signature != "" {
		b.WriteString(n.Signature)
		b.WriteByte('\n')
	}
	if n.FilePath != "" {
		b.WriteString(n.FilePath)
		b.WriteByte('\n')
	}
	if n.Summary != "" {
		b.WriteString(n.Summary)
		b.WriteByte('\n')
	}
	if desc := truncateDocComment(n.Description); desc != "" {
		b.WriteString(desc)
		b.WriteByte('\n')
	}
	// Production-default: emit caller list as a Go-doc-style comment line
	// when augmentCallerHints has populated the "callers" key. Voyage's
	// cross-encoder reads this as natural documentation rather than a
	// raw stat tag. Empty string when the candidate has no inbound CALLS
	// edges (leaf or untraced).
	if callers := kgtypes.Value(n, "callers"); callers != "" {
		b.WriteString("// called by ")
		b.WriteString(callers)
		b.WriteByte('\n')
	}
	// Surface test_kind so the rerank LLM sees the test/non-test classification.
	// Down-weighting test_kind=test/benchmark/example/fuzz on impl-style queries
	// is the reranker-tuning ticket (cddf5098); this ticket only plumbs the field
	// into the prompt.
	if n.TestKind != "" {
		b.WriteString("// test_kind: ")
		b.WriteString(n.TestKind)
		b.WriteByte('\n')
	}
	if hint := topologyHint(n); hint != "" {
		b.WriteString(hint)
		b.WriteByte('\n')
	}
	if n.Keywords != "" {
		b.WriteString(n.Keywords)
	}
	return b.String()
}

// truncateDocComment caps a Description for the rerank doc-text budget:
// first paragraph if a paragraph break appears before 1000 chars, else a
// hard 1000-char cap. Doc-comment paragraphs past the headline are usually
// edge-case discussion, not what users query for.
func truncateDocComment(s string) string {
	const maxLen = 1000
	if i := strings.Index(s, "\n\n"); i > 0 && i < maxLen {
		return s[:i]
	}
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// renderCloudForRerank emits Type:resource_type SymbolName in region\nSummary.
// Cloud nodes always carry resource_type metadata (collector/cloud/node.go:24);
// region is set when non-empty (node.go:26); Summary is populated by the
// per-resource-type helpers in collector/cloud/Summarize. The Type+resource_type
// prefix gives the reranker a category signal; SymbolName is the resource
// name; region disambiguates same-name resources across regions.
func renderCloudForRerank(n *knowledgev1.Node) string {
	var b strings.Builder
	b.WriteString(n.Type)
	if rt := kgtypes.Value(n, "resource_type"); rt != "" {
		b.WriteByte(':')
		b.WriteString(rt)
	}
	if n.SymbolName != "" {
		b.WriteByte(' ')
		b.WriteString(n.SymbolName)
	}
	if region := kgtypes.Value(n, "region"); region != "" {
		b.WriteString(" in ")
		b.WriteString(region)
	}
	if n.Summary != "" {
		b.WriteByte('\n')
		b.WriteString(n.Summary)
	}
	return b.String()
}

// renderCICDForRerank emits Type:resource_type SymbolName (provider)\nSummary.
// Mirrors the cloud branch with provider substituted for region. CI/CD
// nodes always carry resource_type (collector/cicd/node.go:24); provider
// is set when non-empty (node.go:26); Summary is populated by
// collector/cicd/Summarize.
func renderCICDForRerank(n *knowledgev1.Node) string {
	var b strings.Builder
	b.WriteString(n.Type)
	if rt := kgtypes.Value(n, "resource_type"); rt != "" {
		b.WriteByte(':')
		b.WriteString(rt)
	}
	if n.SymbolName != "" {
		b.WriteByte(' ')
		b.WriteString(n.SymbolName)
	}
	if provider := kgtypes.Value(n, "provider"); provider != "" {
		b.WriteString(" (")
		b.WriteString(provider)
		b.WriteByte(')')
	}
	if n.Summary != "" {
		b.WriteByte('\n')
		b.WriteString(n.Summary)
	}
	return b.String()
}

// renderPracticeForRerank packs SymbolName + Description + Summary for
// practice-graph node types (NodePattern, NodeUseCase, NodeExample).
// Pattern nodes carry shape sketches in Description; Summary is the
// search-optimized one-liner produced by the practice-graph creator.
func renderPracticeForRerank(n *knowledgev1.Node) string {
	var b strings.Builder
	if n.SymbolName != "" {
		b.WriteString(n.SymbolName)
		b.WriteByte('\n')
	}
	if n.Description != "" {
		b.WriteString(n.Description)
		b.WriteByte('\n')
	}
	if n.Summary != "" {
		b.WriteString(n.Summary)
	}
	return b.String()
}

// renderKnowledgeForRerank is the default branch — covers knowledge-graph
// node types (decision/finding/plan/phase/step/rule/thought/project/ticket/
// question/research/document/criterion/reuse_check/charge/thought_session)
// AND any unclassified type a future addition introduces. Emits
// Type\nSymbolName\nDescription\nStatus. Type prefix categorizes;
// SymbolName carries title; Description carries body; Status disambiguates
// lifecycle. For unclassified types missing Description/Status the output
// gracefully degrades to Type+SymbolName.
func renderKnowledgeForRerank(n *knowledgev1.Node) string {
	var b strings.Builder
	if n.Type != "" {
		b.WriteString(n.Type)
		b.WriteByte('\n')
	}
	if n.SymbolName != "" {
		b.WriteString(n.SymbolName)
		b.WriteByte('\n')
	}
	if n.Description != "" {
		b.WriteString(n.Description)
		b.WriteByte('\n')
	}
	if n.Status != "" {
		b.WriteString(n.Status)
	}
	return b.String()
}
