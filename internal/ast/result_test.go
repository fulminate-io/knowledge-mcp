// SPDX-License-Identifier: Apache-2.0

// result_test.go — Hydrate / buildCodeNodeIndex / lookup coverage against
// the v2 Capture shape (Text/Kind/Children/Line/StartByte/EndByte). T2-6
// from the plan-review round 1 — preserves test coverage for the
// enclosing-node resolver across the Capture struct expansion.

package ast

import (
	"context"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// fakeBackend implements HydratorBackend by emitting a fixed slice of
// nodes through the visitor callback.
type fakeBackend struct {
	nodes []*knowledgev1.Node
}

func (b fakeBackend) IterateFunctionish(_ context.Context, _ []string, fn func(*knowledgev1.Node) error) error {
	for i := range b.nodes {
		if err := fn(b.nodes[i]); err != nil {
			return err
		}
	}
	return nil
}

func TestHydrate_EmptyResultsHint(t *testing.T) {
	out, err := Hydrate(context.Background(), fakeBackend{}, nil, WalkStats{})
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(out.Matches) != 0 {
		t.Errorf("Matches = %d, want 0", len(out.Matches))
	}
	if out.Hint == "" {
		t.Error("Hint must be populated when Matches is empty")
	}
}

func TestHydrate_EnclosingNodeResolved(t *testing.T) {
	backend := fakeBackend{
		nodes: []*knowledgev1.Node{
			{
				Id:        "node-1",
				FilePath:  "main.go",
				StartLine: 5,
				EndLine:   12,
				Signature: "func MyFunc() error",
			},
		},
	}
	raws := []RawMatch{
		{
			FilePath:  "main.go",
			StartLine: 8,
			EndLine:   8,
			Captures: map[string]Capture{
				"X": {Text: "x", Kind: "identifier", Line: 8},
			},
		},
	}
	out, err := Hydrate(context.Background(), backend, raws, WalkStats{FilesScanned: 1})
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(out.Matches) != 1 {
		t.Fatalf("Matches = %d, want 1", len(out.Matches))
	}
	m := out.Matches[0]
	if m.EnclosingNodeID != "node-1" {
		t.Errorf("EnclosingNodeID = %q, want node-1", m.EnclosingNodeID)
	}
	if m.EnclosingSignature != "func MyFunc() error" {
		t.Errorf("EnclosingSignature = %q, want func MyFunc() error", m.EnclosingSignature)
	}
	// Verify the new Capture fields pass through unchanged.
	cap := m.Captures["X"]
	if cap.Kind != "identifier" {
		t.Errorf("Capture.Kind = %q, want identifier", cap.Kind)
	}
}

func TestHydrate_NoEnclosingFoundLeavesEmpty(t *testing.T) {
	backend := fakeBackend{} // no nodes
	raws := []RawMatch{
		{FilePath: "main.go", StartLine: 8, EndLine: 8, Captures: map[string]Capture{}},
	}
	out, err := Hydrate(context.Background(), backend, raws, WalkStats{})
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(out.Matches) != 1 {
		t.Fatalf("Matches = %d, want 1", len(out.Matches))
	}
	if out.Matches[0].EnclosingNodeID != "" {
		t.Errorf("EnclosingNodeID = %q, want empty", out.Matches[0].EnclosingNodeID)
	}
}

func TestHydrate_NilBackendIsNoOp(t *testing.T) {
	raws := []RawMatch{
		{FilePath: "main.go", StartLine: 1, EndLine: 1, Captures: map[string]Capture{}},
	}
	out, err := Hydrate(context.Background(), nil, raws, WalkStats{})
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(out.Matches) != 1 {
		t.Errorf("Matches = %d, want 1", len(out.Matches))
	}
	if out.Matches[0].EnclosingNodeID != "" {
		t.Errorf("EnclosingNodeID = %q, want empty (NoOpBackend)", out.Matches[0].EnclosingNodeID)
	}
}

func TestBuildCodeNodeIndex_TightestEnclosingPicked(t *testing.T) {
	backend := fakeBackend{
		nodes: []*knowledgev1.Node{
			{Id: "outer", FilePath: "main.go", StartLine: 1, EndLine: 100},
			{Id: "inner", FilePath: "main.go", StartLine: 50, EndLine: 60},
		},
	}
	idx, err := buildCodeNodeIndex(context.Background(), backend, []string{"main.go"})
	if err != nil {
		t.Fatalf("buildCodeNodeIndex: %v", err)
	}
	got, ok := idx.lookup("main.go", 55)
	if !ok {
		t.Fatal("lookup(55) ok=false; want true")
	}
	if got.Id != "inner" {
		t.Errorf("ID = %q, want inner (tightest enclosing)", got.Id)
	}
}

func TestLookup_OffByOneTolerance(t *testing.T) {
	backend := fakeBackend{
		nodes: []*knowledgev1.Node{
			{Id: "n", FilePath: "main.go", StartLine: 10, EndLine: 20},
		},
	}
	idx, err := buildCodeNodeIndex(context.Background(), backend, []string{"main.go"})
	if err != nil {
		t.Fatalf("buildCodeNodeIndex: %v", err)
	}
	// Line 9 should match via -1 tolerance.
	if _, ok := idx.lookup("main.go", 9); !ok {
		t.Error("lookup(9): off-by-one tolerance failed")
	}
	// Line 21 should match via +1 tolerance.
	if _, ok := idx.lookup("main.go", 21); !ok {
		t.Error("lookup(21): off-by-one tolerance failed")
	}
	// Line 22 must NOT match.
	if _, ok := idx.lookup("main.go", 22); ok {
		t.Error("lookup(22): out of range, but matched")
	}
}

func TestLookup_UnknownFileReturnsFalse(t *testing.T) {
	idx := &codeNodeIndex{byFile: map[string][]rangeEntry{}}
	if _, ok := idx.lookup("unknown.go", 1); ok {
		t.Error("lookup on unknown file = true; want false")
	}
}

// TestZeroScanHint pins the wrong-root hint format: it names the walked
// directory, the language, and carries the "wrong root" signal so the LLM can
// tell a zero-files-scanned walk from a scanned-but-no-match one.
func TestZeroScanHint(t *testing.T) {
	got := ZeroScanHint("/tmp/foo", "go")
	want := "walked /tmp/foo: no go files found — wrong root? pass repo:<name|/abs/path> or check --root"
	if got != want {
		t.Errorf("ZeroScanHint = %q, want %q", got, want)
	}
}
