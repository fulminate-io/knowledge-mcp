// SPDX-License-Identifier: Apache-2.0

package kgtypes

import "testing"

// TestNodeType_IdiomIsAKnowledgeType pins the client half of the practice
// `idiom` vocabulary entry: the wire literal a language-idiom landing writes,
// registered as a knowledge type so the classifier never routes it through the
// code-graph path. The server half is pinned in its own module.
func TestNodeType_IdiomIsAKnowledgeType(t *testing.T) {
	if got, want := string(NodeIdiom), "idiom"; got != want {
		t.Fatalf("NodeIdiom literal = %q, want %q", got, want)
	}
	if !knowledgeTypes[NodeIdiom] {
		t.Fatalf("NodeIdiom (%q) must be in knowledgeTypes map", NodeIdiom)
	}
	if !NodeIdiom.isKnowledgeType() {
		t.Fatalf("NodeIdiom.isKnowledgeType() = false, want true")
	}
	if NodeIdiom.IsCodeType() {
		t.Fatalf("NodeIdiom.IsCodeType() = true, want false")
	}
}
