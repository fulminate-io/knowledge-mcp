// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

func TestHandleChargeClient_EmptyThought_Errors(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","polarity":"positive","weight":1.0}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "charge requires 'thought'")
}

func TestHandleChargeClient_BadPolarity_Errors(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"t-1","polarity":"sideways","weight":1.0}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "polarity must be 'positive' or 'negative'")
}

func TestHandleChargeClient_NonChargeableTarget_Rejected(t *testing.T) {
	// The parent verify resolves a non-chargeable node (document) → rejected with
	// the must-be-one-of-thought/finding/research message. A decision/document/
	// other type is NOT a chargeable claim node.
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"doc-1": nodeResultJSON(t, "doc-1", "document", nil),
	}}
	deps := interceptTestDeps{gc: fc}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"doc-1","polarity":"positive","weight":1.0,"reasoning":"r"}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), `charge target doc-1 is type "document", must be one of thought/finding/research`)
}

// TestHandleChargeClient_FindingTarget_Accepted proves the relaxed gate: a charge
// against a FINDING node succeeds (no IsError, charge id rendered). Mirrors the
// LowersToCreateBatch success-path scaffolding — seeded mutateIDs + nil GraphClient
// (interceptTestDeps wires no GraphClient) → the bare-ID tail.
func TestHandleChargeClient_FindingTarget_Accepted(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"f-1": nodeResultJSON(t, "f-1", "finding", nil),
		},
		mutateIDs: []string{"charge-f"},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"f-1","polarity":"positive","weight":2.0,"reasoning":"a finding can be charged"}`),
	})
	require.False(t, res.IsError, "charging a finding should succeed: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "Charge recorded → ID: charge-f")
}

// TestHandleChargeClient_ResearchTarget_Accepted is the research analog: a charge
// against a RESEARCH node succeeds under the relaxed gate.
func TestHandleChargeClient_ResearchTarget_Accepted(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"r-1": nodeResultJSON(t, "r-1", "research", nil),
		},
		mutateIDs: []string{"charge-r"},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"r-1","polarity":"negative","weight":3.0,"reasoning":"a research question can be charged"}`),
	})
	require.False(t, res.IsError, "charging a research node should succeed: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "Charge recorded → ID: charge-r")
}

func TestHandleChargeClient_MissingTarget_NotFound(t *testing.T) {
	fc := &fakeGraphCaller{} // no seeded node → parent verify misses.
	deps := interceptTestDeps{gc: fc}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"missing","polarity":"positive","weight":1.0,"reasoning":"r"}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "thought missing not found")
}

// TestHandleChargeClient_LowersToCreateBatch covers the composer happy path: a
// valid charge against a NodeThought parent lowers to a CREATE MutationPlan with
// the charge NodeBody (type=charge, SymbolName=truncated reasoning, polarity +
// weight metadata) + EdgeChargedBy (thought→charge) + EdgeEvidencedBy
// (charge→evidence). GraphClient is nil (test affordance) → the bare-ID tail.
func TestHandleChargeClient_LowersToCreateBatch(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"th-1": nodeResultJSON(t, "th-1", "thought", nil),
			// Evidence ev-1 resolves in knowledge → raw id (no proxy).
			"ev-1": nodeResultJSON(t, "ev-1", "finding", nil),
		},
		mutateIDs: []string{"charge-1"},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"th-1","polarity":"positive","weight":3.0,"reasoning":"because the test says so","evidence":["ev-1"]}`),
	})
	require.False(t, res.IsError, "charge should succeed: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "Charge recorded → ID: charge-1")

	// Exactly one CREATE MutationPlan: charge NodeBody + 2 edges.
	require.Len(t, fc.execMutations, 1)
	m := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, m.GetKind())
	require.Len(t, m.GetNodeBodies(), 1)
	body := m.GetNodeBodies()[0]
	assert.Equal(t, "charge", body.GetType())
	assert.Equal(t, "because the test says so", body.GetName(), "SymbolName=reasoning (under maxLen, untruncated)")
	assert.Equal(t, "positive", body.GetMetadata()["polarity"])
	assert.Equal(t, "3.00", body.GetMetadata()["weight"])
	assert.Equal(t, "because the test says so", body.GetContent())

	// Edge directions: EdgeChargedBy th-1→charge(slot0); EdgeEvidencedBy charge(slot0)→ev-1.
	edges := m.GetEdges()
	require.Len(t, edges, 2)
	assert.Equal(t, "th-1", edges[0].GetFromId())
	assert.Equal(t, int32(0), edges[0].GetToIdx())
	assert.Equal(t, string(kgtypes.EdgeChargedBy), edges[0].GetType())
	assert.Equal(t, int32(0), edges[1].GetFromIdx())
	assert.Equal(t, "ev-1", edges[1].GetToId())
	assert.Equal(t, string(kgtypes.EdgeEvidencedBy), edges[1].GetType())
}

// TestHandleChargeClient_NoHitEvidence_RawIDPreserved covers ResolveOrProxy
// outcome (c): an evidence id found in NEITHER knowledge NOR any foreign graph
// is emitted as the raw id on the EdgeEvidencedBy edge (NOT dropped).
func TestHandleChargeClient_NoHitEvidence_RawIDPreserved(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"th-1": nodeResultJSON(t, "th-1", "thought", nil),
			// dangling-ev is NOT seeded anywhere → no-hit → raw id preserved.
		},
		mutateIDs: []string{"charge-2"},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"th-1","polarity":"negative","weight":1.0,"reasoning":"r","evidence":["dangling-ev"]}`),
	})
	require.False(t, res.IsError, "charge should succeed: %s", toolResultText(res))
	require.Len(t, fc.execMutations, 1)
	edges := fc.execMutations[0].GetEdges()
	require.Len(t, edges, 2, "EdgeChargedBy + the dangling EdgeEvidencedBy (no-hit ID NOT dropped)")
	assert.Equal(t, "dangling-ev", edges[1].GetToId(), "no-hit evidence id preserved as raw (outcome c)")
}

