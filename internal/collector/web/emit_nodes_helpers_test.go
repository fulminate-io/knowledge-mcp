// SPDX-License-Identifier: Apache-2.0

package web

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// This file holds the shared helpers for emit_nodes_test.go. Split
// out so emit_nodes_test.go stays under the 300 LOC recommended cap.

// assertInlineEmphasis fails the test unless the first node whose
// type and content match carries inline_emphasis metadata that
// round-trips to want. want==nil means the key must be absent.
func assertInlineEmphasis(t *testing.T, nodes []*knowledgev1.Node, nodeType, content string, want []inlineEmphasis) {
	t.Helper()
	for _, n := range nodes {
		if n.Type != nodeType || n.Content != content {
			continue
		}
		raw, ok := n.Metadata["inline_emphasis"]
		if want == nil {
			if ok {
				t.Errorf("%s %q: inline_emphasis should be absent, got %q", nodeType, content, raw)
			}
			return
		}
		if !ok {
			t.Fatalf("%s %q: inline_emphasis missing (metadata=%v)", nodeType, content, n.Metadata)
		}
		var decoded []inlineEmphasis
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("%s %q: inline_emphasis not JSON: %v (raw=%q)", nodeType, content, err, raw)
		}
		if len(decoded) != len(want) {
			t.Fatalf("%s %q: want %d entries, got %d: %+v",
				nodeType, content, len(want), len(decoded), decoded)
		}
		for i, got := range decoded {
			if got != want[i] { //nolint:gosec // len-checked above, indices bounded
				t.Errorf("%s %q entry %d: got %+v, want %+v",
					nodeType, content, i, got, want[i]) //nolint:gosec // len-checked above
			}
		}
		return
	}
	t.Fatalf("no %s node with content %q", nodeType, content)
}

func indexNodes(nodes []*knowledgev1.Node) map[string]*knowledgev1.Node {
	m := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		m[n.Id] = n
	}
	return m
}

func findOneChild(t *testing.T, edges []kgwire.BatchEdge, parentID, wantType string, byID map[string]*knowledgev1.Node) string {
	t.Helper()
	var found string
	for _, e := range edges {
		if e.Type != kgtypes.EdgeContains || e.FromID != parentID {
			continue
		}
		if byID[e.ToID].Type == wantType {
			if found != "" {
				t.Fatalf("multiple %s children of %s", wantType, parentID)
			}
			found = e.ToID
		}
	}
	return found
}

func findFirstChildID(edges []kgwire.BatchEdge, parentID, wantType string, byID map[string]*knowledgev1.Node) string {
	for _, e := range edges {
		if e.Type != kgtypes.EdgeContains || e.FromID != parentID {
			continue
		}
		if byID[e.ToID].Type == wantType {
			return e.ToID
		}
	}
	return ""
}

// childrenOf returns the child node types in the order their EdgeContains
// edges appear in the slice.
func childrenOf(edges []kgwire.BatchEdge, parentID string, byID map[string]*knowledgev1.Node) []string {
	var types []string
	for _, e := range edges {
		if e.Type == kgtypes.EdgeContains && e.FromID == parentID {
			types = append(types, byID[e.ToID].Type)
		}
	}
	return types
}

// sectionChildPositions extracts the `position` metadata from each
// section→child EdgeContains edge in order.
func sectionChildPositions(edges []kgwire.BatchEdge, sectionID string, byID map[string]*knowledgev1.Node) []int {
	var positions []int
	for _, e := range edges {
		if e.Type == kgtypes.EdgeContains && e.FromID == sectionID {
			md := map[string]string{}
			_ = json.Unmarshal([]byte(e.Evidence), &md)
			p, err := strconv.Atoi(md["position"])
			if err != nil {
				p = -1
			}
			positions = append(positions, p)
			_ = byID // retained for potential future assertions
		}
	}
	return positions
}

func parseMeta(t *testing.T, raw string) map[string]string {
	t.Helper()
	m := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return m
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("edge evidence not JSON: %q (%v)", raw, err)
	}
	return m
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
