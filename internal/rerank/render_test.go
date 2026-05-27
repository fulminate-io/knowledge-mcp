// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestRenderForRerank_CodeNode verifies the code-graph branch packs
// SymbolName, Signature, FilePath, Summary, and Keywords. Code-graph
// node types are dynamic strings produced by the tree-sitter chunker
// (e.g., "function", "method_declaration") and route through
// IsCodeType() — not an enumerated knowledge type.
func TestRenderForRerank_CodeNode(t *testing.T) {
	n := &knowledgev1.Node{
		Type:       "function",
		SymbolName: "Authenticate",
		Signature:  "func Authenticate(token string) bool",
		FilePath:   "pkg/auth.go",
		Summary:    "Validates a JWT and returns true on success.",
		Keywords:   "auth jwt validate token",
	}
	out := renderForRerank(n)

	assert.Contains(t, out, "Authenticate")
	assert.Contains(t, out, "func Authenticate(token string) bool")
	assert.Contains(t, out, "pkg/auth.go")
	assert.Contains(t, out, "Validates a JWT")
	assert.Contains(t, out, "auth jwt validate token")
	// Empty fields are skipped: no leading/trailing newlines from missing data.
	assert.False(t, strings.HasPrefix(out, "\n"))
}

// TestRenderForRerank_CodeNode_CallersLine confirms the production-default
// caller hint: when a code node carries n.Value("callers"), the renderer
// emits a Go-doc-style `// called by …` line so Voyage's cross-encoder
// reads it as natural code documentation. augmentCallerHints populates
// the metadata key at rerank time; the renderer just surfaces it.
func TestRenderForRerank_CodeNode_CallersLine(t *testing.T) {
	n := &knowledgev1.Node{
		Type:       "function",
		SymbolName: "decryptGraphFile",
		FilePath:   "domains/store/encryption.go",
	}
	kgtypes.SetValue(n, "callers", "loadKnowledgeFile, loadOverlayForMerge")
	out := renderForRerank(n)
	assert.Contains(t, out, "// called by loadKnowledgeFile, loadOverlayForMerge")
}

// TestRenderForRerank_CodeNode_TestKind confirms the test_kind plumbing:
// when a code node carries a non-empty TestKind, the renderer emits a
// Go-doc-style `// test_kind: <kind>` line so Voyage's cross-encoder
// reads the test classification as natural code documentation. When
// TestKind is empty the line is omitted entirely (preserving byte-
// identical output to before this plumbing landed).
func TestRenderForRerank_CodeNode_TestKind(t *testing.T) {
	t.Run("non-empty kind emits line", func(t *testing.T) {
		n := &knowledgev1.Node{
			Type:       "function",
			SymbolName: "BenchmarkAuthenticate",
			FilePath:   "pkg/auth_test.go",
			TestKind:   "benchmark",
		}
		out := renderForRerank(n)
		assert.Contains(t, out, "// test_kind: benchmark")
	})
	t.Run("empty kind omits line", func(t *testing.T) {
		n := &knowledgev1.Node{
			Type:       "function",
			SymbolName: "Authenticate",
			FilePath:   "pkg/auth.go",
		}
		out := renderForRerank(n)
		assert.NotContains(t, out, "test_kind")
	})
	// test_block flows through renderCodeForRerank cleanly: chunk type is
	// dynamic (NodeType("test_block")), IsCodeType() returns true, and the
	// renderer emits SymbolName, FilePath, and the test_kind line when the
	// reranker-tuning ticket eventually populates TestKind on test_blocks.
	// renderForRerank itself is UNCHANGED for this ticket — the assertion
	// proves the pre-existing chunk-type-agnostic code path handles the new
	// chunk type without modification.
	t.Run("test_block chunk type renders cleanly", func(t *testing.T) {
		n := &knowledgev1.Node{
			Type:       "test_block",
			SymbolName: "rejects expired",
			FilePath:   "pkg/auth_test.ts",
			Signature:  "(done)",
			TestKind:   "test",
		}
		out := renderForRerank(n)
		assert.Contains(t, out, "rejects expired", "SymbolName must surface")
		assert.Contains(t, out, "pkg/auth_test.ts", "FilePath must surface")
		assert.Contains(t, out, "// test_kind: test",
			"test_kind line emits identically for test_block as for function")
	})
}

