// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// helpers ---------------------------------------------------------------

// goChunk returns a tree-sitter Chunk shaped like a Go function declaration.
func goChunk(name, file string, exported bool) treesitter.Chunk {
	return treesitter.Chunk{
		Content:   "func " + name + "() {}",
		FilePath:  file,
		Language:  treesitter.LangGo,
		ChunkType: "function_declaration",
		Name:      name,
		StartLine: 1,
		EndLine:   3,
		Exported:  exported,
		Context:   treesitter.ChunkContext{PackageName: "pkg"},
	}
}

// commentChunk returns a tree-sitter Chunk shaped like a line comment.
func commentChunk(file string) treesitter.Chunk {
	return treesitter.Chunk{
		Content:   "// a comment",
		FilePath:  file,
		Language:  treesitter.LangGo,
		ChunkType: "comment",
		StartLine: 1,
		EndLine:   1,
	}
}

// findEdges returns every edge of the given type from the slice.
func findEdges(edges []*knowledgev1.Edge, t kgtypes.EdgeType) []*knowledgev1.Edge {
	var out []*knowledgev1.Edge
	for _, e := range edges {
		if kgtypes.EdgeType(e.Type) == t {
			out = append(out, e)
		}
	}
	return out
}

// nodeByID returns the node with the given ID, or nil.
func nodeByID(nodes []*knowledgev1.Node, id string) *knowledgev1.Node {
	for _, n := range nodes {
		if n.Id == id {
			return n
		}
	}
	return nil
}

// nodesByType returns every node of the given type.
func nodesByType(nodes []*knowledgev1.Node, t kgtypes.NodeType) []*knowledgev1.Node {
	var out []*knowledgev1.Node
	for _, n := range nodes {
		if kgtypes.NodeType(n.Type) == t {
			out = append(out, n)
		}
	}
	return out
}

// tests -----------------------------------------------------------------

// TestChunkResultsToPopulate_OneLanguageOneNode confirms that two Results
// in the same language produce exactly ONE NodeLanguage hub node.
func TestChunkResultsToPopulate_OneLanguageOneNode(t *testing.T) {
	results := []*treesitter.Result{
		{
			FilePath: "a.go",
			Language: treesitter.LangGo,
			Chunks:   []treesitter.Chunk{goChunk("Foo", "a.go", true)},
		},
		{
			FilePath: "b.go",
			Language: treesitter.LangGo,
			Chunks:   []treesitter.Chunk{goChunk("Bar", "b.go", true)},
		},
	}
	pop := chunkResultsToPopulate("myrepo", "", results)

	langNodes := nodesByType(pop.Nodes, kgtypes.NodeLanguage)
	if len(langNodes) != 1 {
		t.Fatalf("expected exactly 1 NodeLanguage, got %d", len(langNodes))
	}
	want := "lang:myrepo:go"
	if langNodes[0].Id != want {
		t.Errorf("lang_node ID: got %q, want %q", langNodes[0].Id, want)
	}
	if langNodes[0].SymbolName != "go" || langNodes[0].Language != "go" {
		t.Errorf("lang_node SymbolName/Language: got %q/%q, want go/go", langNodes[0].SymbolName, langNodes[0].Language)
	}
}

// TestChunkResultsToPopulate_TwoLanguagesTwoNodes confirms that distinct
// languages produce distinct NodeLanguage hub nodes with deterministic IDs.
func TestChunkResultsToPopulate_TwoLanguagesTwoNodes(t *testing.T) {
	tsChunk := treesitter.Chunk{
		Content:   "function fn() {}",
		FilePath:  "x.ts",
		Language:  treesitter.LangTypeScript,
		ChunkType: "function_declaration",
		Name:      "fn",
		StartLine: 1, EndLine: 1,
		Exported: true,
		Context:  treesitter.ChunkContext{PackageName: "pkg"},
	}
	results := []*treesitter.Result{
		{FilePath: "a.go", Language: treesitter.LangGo, Chunks: []treesitter.Chunk{goChunk("Foo", "a.go", true)}},
		{FilePath: "x.ts", Language: treesitter.LangTypeScript, Chunks: []treesitter.Chunk{tsChunk}},
	}
	pop := chunkResultsToPopulate("knowledge", "", results)

	langNodes := nodesByType(pop.Nodes, kgtypes.NodeLanguage)
	if len(langNodes) != 2 {
		t.Fatalf("expected 2 NodeLanguage, got %d", len(langNodes))
	}
	got := map[string]bool{}
	for _, n := range langNodes {
		got[n.Id] = true
	}
	for _, want := range []string{"lang:knowledge:go", "lang:knowledge:typescript"} {
		if !got[want] {
			t.Errorf("missing expected lang_node ID %q", want)
		}
	}
}

