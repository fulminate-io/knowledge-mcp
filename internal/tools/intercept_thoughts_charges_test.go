// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// seededChargesFixture returns a ctxCaller seeding thought
// 7c1f0e5bdddddddddddddddddddddddd with one positive
// charge c1, joined by an EdgeChargedBy edge — exactly the two reads
// fetchChargesFor issues (ONE EdgeChargedBy edges read + ONE bulk node hydrate).
func seededChargesFixture() *ctxCaller {
	charge := &knowledgev1.Node{Id: "c1", Type: string(kgtypes.NodeCharge)}
	kgtypes.SetValue(charge, "polarity", "positive")
	kgtypes.SetValue(charge, "weight", "7")
	return &ctxCaller{
		nodesByID: nodesByID(charge),
		chargeEdges: []*knowledgev1.Edge{
			{Type: string(kgtypes.EdgeChargedBy), FromId: "7c1f0e5bdddddddddddddddddddddddd", ToId: "c1"},
		},
	}
}

func callChargesFor(t *testing.T, deps ClientDeps, args map[string]any) (bool, kgtools.ToolResult) {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return InterceptThoughts(opCtx(), deps, kgtools.CallToolParams{Name: "thoughts", Arguments: raw})
}

// TestChargesFor_DispatchClaimed is the core QA repro: thoughts(charges_for)
// must be CLAIMED client-side, not fall through to the engine deny.
func TestChargesFor_DispatchClaimed(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededChargesFixture()}
	handled, res := callChargesFor(t, deps, map[string]any{"operation": "charges_for", "thought_ids": []string{"7c1f0e5bdddddddddddddddddddddddd"}})
	require.True(t, handled, "thoughts(charges_for) must be claimed by the client intercept")
	assert.False(t, res.IsError, "a valid charges_for must not error: %s", toolResultText(res))
	assert.NotContains(t, toolResultText(res), "is not a recognized engine-reducible shape",
		"charges_for must not fall through to the engine deny")
}

// TestChargesFor_RequiresThoughtIDs: an empty/absent thought_ids is a loud error.
func TestChargesFor_RequiresThoughtIDs(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededChargesFixture()}
	handled, res := callChargesFor(t, deps, map[string]any{"operation": "charges_for"})
	require.True(t, handled, "charges_for is still claimed even when thought_ids is missing")
	assert.True(t, res.IsError, "missing thought_ids must be a loud error")
	assert.Contains(t, toolResultText(res), "thought_ids is required")
}

// chargesForJSON is the decoded shape of the json render arm.
type chargesForJSON struct {
	ChargesByThought map[string][]knowledgev1.Node `json:"charges_by_thought"`
}

// TestChargesFor_JSONShape: format=json maps the thought id to its one-element
// charge node array, carrying the charge id and polarity.
func TestChargesFor_JSONShape(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededChargesFixture()}
	handled, res := callChargesFor(t, deps, map[string]any{
		"operation": "charges_for", "thought_ids": []string{"7c1f0e5bdddddddddddddddddddddddd"}, "format": "json",
	})
	require.True(t, handled)
	require.False(t, res.IsError, "json charges_for errored: %s", toolResultText(res))

	var got chargesForJSON
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &got))
	charges := got.ChargesByThought["7c1f0e5bdddddddddddddddddddddddd"]
	require.Len(t, charges, 1, "the resolved id must map to its one seeded charge")
	assert.Equal(t, "c1", charges[0].GetId(), "the charge node id must be carried")
	assert.Equal(t, "positive", kgtypes.Value(&charges[0], "polarity"), "the charge polarity must be carried")
}

// chargesForResolveFake seeds the prefix-resolution fixture on fakeGraphCaller.
// ctxCaller cannot serve these: it has no ById arm at all, so the resolution
// probe would always miss.
func chargesForResolveFake(t *testing.T, seedPrefix bool) *fakeGraphCaller {
	t.Helper()
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"c1": nodeResultJSON(t, "c1", "charge", map[string]string{"polarity": "positive", "weight": "7"}),
		},
		edgesByID: map[string][]*knowledgev1.Edge{
			chargeFullID: {{Type: string(kgtypes.EdgeChargedBy), FromId: chargeFullID, ToId: "c1"}},
		},
	}
	if seedPrefix {
		fc.queryResponses[chargePrefix] = nodeResultJSON(t, chargeFullID, "thought", nil)
	}
	return fc
}