// TestRenderForRerank_CodeNode_NoCallersWhenEmpty confirms the renderer
// emits no callers line when the metadata key is absent (leaf node, or
// non-code-graph search where augmentCallerHints skipped).
func TestRenderForRerank_CodeNode_NoCallersWhenEmpty(t *testing.T) {
	n := &knowledgev1.Node{
		Type:       "function",
		SymbolName: "Leaf",
		FilePath:   "pkg/leaf.go",
	}
	out := renderForRerank(n)
	assert.NotContains(t, out, "// called by")
}

// TestRenderForRerank_CodeNode_Description verifies the doc-comment branch:
// Description is included between Summary and Keywords. Doc comments carry
// the natural-language phrasing the user typically queries with — load-
// bearing post-c086ac9e (comments folded into Description).
func TestRenderForRerank_CodeNode_Description(t *testing.T) {
	n := &knowledgev1.Node{
		Type:        "function",
		SymbolName:  "tokenizeCode",
		Description: "tokenizeCode splits a code identifier into BM25 search tokens, handling camelCase, snake_case, and unicode boundaries.",
		Summary:     "Tokenizes code into BM25 terms.",
	}
	out := renderForRerank(n)
	assert.Contains(t, out, "splits a code identifier")
	assert.Contains(t, out, "camelCase")
	assert.Contains(t, out, "snake_case")
}

// TestTruncateDocComment_FirstParagraph caps long multi-paragraph doc
// comments at the first \n\n break when it lands under the 1000-char cap.
// Subsequent paragraphs are typically edge-case discussion, not what
// queries match against.
func TestTruncateDocComment_FirstParagraph(t *testing.T) {
	desc := "First paragraph headline that the query matches.\n\nSecond paragraph: edge cases nobody queries for, including a long discussion of failure modes that we don't want polluting the rerank doc text."
	got := truncateDocComment(desc)
	assert.Equal(t, "First paragraph headline that the query matches.", got)
}

// TestTruncateDocComment_HardCap covers single-paragraph descriptions that
// exceed the 1000-char budget — clamp to 1000 chars.
func TestTruncateDocComment_HardCap(t *testing.T) {
	desc := strings.Repeat("a", 1500)
	got := truncateDocComment(desc)
	assert.Len(t, got, 1000)
}

// TestTruncateDocComment_ShortPassthrough confirms short single-paragraph
// descriptions pass through unchanged.
func TestTruncateDocComment_ShortPassthrough(t *testing.T) {
	desc := "Compact one-line doc comment."
	assert.Equal(t, desc, truncateDocComment(desc))
}

// TestRenderForRerank_KnowledgeNode verifies the default branch (knowledge
// graph) packs Type + SymbolName + Description + Status. kgtypes.NodeDecision is
// representative of the structured knowledge types.
func TestRenderForRerank_KnowledgeNode(t *testing.T) {
	n := &knowledgev1.Node{
		Type:        string(kgtypes.NodeDecision),
		SymbolName:  "Adopt Voyage rerank-2.5",
		Description: "Use Voyage's cross-encoder over fan-in scoring.",
		Status:      "accepted",
	}
	out := renderForRerank(n)

	assert.Contains(t, out, string(kgtypes.NodeDecision))
	assert.Contains(t, out, "Adopt Voyage rerank-2.5")
	assert.Contains(t, out, "Use Voyage's cross-encoder over fan-in scoring.")
	assert.Contains(t, out, "accepted")
	// Code-shape fields are absent (none in the input).
	assert.NotContains(t, out, "Signature")
}