// TestChunkResultsToPopulate_EmitsLanguageEdgePerSymbol confirms that
// every non-comment chunk produces an EdgeLanguage edge to its lang_node.
func TestChunkResultsToPopulate_EmitsLanguageEdgePerSymbol(t *testing.T) {
	results := []*treesitter.Result{
		{
			FilePath: "a.go",
			Language: treesitter.LangGo,
			Chunks: []treesitter.Chunk{
				goChunk("Foo", "a.go", true),
				goChunk("bar", "a.go", false),
			},
		},
	}
	pop := chunkResultsToPopulate("myrepo", "", results)

	langEdges := findEdges(pop.Edges, kgtypes.EdgeLanguage)
	if len(langEdges) != 2 {
		t.Fatalf("expected 2 EdgeLanguage edges (one per symbol chunk), got %d", len(langEdges))
	}
	for _, e := range langEdges {
		if e.ToId != "lang:myrepo:go" {
			t.Errorf("EdgeLanguage ToID: got %q, want lang:myrepo:go", e.ToId)
		}
	}
}

// TestChunkResultsToPopulate_NoLanguageEdgeForComments confirms that
// comment chunks do NOT produce EdgeLanguage edges.
func TestChunkResultsToPopulate_NoLanguageEdgeForComments(t *testing.T) {
	results := []*treesitter.Result{
		{
			FilePath: "a.go",
			Language: treesitter.LangGo,
			Chunks: []treesitter.Chunk{
				commentChunk("a.go"),
				commentChunk("a.go"),
			},
		},
	}
	pop := chunkResultsToPopulate("myrepo", "", results)

	langEdges := findEdges(pop.Edges, kgtypes.EdgeLanguage)
	if len(langEdges) != 0 {
		t.Errorf("expected 0 EdgeLanguage edges from comment-only chunks, got %d", len(langEdges))
	}
	// And no NodeLanguage either — there were no non-comment chunks to anchor it.
	langNodes := nodesByType(pop.Nodes, kgtypes.NodeLanguage)
	if len(langNodes) != 0 {
		t.Errorf("expected 0 NodeLanguage from comment-only chunks, got %d", len(langNodes))
	}
}

// TestChunkResultsToPopulate_FoldsCommentsIntoDescription confirms that
// preceding comment chunks attach to the next declaration's Description
// even when the chunker emits declarations BEFORE orphan comments in the
// raw chunk slice — populate must sort by StartByte before folding.
//
// Real chunker output order (treesitter/chunker.go:243 declarations loop,
// then :257 collectOrphans appending comments) is `[decl, comment]`,
// not document order `[comment, decl]`. Without the sort this test would
// fail with Description="" — every code node's doc comment would be lost.
func TestChunkResultsToPopulate_FoldsCommentsIntoDescription(t *testing.T) {
	docComment := treesitter.Chunk{
		Content:   "// Authenticate validates a token.",
		FilePath:  "a.go",
		Language:  treesitter.LangGo,
		ChunkType: "comment",
		StartLine: 1,
		EndLine:   1,
		StartByte: 0,
		EndByte:   34,
	}
	decl := treesitter.Chunk{
		Content:   "func Authenticate() {}",
		FilePath:  "a.go",
		Language:  treesitter.LangGo,
		ChunkType: "function_declaration",
		Name:      "Authenticate",
		StartLine: 2,
		EndLine:   2,
		StartByte: 35,
		EndByte:   57,
		Exported:  true,
		Context:   treesitter.ChunkContext{PackageName: "pkg"},
	}
	results := []*treesitter.Result{
		{
			FilePath: "a.go",
			Language: treesitter.LangGo,
			// Wrong-order on purpose: mirror the real chunker output where
			// declarations come from the TopLevel query loop first and
			// orphan comments are appended afterward by collectOrphans.
			Chunks: []treesitter.Chunk{decl, docComment},
		},
	}
	pop := chunkResultsToPopulate("myrepo", "", results)

	declID := "a.go:Authenticate"
	n := nodeByID(pop.Nodes, declID)
	if n == nil {
		t.Fatalf("declaration node %q not produced", declID)
	}
	if n.Description == "" {
		t.Fatalf("declaration Description not populated from preceding comment — got empty")
	}
	if !contains(n.Description, "Authenticate validates a token") {
		t.Errorf("Description missing comment text: %q", n.Description)
	}
}

