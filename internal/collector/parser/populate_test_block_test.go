// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestChunkResultsToPopulate_TestBlockPassThrough verifies that a test_block
// chunk flows through chunkToNode without any chunk-type-specific branching:
// NodeType is the literal "test_block" string, IsCodeType is true (so the
// node participates in code-graph indexing), IsComment is false (so the
// chunk is NOT folded into the next symbol's Description), and IsTest /
// TestKind both propagate the chunker's defaults (false / empty) — Bucket B
// owns predicate population.
func TestChunkResultsToPopulate_TestBlockPassThrough(t *testing.T) {
	chunk := treesitter.Chunk{
		Content:   "it(\"rejects expired\", (done) => {})",
		FilePath:  "auth_test.ts",
		Language:  treesitter.LangTypeScript,
		ChunkType: "test_block",
		Name:      "rejects expired",
		StartLine: 4, EndLine: 4,
		Exported:   false,
		ParentName: "Auth",
		Context: treesitter.ChunkContext{
			PackageName: "auth",
			Signature:   "(done)",
		},
	}

	results := []*treesitter.Result{
		{
			FilePath: "auth_test.ts",
			Language: treesitter.LangTypeScript,
			Chunks:   []treesitter.Chunk{chunk},
		},
	}

	pop := chunkResultsToPopulate("myrepo", &treesitter.RepoContext{}, results)

	id := ChunkNodeID(chunk)
	n := nodeByID(pop.Nodes, id)
	if n == nil {
		t.Fatalf("test_block node missing — looked for %q", id)
	}

	if kgtypes.NodeType(n.Type) != kgtypes.NodeType("test_block") {
		t.Errorf("Type = %q; want \"test_block\"", n.Type)
	}
	if !kgtypes.NodeType(n.Type).IsCodeType() {
		t.Errorf("Type.IsCodeType() = false; want true")
	}
	if kgtypes.NodeType(n.Type).IsComment() {
		t.Errorf("Type.IsComment() = true; want false (test_block must NOT fold into surrounding symbol)")
	}
	if n.IsTest {
		t.Errorf("IsTest = true; this ticket leaves it false (Bucket B's scope)")
	}
	if n.TestKind != "" {
		t.Errorf("TestKind = %q; this ticket leaves it empty (Bucket B's scope)", n.TestKind)
	}
	if n.Signature != "(done)" {
		t.Errorf("Signature = %q; want %q (round-trip from Context.Signature)", n.Signature, "(done)")
	}
	if n.SymbolName != "rejects expired" {
		t.Errorf("SymbolName = %q; want %q", n.SymbolName, "rejects expired")
	}
	if n.IsExported {
		t.Errorf("IsExported = true; test_block must always be false")
	}
}
