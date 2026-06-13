// SPDX-License-Identifier: Apache-2.0

package kgtypes

import (
	"testing"
)

// TestNodeType_PatternTypesAreKnowledgeTypes asserts that the pattern catalog
// node types (NodePattern, NodeReuseCheck) are registered as knowledge types
// — both present in the knowledgeTypes map and reporting isKnowledgeType()=true
// / IsCodeType()=false. The pattern catalog (T1-T5) keys off these classifiers
// to skip code-graph-only summarization paths and route pattern/reuse_check
// nodes through the knowledge graph instead.
func TestNodeType_PatternTypesAreKnowledgeTypes(t *testing.T) {
	t.Run("pattern is a knowledge type", func(t *testing.T) {
		if !knowledgeTypes[NodePattern] {
			t.Fatalf("NodePattern (%q) must be in knowledgeTypes map", NodePattern)
		}
		if !NodePattern.isKnowledgeType() {
			t.Fatalf("NodePattern.isKnowledgeType() = false, want true")
		}
		if NodePattern.IsCodeType() {
			t.Fatalf("NodePattern.IsCodeType() = true, want false")
		}
	})

	t.Run("reuse_check is a knowledge type", func(t *testing.T) {
		if !knowledgeTypes[NodeReuseCheck] {
			t.Fatalf("NodeReuseCheck (%q) must be in knowledgeTypes map", NodeReuseCheck)
		}
		if !NodeReuseCheck.isKnowledgeType() {
			t.Fatalf("NodeReuseCheck.isKnowledgeType() = false, want true")
		}
		if NodeReuseCheck.IsCodeType() {
			t.Fatalf("NodeReuseCheck.IsCodeType() = true, want false")
		}
	})

	t.Run("pattern literals round-trip through the node-type value", func(t *testing.T) {
		if got, want := string(NodePattern), "pattern"; got != want {
			t.Fatalf("NodePattern literal = %q, want %q", got, want)
		}
		if got, want := string(NodeReuseCheck), "reuse_check"; got != want {
			t.Fatalf("NodeReuseCheck literal = %q, want %q", got, want)
		}
	})
}

// TestNodeType_UseCaseAndExampleAreKnowledgeTypes asserts that the granular
// pattern-schema node types (NodeUseCase, NodeExample) are registered as
// knowledge types.
func TestNodeType_UseCaseAndExampleAreKnowledgeTypes(t *testing.T) {
	t.Run("use_case is a knowledge type", func(t *testing.T) {
		if !knowledgeTypes[NodeUseCase] {
			t.Fatalf("NodeUseCase (%q) must be in knowledgeTypes map", NodeUseCase)
		}
		if !NodeUseCase.isKnowledgeType() {
			t.Fatalf("NodeUseCase.isKnowledgeType() = false, want true")
		}
		if NodeUseCase.IsCodeType() {
			t.Fatalf("NodeUseCase.IsCodeType() = true, want false")
		}
	})

	t.Run("example is a knowledge type", func(t *testing.T) {
		if !knowledgeTypes[NodeExample] {
			t.Fatalf("NodeExample (%q) must be in knowledgeTypes map", NodeExample)
		}
		if !NodeExample.isKnowledgeType() {
			t.Fatalf("NodeExample.isKnowledgeType() = false, want true")
		}
		if NodeExample.IsCodeType() {
			t.Fatalf("NodeExample.IsCodeType() = true, want false")
		}
	})
}

// TestNodeMetaValue_IsKnowledgeType locks in that NodeMetaValue is
// classified as a knowledge type — this is the signal the IsCodeType
// classifier consults to skip code-graph summarization. A future
// refactor that drops NodeMetaValue from knowledgeTypes would silently
// route value-nodes through the LLM summarizer and break dedupe.
func TestNodeMetaValue_IsKnowledgeType(t *testing.T) {
	if !knowledgeTypes[NodeMetaValue] {
		t.Fatal("NodeMetaValue must be in knowledgeTypes map")
	}
	if !NodeMetaValue.isKnowledgeType() {
		t.Fatal("NodeMetaValue.isKnowledgeType() must be true")
	}
	if NodeMetaValue.IsCodeType() {
		t.Fatal("NodeMetaValue.IsCodeType() must be false (storage primitive, not code)")
	}
	if got, want := string(NodeMetaValue), "meta_value"; got != want {
		t.Fatalf("NodeMetaValue literal = %q, want %q", got, want)
	}
}

// TestNodeGraphTypeDef_WireLiteral pins the client NodeGraphTypeDef literal to
// the agreed cross-module wire string "graph_type_def". The server store
// vocabulary (cmd/knowledge-server/internal/store/node_types_vocab.go) carries
// an independent copy of this const — a deliberate per-module duplicate (no
// shared package). This per-module drift-guard plus its server twin
// (TestNodeGraphTypeDef_WireLiteral in store) fail if either literal changes
// without the other, keeping the two modules in lockstep on the wire value.
func TestNodeGraphTypeDef_WireLiteral(t *testing.T) {
	if got, want := string(NodeGraphTypeDef), "graph_type_def"; got != want {
		t.Fatalf("NodeGraphTypeDef literal = %q, want %q (must match the server store const + the proto file's documented wire string)", got, want)
	}
}

// TestHiveNodeTypes_WireLiterals pins the client kgtypes hive node-type literals
// to their agreed cross-module wire strings. The server store vocabulary
// (cmd/knowledge-server/internal/store/node_types_vocab.go) carries independent
// copies of these consts — a deliberate per-module duplicate (no shared
// package). This drift-guard plus its server twin
// (TestHiveNodeTypes_WireLiterals in store) fail if either module's literal
// changes without the other.
func TestHiveNodeTypes_WireLiterals(t *testing.T) {
	cases := []struct {
		got  NodeType
		want string
	}{
		{NodeHive, "hive"},
		{NodeMessage, "message"},
		{NodeHiveMember, "hive_member"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Fatalf("hive node-type literal = %q, want %q (must match the server store const)", c.got, c.want)
		}
	}
}