// contains is a small helper to keep the test reads scannable.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestChunkResultsToPopulate_PromotesIsExported confirms that the legacy
// "exported" metadata key is no longer written and that node.IsExported
// is populated from chunk.Exported (true and false).
func TestChunkResultsToPopulate_PromotesIsExported(t *testing.T) {
	results := []*treesitter.Result{
		{
			FilePath: "a.go",
			Language: treesitter.LangGo,
			Chunks: []treesitter.Chunk{
				goChunk("Public", "a.go", true),
				goChunk("private", "a.go", false),
			},
		},
	}
	pop := chunkResultsToPopulate("myrepo", "", results)

	pubID := ChunkNodeID(goChunk("Public", "a.go", true))
	privID := ChunkNodeID(goChunk("private", "a.go", false))

	pub := nodeByID(pop.Nodes, pubID)
	if pub == nil {
		t.Fatalf("Public node missing — looked for %q", pubID)
	}
	if !pub.IsExported {
		t.Errorf("Public.IsExported = false; want true")
	}
	if pub.Metadata["exported"] != "" {
		t.Errorf("Public still carries legacy metadata exported=%q; want empty", pub.Metadata["exported"])
	}

	priv := nodeByID(pop.Nodes, privID)
	if priv == nil {
		t.Fatalf("private node missing — looked for %q", privID)
	}
	if priv.IsExported {
		t.Errorf("private.IsExported = true; want false")
	}
}

// goTestChunk returns a tree-sitter Chunk for a Go function declaration with
// IsTest/TestKind populated. Mirrors goChunk but for the test-classification
// dimension.
func goTestChunk(name, file string, isTest bool, kind treesitter.TestKind) treesitter.Chunk {
	return treesitter.Chunk{
		Content:   "func " + name + "() {}",
		FilePath:  file,
		Language:  treesitter.LangGo,
		ChunkType: "function_declaration",
		Name:      name,
		StartLine: 1,
		EndLine:   3,
		IsTest:    isTest,
		TestKind:  kind,
		Context:   treesitter.ChunkContext{PackageName: "pkg"},
	}
}

// TestChunkResultsToPopulate_PromotesIsTest confirms that chunkToNode promotes
// chunk.IsTest and chunk.TestKind onto the persisted Node, with no metadata
// fallback. Mirrors TestChunkResultsToPopulate_PromotesIsExported.
func TestChunkResultsToPopulate_PromotesIsTest(t *testing.T) {
	results := []*treesitter.Result{
		{
			FilePath: "foo_test.go",
			Language: treesitter.LangGo,
			Chunks: []treesitter.Chunk{
				goTestChunk("TestFoo", "foo_test.go", true, treesitter.TestKindTest),
				goTestChunk("BenchmarkFoo", "foo_test.go", true, treesitter.TestKindBenchmark),
				goTestChunk("RegularFunc", "foo.go", false, treesitter.TestKindNone),
			},
		},
	}
	pop := chunkResultsToPopulate("myrepo", "", results)

	testID := ChunkNodeID(goTestChunk("TestFoo", "foo_test.go", true, treesitter.TestKindTest))
	benchID := ChunkNodeID(goTestChunk("BenchmarkFoo", "foo_test.go", true, treesitter.TestKindBenchmark))
	regID := ChunkNodeID(goTestChunk("RegularFunc", "foo.go", false, treesitter.TestKindNone))

	tn := nodeByID(pop.Nodes, testID)
	if tn == nil {
		t.Fatalf("TestFoo node missing — looked for %q", testID)
	}
	if !tn.IsTest {
		t.Errorf("TestFoo.IsTest = false; want true")
	}
	if tn.TestKind != "test" {
		t.Errorf("TestFoo.TestKind = %q; want %q", tn.TestKind, "test")
	}
	if tn.Metadata["is_test"] != "" {
		t.Errorf("TestFoo carries legacy metadata is_test=%q; want empty", tn.Metadata["is_test"])
	}
	if tn.Metadata["test_kind"] != "" {
		t.Errorf("TestFoo carries legacy metadata test_kind=%q; want empty", tn.Metadata["test_kind"])
	}

	bn := nodeByID(pop.Nodes, benchID)
	if bn == nil {
		t.Fatalf("BenchmarkFoo node missing — looked for %q", benchID)
	}
	if !bn.IsTest {
		t.Errorf("BenchmarkFoo.IsTest = false; want true")
	}
	if bn.TestKind != "benchmark" {
		t.Errorf("BenchmarkFoo.TestKind = %q; want %q", bn.TestKind, "benchmark")
	}

	rn := nodeByID(pop.Nodes, regID)
	if rn == nil {
		t.Fatalf("RegularFunc node missing — looked for %q", regID)
	}
	if rn.IsTest {
		t.Errorf("RegularFunc.IsTest = true; want false")
	}
	if rn.TestKind != "" {
		t.Errorf("RegularFunc.TestKind = %q; want empty", rn.TestKind)
	}
}