// TestHandleChargeClient_ThoughtEvidence_ResolvesToDirectEdge locks the
// charge-evidence resolution behavior: a charge citing a THOUGHT ID as
// evidence resolves via outcome (a) (knowledge-hit → raw id) and lands a direct
// EdgeEvidencedBy edge targeting the thought node — no proxy materialization, no
// drop. A thought is a knowledge-graph node, so resolveCrossGraphID returns the
// raw id at intercept_thoughts_charge.go:193-195; this test guards against a
// future NodeThought guard that would proxy or drop thought evidence.
func TestHandleChargeClient_ThoughtEvidence_ResolvesToDirectEdge(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"th-1": nodeResultJSON(t, "th-1", "thought", nil), // the charge parent.
			// The EVIDENCE thought resolves in knowledge → raw id (no proxy).
			"eth-1": nodeResultJSON(t, "eth-1", "thought", nil),
		},
		mutateIDs: []string{"charge-3"},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"th-1","polarity":"positive","weight":4.0,"reasoning":"a related thought confirms this","evidence":["eth-1"]}`),
	})
	require.False(t, res.IsError, "charge should succeed: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 1)
	m := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, m.GetKind())

	// EdgeChargedBy th-1→charge(slot0); EdgeEvidencedBy charge(slot0)→eth-1 (the
	// thought node directly — knowledge-hit outcome a, no proxy, no drop).
	edges := m.GetEdges()
	require.Len(t, edges, 2)
	assert.Equal(t, int32(0), edges[1].GetFromIdx(), "evidenced-by originates at the charge slot")
	assert.Equal(t, "eth-1", edges[1].GetToId(), "evidenced-by targets the thought node directly (raw id)")
	assert.Equal(t, string(kgtypes.EdgeEvidencedBy), edges[1].GetType())
}

// TestTruncateAtWordCreate_RuneCorrect proves the helper counts and slices by
// RUNES, not bytes: a multibyte input over maxLen is capped to exactly maxLen
// runes and is always valid UTF-8 (a byte cap would slice mid-rune), while
// ASCII word-boundary behavior is preserved and sub-cap input is unchanged.
func TestTruncateAtWordCreate_RuneCorrect(t *testing.T) {
	// Multibyte over cap: 300 em-dashes (U+2014, 3 bytes / 1 rune each), no
	// spaces → idx<=0 path → exactly maxLen runes of valid UTF-8.
	const maxLen = 60
	emdashes := strings.Repeat("—", 300)
	got := truncateAtWordCreate(emdashes, maxLen)
	assert.Equal(t, maxLen, utf8.RuneCountInString(got), "capped to maxLen RUNES, not bytes")
	assert.True(t, utf8.ValidString(got), "result must be valid UTF-8 (byte cap would slice mid-rune)")

	// Sub-cap input is returned unchanged.
	short := "hello world"
	assert.Equal(t, short, truncateAtWordCreate(short, maxLen), "sub-cap input unchanged")

	// ASCII word-boundary preserved: cut at the last space within the cap.
	ascii := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda"
	gotASCII := truncateAtWordCreate(ascii, 20)
	assert.LessOrEqual(t, utf8.RuneCountInString(gotASCII), 20)
	assert.False(t, strings.HasSuffix(gotASCII, " "), "no trailing space")
	assert.True(t, strings.HasPrefix(ascii, gotASCII), "word-boundary cut is a prefix of the input")
	assert.NotContains(t, gotASCII[strings.LastIndex(gotASCII, " ")+1:], " ")

	// SummaryMaxLen multibyte path: a summary of em-dashes longer than
	// SummaryMaxLen runes caps to SummaryMaxLen runes of valid UTF-8.
	bigSummary := strings.Repeat("—", validate.SummaryMaxLen+100)
	gotSummary := truncateAtWordCreate(bigSummary, validate.SummaryMaxLen)
	assert.Equal(t, validate.SummaryMaxLen, utf8.RuneCountInString(gotSummary))
	assert.True(t, utf8.ValidString(gotSummary))
}
