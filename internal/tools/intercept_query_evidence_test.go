// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// seedEvidenceFixture builds the in-memory fixture matching the
// evidence golden: decision → 2 findings (informed-by) → 1 reference
// each (references).
func seedEvidenceFixture() (*parityGraphFixture, string) {
	f := newParityFixture()
	decisionID := "00000000000000000000000000000ce0"
	dec := knowledgev1.Node{
		Id: decisionID, Type: string(kgtypes.NodeDecision),
		SymbolName: "cap-evidence-decision", Status: "active",
		Summary: "decision sum",
	}
	kgtypes.SetValue(&dec, "choice", "evidence choice")
	kgtypes.SetValue(&dec, "rationale", "evidence rationale")
	kgtypes.SetValue(&dec, "alternatives", "alt-x, alt-y")
	f.add(&dec)

	for i, name := range []string{"finding-1", "finding-2"} {
		fid := "00000000000000000000000000000ef" + string(rune('1'+i))
		f.add(&knowledgev1.Node{
			Id: fid, Type: string(kgtypes.NodeFinding), SymbolName: name,
			Status: "active", Description: "fdesc " + name,
			Summary: "fsum " + name,
		})
		f.edges = append(f.edges, &knowledgev1.Edge{FromId: decisionID, ToId: fid, Type: string(kgtypes.EdgeInformedBy)})

		ridx := "00000000000000000000000000000eb" + string(rune('1'+i))
		rn := knowledgev1.Node{
			Id: ridx, Type: string(kgtypes.NodeReference), SymbolName: "ref for " + name,
			Status: "active", Summary: "ref sum",
		}
		kgtypes.SetValue(&rn, "type", "url")
		kgtypes.SetValue(&rn, "url", "https://example.invalid/"+name)
		f.add(&rn)
		f.edges = append(f.edges, &knowledgev1.Edge{FromId: fid, ToId: ridx, Type: string(kgtypes.EdgeReferences)})
	}

	return f, decisionID
}

func TestInterceptQueryEvidence_TextFormat_ByteIdentical(t *testing.T) {
	f, decID := seedEvidenceFixture()
	deps := &parityDeps{gc: f.gc()}
	args := mustMarshal(t, map[string]any{"mode": "evidence", "id": decID})

	handled, res := InterceptQueryEvidence(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept error: %v", res.Content)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "evidence")
	assert.Equal(t, want, got)
}

func TestInterceptQueryEvidence_JSONFormat_ByteIdentical(t *testing.T) {
	f, decID := seedEvidenceFixture()
	deps := &parityDeps{gc: f.gc()}
	args := mustMarshal(t, map[string]any{"mode": "evidence", "id": decID, "format": "json"})

	handled, res := InterceptQueryEvidence(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "evidence.json")
	assert.Equal(t, want, got)
}

func TestInterceptQueryEvidence_MissingID_Errors(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "evidence"})
	handled, res := InterceptQueryEvidence(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, extractText(res), "evidence mode requires 'id' parameter")
}

func TestInterceptQueryEvidence_WrongTool_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "evidence", "id": "x"})
	handled, _ := InterceptQueryEvidence(opCtx(), deps, kgtools.CallToolParams{Name: "search", Arguments: args})
	assert.False(t, handled)
}

func TestInterceptQueryEvidence_WrongMode_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "plan_tree", "id": "x"})
	handled, _ := InterceptQueryEvidence(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled)
}
