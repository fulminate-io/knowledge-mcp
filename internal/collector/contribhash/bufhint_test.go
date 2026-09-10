// SPDX-License-Identifier: Apache-2.0

package contribhash

import (
	"math"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

func TestBufHint(t *testing.T) {
	cases := []struct {
		name  string
		parts []int
		want  int
	}{
		{"no parts is zero", nil, 0},
		{"an ordinary sum is exact", []int{10, 20, 30}, 60},
		{"zero parts contribute nothing", []int{0, 0, 7}, 7},
		{"the ceiling itself is admitted", []int{maxBufHint}, maxBufHint},
		{"one part over the ceiling saturates", []int{maxBufHint + 1}, maxBufHint},
		{"a sum crossing the ceiling saturates", []int{maxBufHint - 1, 2}, maxBufHint},
		{"a part that would wrap int saturates", []int{1, math.MaxInt}, maxBufHint},
		{"two parts that would wrap int saturate", []int{math.MaxInt, math.MaxInt}, maxBufHint},
		{"a negative part saturates rather than shrinking", []int{10, -1}, maxBufHint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bufHint(tc.parts...); got != tc.want {
				t.Errorf("bufHint(%v) = %d, want %d", tc.parts, got, tc.want)
			}
		})
	}
}

// TestBufHintNeverWraps is the property the bound exists for: whatever parts
// are handed in, the result is a legal make() capacity — non-negative and no
// larger than the ceiling. A wrapping sum would produce a negative here and
// panic the allocator at the call site instead.
func TestBufHintNeverWraps(t *testing.T) {
	parts := [][]int{
		{math.MaxInt, 1},
		{math.MaxInt / 2, math.MaxInt / 2, math.MaxInt / 2},
		{maxBufHint, maxBufHint, maxBufHint},
		{math.MinInt},
	}
	for _, p := range parts {
		got := bufHint(p...)
		if got < 0 || got > maxBufHint {
			t.Errorf("bufHint(%v) = %d, outside [0, %d] — not a legal make capacity", p, got, maxBufHint)
		}
	}
}

// TestNodeBufHintIsStillExactBelowTheCeiling proves the bound did not perturb
// ordinary sizing: for a node nowhere near the ceiling, the hint is the same
// sum it always was. The expectation is computed here from the field lengths
// rather than read back from the function, so this is an external answer key.
//
// THE DIGESTS THEMSELVES are guarded by the golden vectors in vector_test.go,
// which pin the encoding against testdata rather than against this package.
func TestNodeBufHintIsStillExactBelowTheCeiling(t *testing.T) {
	const framingPerField = 5
	const nodeFieldCount = 14
	body := strings.Repeat("x", 4096)
	n := &knowledgev1.Node{
		Type: "file", SymbolName: "sym", FilePath: "a/b.go", Language: "go",
		Content: body, Signature: "func()", TestKind: "", Description: "d",
		Source: "src", Status: "ok",
	}
	want := len(n.GetType()) + len(n.GetSymbolName()) + len(n.GetFilePath()) +
		len(n.GetLanguage()) + len(n.GetContent()) + len(n.GetSignature()) +
		len(n.GetTestKind()) + len(n.GetDescription()) + len(n.GetSource()) +
		len(n.GetStatus()) + nodeFieldCount*framingPerField + 32
	if got := nodeBufHint(n); got != want {
		t.Errorf("nodeBufHint = %d, want the exact sum %d", got, want)
	}

	// The hashes still compute over an oversized field, and are stable call to
	// call — the bound is on the hint, never on what is hashed.
	e := kgwire.BatchEdge{FromID: "a", ToID: "b", Type: "CALLS", Method: "m", Evidence: body}
	if EdgeContributionHash(e) != EdgeContributionHash(e) {
		t.Error("EdgeContributionHash is not stable across calls")
	}
	if NodeContributionHash(n) != NodeContributionHash(n) {
		t.Error("NodeContributionHash is not stable across calls")
	}
}