// TestRenderForRerank_PracticeNode verifies the practice-graph branch packs
// SymbolName + Description + Summary. kgtypes.NodePattern is the canonical practice-
// graph type (NodeUseCase / NodeExample share the same branch).
func TestRenderForRerank_PracticeNode(t *testing.T) {
	n := &knowledgev1.Node{
		Type:        string(kgtypes.NodePattern),
		SymbolName:  "composite-layered-db",
		Description: "DB wrapper that fans-in across overlay+base layers.",
		Summary:     "compositeDB delegates reads to overlay-then-base layers.",
	}
	out := renderForRerank(n)

	assert.Contains(t, out, "composite-layered-db")
	assert.Contains(t, out, "DB wrapper that fans-in")
	assert.Contains(t, out, "compositeDB delegates reads")
}

// TestRenderForRerank_CloudNode verifies the cloud-graph branch packs
// Type:resource_type SymbolName in region\nSummary. Asserts the structural
// shape (separator characters and ordering), not the exact byte sequence.
// resource_type and region are set via Node.SetValue (Metadata map).
func TestRenderForRerank_CloudNode(t *testing.T) {
	n := &knowledgev1.Node{
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "my-bucket",
		Summary:    "S3 bucket my-bucket in us-east-1.",
	}
	kgtypes.SetValue(n, "resource_type", "s3-bucket")
	kgtypes.SetValue(n, "region", "us-east-1")

	out := renderForRerank(n)

	// Field-shape: starts with cloud-resource:s3-bucket (no whitespace separator).
	assert.True(t, strings.HasPrefix(out, "cloud-resource:s3-bucket"),
		"cloud render must start with `<type>:<resource_type>`; got %q", out)
	assert.Contains(t, out, "my-bucket")
	assert.Contains(t, out, " in us-east-1")
	assert.Contains(t, out, "S3 bucket my-bucket in us-east-1.")
}

// TestRenderForRerank_CICDNode verifies the cicd-graph branch packs
// Type:resource_type SymbolName (provider)\nSummary. Mirrors the cloud
// branch with provider substituted for region.
func TestRenderForRerank_CICDNode(t *testing.T) {
	n := &knowledgev1.Node{
		Type:       string(kgtypes.NodeCICDResource),
		SymbolName: "ci.yml",
		Summary:    "GitHub Actions workflow ci.yml triggered on push.",
	}
	kgtypes.SetValue(n, "resource_type", "github-actions-workflow")
	kgtypes.SetValue(n, "provider", "github")

	out := renderForRerank(n)

	assert.True(t, strings.HasPrefix(out, "cicd-resource:github-actions-workflow"),
		"cicd render must start with `<type>:<resource_type>`; got %q", out)
	assert.Contains(t, out, "ci.yml")
	assert.Contains(t, out, "(github)")
	assert.Contains(t, out, "GitHub Actions workflow ci.yml")
}

// TestRenderForRerank_DefaultNode is the regression guard for T2-4: the
// default branch (currently renderKnowledgeForRerank) must NOT fall
// through to renderCodeForRerank. kgtypes.NodeMetaValue is a knowledge type
// without code-shape fields — its render must NOT contain code-only
// fields like Signature/FilePath/Keywords.
//
// If a future refactor reintroduces "default falls through to code",
// this test fails because the code shape would surface a Signature
// (here empty) and emit blank lines instead of the knowledge shape.
func TestRenderForRerank_DefaultNode(t *testing.T) {
	n := &knowledgev1.Node{
		Type:       string(kgtypes.NodeMetaValue),
		SymbolName: "promoted-key",
		Summary:    "Promoted metadata key value-node.",
	}
	out := renderForRerank(n)

	// Knowledge shape: Type prefix and SymbolName present.
	assert.Contains(t, out, string(kgtypes.NodeMetaValue))
	assert.Contains(t, out, "promoted-key")

	// Regression guard: default must NOT produce the code-render shape.
	// renderCodeForRerank starts with SymbolName (no Type prefix). The
	// knowledge render starts with Type. If the default branch ever
	// regresses to code rendering, the prefix flips.
	assert.True(t, strings.HasPrefix(out, string(kgtypes.NodeMetaValue)),
		"default branch must use knowledge render (Type prefix), not code render; got %q", out)
}
