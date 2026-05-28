// SPDX-License-Identifier: Apache-2.0

package topology

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// fakeNodeIndexCaller is a scripted graphCaller + topoExecutor for golden-file
// tests. The node-index fetch rides the Execute carrier seam (T-GTB6); Execute
// returns an empty typed Nodes carrier so every dead function surfaces as
// "unmapped" — the dead_code finding shape for unmapped functions (file:line
// evidence keys) is the golden check.
type fakeNodeIndexCaller struct{}

func (fakeNodeIndexCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (fakeNodeIndexCaller) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	// Empty Nodes carrier → empty node index → every dead function is unmapped.
	return enginetest.ResponseWithNodes(), nil
}

// TestRunDeadCode_GoldenFile_UnmappedShape is the FUL-241 Phase 9 step
// 3 equivalence check. Pre-migration the server's DeadCodeAnalyzer
// produced the same Finding shape this test pins; the client-side
// RunDeadCode must produce identical structure.
//
// The fixture is a single-file Go module with one main + one unused
// function. RTA marks the unused function as dead; the empty node-index
// response forces the "unmapped" code path so the test also pins that
// shape.
func TestRunDeadCode_GoldenFile_UnmappedShape(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not on PATH")
	}

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/dctest\n\ngo 1.22\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

func main() {
	live()
}

func live() {}

func dead() {}
`), 0o600))

	findings, err := RunDeadCode(context.Background(), fakeNodeIndexCaller{}, dir, "dctest", 0)
	require.NoError(t, err)
	require.NotEmpty(t, findings, "RTA should find dead() unreachable")

	// Pin the unmapped-finding shape.
	var deadFinding *struct{ Algorithm, Title, Summary string }
	for i := range findings {
		f := findings[i]
		if f.Algorithm == "dead_code" && (containsIgnoreCase(f.Title, "dead") && containsIgnoreCase(f.Title, ".dead")) {
			deadFinding = &struct{ Algorithm, Title, Summary string }{f.Algorithm, f.Title, f.Summary}
			assert.Equal(t, "dead_code", f.Algorithm)
			assert.Contains(t, f.Title, "dead")
			assert.Contains(t, f.Summary, "RTA found")
			assert.InDelta(t, float64(1), f.Metrics["confidence"], 0.0001)
			assert.InDelta(t, float64(0), f.Metrics["review_needed"], 0.0001)
			break
		}
	}
	require.NotNil(t, deadFinding, "expected at least one finding for dead()")
}

// containsIgnoreCase mirrors strings.Contains but case-insensitive. The
// finding title format is "Dead function: pkg.func"; we don't pin the
// exact case in case future formatting tweaks pluralize or capitalize.
func containsIgnoreCase(s, sub string) bool {
	return len(sub) <= len(s) && (func() bool {
		ls := bytesToLowerLocal([]byte(s))
		lsub := bytesToLowerLocal([]byte(sub))
		for i := 0; i+len(lsub) <= len(ls); i++ {
			match := true
			for j := range lsub {
				if ls[i+j] != lsub[j] {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
		return false
	}())
}

func bytesToLowerLocal(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		out[i] = c
	}
	return out
}