// TestChargesFor_PrefixResolves covers BOTH render arms, which is the whole
// point: rewiring only the fetch would leave the DEFAULT text arm printing the
// caller's prefix against a zero count — this defect reproduced inside its fix.
func TestChargesFor_PrefixResolves(t *testing.T) {
	t.Run("json arm keys the map by the RESOLVED full id", func(t *testing.T) {
		deps := interceptTestDeps{gc: chargesForResolveFake(t, true)}
		handled, res := callChargesFor(t, deps, map[string]any{
			"operation": "charges_for", "thought_ids": []string{chargePrefix}, "format": "json",
		})
		require.True(t, handled)
		require.False(t, res.IsError, "prefix charges_for errored: %s", toolResultText(res))

		var got chargesForJSON
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &got))
		require.Len(t, got.ChargesByThought, 1, "exactly one key")
		charges, ok := got.ChargesByThought[chargeFullID]
		require.True(t, ok, "the map must be keyed by the FULL id, not the prefix")
		require.Len(t, charges, 1)
		assert.Equal(t, "c1", charges[0].GetId())
	})

	t.Run("default text arm lists the resolved id, not the prefix", func(t *testing.T) {
		deps := interceptTestDeps{gc: chargesForResolveFake(t, true)}
		handled, res := callChargesFor(t, deps, map[string]any{
			"operation": "charges_for", "thought_ids": []string{chargePrefix},
		})
		require.True(t, handled)
		require.False(t, res.IsError, "prefix charges_for errored: %s", toolResultText(res))
		body := toolResultText(res)
		assert.Contains(t, body, chargeFullID+": 1 charge(s)")
		assert.NotContains(t, body, chargePrefix+": 0 charge(s)", "the reported symptom must be gone")
	})
}

// TestChargesFor_AmbiguousPrefixErrors relays the server's candidate list rather
// than swallowing it, and reads no charges.
func TestChargesFor_AmbiguousPrefixErrors(t *testing.T) {
	fc := chargesForResolveFake(t, false)
	fc.queryErrors = map[string]error{
		chargePrefix: errors.New("ambiguous id prefix \"8e30f608\" matches multiple nodes:\n" +
			"  8e30f608aaaaaaaaaaaaaaaaaaaaaaaa  thought  A\n" +
			"  8e30f608bbbbbbbbbbbbbbbbbbbbbbbb  thought  B"),
	}
	_, res := callChargesFor(t, interceptTestDeps{gc: fc}, map[string]any{
		"operation": "charges_for", "thought_ids": []string{chargePrefix},
	})
	body := toolResultText(res)
	assert.True(t, res.IsError, "an ambiguous prefix must be a loud error")
	assert.Contains(t, body, "8e30f608aaaaaaaaaaaaaaaaaaaaaaaa")
	assert.Contains(t, body, "8e30f608bbbbbbbbbbbbbbbbbbbbbbbb")
	assert.NotContains(t, body, "c1", "no charges may be read on an unresolvable id")
}

// TestChargesFor_MissingPrefixErrors names the id rather than silently skipping
// it — a silently-skipped unresolvable id is the defect this ticket removes.
func TestChargesFor_MissingPrefixErrors(t *testing.T) {
	const missing = "deadbeefdeadbeef"
	fc := chargesForResolveFake(t, false)
	fc.queryErrors = map[string]error{missing: errors.New("node deadbeefdeadbeef not found")}
	_, res := callChargesFor(t, interceptTestDeps{gc: fc}, map[string]any{
		"operation": "charges_for", "thought_ids": []string{missing},
	})
	assert.True(t, res.IsError, "a missing id must be a loud error")
	assert.Contains(t, toolResultText(res), missing, "the message names the id")
}

// TestChargesFor_FullIDSkipsResolve is the bounded-cost guard: a full 32-char id
// must cost ZERO resolution reads. Without the verbatim fast path this is 1.
func TestChargesFor_FullIDSkipsResolve(t *testing.T) {
	fc := chargesForResolveFake(t, false)
	_, res := callChargesFor(t, interceptTestDeps{gc: fc}, map[string]any{
		"operation": "charges_for", "thought_ids": []string{chargeFullID},
	})
	require.False(t, res.IsError, "full-id charges_for errored: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), chargeFullID+": 1 charge(s)")

	// Execute records one query call per ById read; plural reads record id "".
	byIDReads := 0
	for _, c := range fc.calls {
		if c.tool == "query" && strings.Contains(string(c.args), `"id":"`+chargeFullID+`"`) {
			byIDReads++
		}
	}
	assert.Zero(t, byIDReads, "a full 32-char id must skip resolution entirely")
}

// TestChargesFor_PrefixReadBackAgrees is the end-to-end loop this ticket exists
// for: charge with an 8-char prefix, then read charges_for with the SAME prefix,
// and assert both speak the same canonical id.
func TestChargesFor_PrefixReadBackAgrees(t *testing.T) {
	fc := prefixChargeFake(t)
	deps := interceptTestDeps{gc: fc}

	chargeRes := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name: "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"` + chargePrefix + `",` +
			`"polarity":"positive","weight":2.0,"reasoning":"read-back"}`),
	})
	require.False(t, chargeRes.IsError, "the charge must succeed: %s", toolResultText(chargeRes))
	assert.Contains(t, toolResultText(chargeRes), "Charged: "+chargeFullID+" (thought)")

	_, readRes := callChargesFor(t, deps, map[string]any{
		"operation": "charges_for", "thought_ids": []string{chargePrefix}, "format": "json",
	})
	require.False(t, readRes.IsError, "the read-back must succeed: %s", toolResultText(readRes))

	var got chargesForJSON
	require.NoError(t, json.Unmarshal([]byte(toolResultText(readRes)), &got))
	charges, ok := got.ChargesByThought[chargeFullID]
	require.True(t, ok,
		"charges_for must key on the SAME full id the charge response printed on its Charged: line")
	require.Len(t, charges, 1)
	assert.Equal(t, "c1", charges[0].GetId(), "both hops agree on the charge")
}
