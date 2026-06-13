// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// sessionCreateBody returns the first CREATE MutationPlan NodeBody whose type is
// thought_session across all recorded mutations, or nil if the resolver attached
// to an existing session and minted none.
func sessionCreateBody(fc *backfillFakeCaller) *knowledgev1.NodeBody {
	for _, m := range fc.mutations {
		if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			continue
		}
		for _, b := range m.GetNodeBodies() {
			if b.GetType() == string(kgtypes.NodeThoughtSession) {
				return b
			}
		}
	}
	return nil
}

// thoughtCreatePlan returns the CREATE MutationPlan carrying the thought NodeBody
// (the plan that also carries the session→thought EdgeKGContains batch edge).
func thoughtCreatePlan(fc *backfillFakeCaller) *knowledgev1.MutationPlan {
	for _, m := range fc.mutations {
		if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			continue
		}
		for _, b := range m.GetNodeBodies() {
			if b.GetType() == string(kgtypes.NodeThought) {
				return m
			}
		}
	}
	return nil
}

// seedSessions builds n NodeThoughtSession nodes with distinct names and
// id-ordered ids, so the harness's symbol_name-EQ filter and the resolver's
// lowest-id tie-break have a deterministic backing set to operate over.
func seedSessions(n int) []*knowledgev1.Node {
	out := make([]*knowledgev1.Node, 0, n)
	for i := range n {
		out = append(out, &knowledgev1.Node{
			Id:         fmt.Sprintf("sess-%03d", i),
			Type:       string(kgtypes.NodeThoughtSession),
			SymbolName: fmt.Sprintf("session-%03d", i),
		})
	}
	return out
}

// TestHandleThinkClient_SessionByPredicate_NoDuplicate proves the bounded
// symbol_name-EQ predicate browse FINDS a session that the OLD limit:0 capped
// first page would have missed: with 25 sessions seeded, a think into the target
// name attaches to the EXISTING session (no duplicate CREATE), the thought's
// containment edge points at the seeded target, and the resolve is a SINGLE
// bounded wire read.
func TestHandleThinkClient_SessionByPredicate_NoDuplicate(t *testing.T) {
	sessions := seedSessions(25)
	// The target sits past the old 10-row first page (index 20).
	target := sessions[20]
	target.SymbolName = "beyond-old-page"

	fc := &backfillFakeCaller{
		sessions:  sessions,
		mutateIDs: []string{"th-new"},
	}
	deps := interceptTestDeps{gc: fc}

	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"think","content":"a thought beyond the page","summary":"beyond-page thought gist","session":"beyond-old-page"}`),
	})
	require.False(t, res.IsError, "predicate-resolved think should succeed: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "Session: beyond-old-page")

	// No duplicate session CREATE — the existing session was found via the
	// predicate browse, not re-minted.
	assert.Nil(t, sessionCreateBody(fc),
		"NO thought_session CREATE: the existing session must be found, not duplicated")

	// The thought's containment edge originates from the SEEDED target session id.
	createPlan := thoughtCreatePlan(fc)
	require.NotNil(t, createPlan, "a thought CREATE plan must exist")
	assert.True(t, hasContainsFrom(createPlan, target.GetId()),
		"the thought must attach to the seeded target session id %s", target.GetId())

	// ONE bounded SESSION predicate read — the must-not-multiply-reads guarantee
	// AND the fails-when-absent for the predicate design (a reversion to the
	// unfiltered capped browse misses the target → duplicate CREATE → the nil
	// assertion flips; a reversion to a paged drain → >1 session browse). Scoped to
	// the session browse so the unrelated origin/agent resolution read is excluded.
	assert.Equal(t, 1, fc.sessionBrowseCalls, "the bounded session predicate browse resolves in ONE wire read")
}

// TestHandleThinkClient_SameNameCollision_LowestIDWins is the determinism guard:
// two sessions share a name (returned together by the predicate browse) and the
// resolver must attach to the LOWER id deterministically (sort.Strings/ids[0]),
// never the first-encountered browse-order match.
func TestHandleThinkClient_SameNameCollision_LowestIDWins(t *testing.T) {
	lowerID, higherID := "00aa-lower", "ff99-higher"
	// Seed the higher id FIRST so a non-sorted first-encountered resolve would
	// (wrongly) pick it — the assertion below flips red if the sort is dropped.
	sessions := []*knowledgev1.Node{
		{Id: higherID, Type: string(kgtypes.NodeThoughtSession), SymbolName: "dup-name"},
		{Id: "other-1", Type: string(kgtypes.NodeThoughtSession), SymbolName: "unrelated-a"},
		{Id: lowerID, Type: string(kgtypes.NodeThoughtSession), SymbolName: "dup-name"},
		{Id: "other-2", Type: string(kgtypes.NodeThoughtSession), SymbolName: "unrelated-b"},
	}
	fc := &backfillFakeCaller{
		sessions:  sessions,
		mutateIDs: []string{"th-new"},
	}
	deps := interceptTestDeps{gc: fc}

	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"think","content":"a thought into a duplicated session","summary":"collision thought gist","session":"dup-name"}`),
	})
	require.False(t, res.IsError, "collision think should succeed: %s", toolResultText(res))

	createPlan := thoughtCreatePlan(fc)
	require.NotNil(t, createPlan, "a thought CREATE plan must exist")
	assert.True(t, hasContainsFrom(createPlan, lowerID),
		"the thought must attach to the LOWER-id session %s (deterministic lowest-id tie-break)", lowerID)
	assert.False(t, hasContainsFrom(createPlan, higherID),
		"the thought must NOT attach to the higher-id duplicate %s", higherID)
}

// TestHandleThinkClient_SessionResolve_ReadOnlyHotPath codifies the
// accepted-residue disposition AND the bounded-read shape: resolving a session —
// even one with same-name duplicates present — performs NO write-side repair and
// exactly ONE browse read on the hot path.
func TestHandleThinkClient_SessionResolve_ReadOnlyHotPath(t *testing.T) {
	sessions := []*knowledgev1.Node{
		{Id: "00aa-lower", Type: string(kgtypes.NodeThoughtSession), SymbolName: "dup-name"},
		{Id: "ff99-higher", Type: string(kgtypes.NodeThoughtSession), SymbolName: "dup-name"},
		{Id: "other-1", Type: string(kgtypes.NodeThoughtSession), SymbolName: "unrelated-a"},
	}
	fc := &backfillFakeCaller{
		sessions:  sessions,
		mutateIDs: []string{"th-new"},
	}
	deps := interceptTestDeps{gc: fc}

	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"think","content":"a read-only-path thought","summary":"read-only hot path gist","session":"dup-name"}`),
	})
	require.False(t, res.IsError, "read-only-path think should succeed: %s", toolResultText(res))

	// (1) NO write-side repair: no DELETE, and no EdgeKGContains LINK re-pointing a
	// duplicate's containment (a self-heal-on-touch merge). The accepted-residue
	// disposition: duplicates stay split; the hot path never repairs them.
	for _, m := range fc.mutations {
		assert.NotEqual(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, m.GetKind(),
			"the resolve hot path must issue NO DELETE (accepted-residue, no self-heal)")
		if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_LINK &&
			m.GetEdgeSpec().GetRelationship() == string(kgtypes.EdgeKGContains) {
			// The only contains edge is the new thought's session attachment, which
			// rides the CREATE batch (not a LINK). A contains LINK here would be a
			// duplicate-merge repair.
			t.Errorf("unexpected EdgeKGContains LINK plan — no contains re-point/merge on the hot path")
		}
	}

	// (2) ONE bounded SESSION predicate browse — not a paged drain or per-session
	// N+1. Scoped to the session browse so the unrelated origin/agent read is
	// excluded; a revert to a drain over the session set → >1 session browse → RED.
	assert.Equal(t, 1, fc.sessionBrowseCalls,
		"the bounded session predicate browse is a SINGLE read, not a drain")
}

// TestHandleThinkClient_PredicateBlindServer_NoWrongAttach is the
// defense-in-depth regression: against a server that DROPS field_predicates and
// returns the unfiltered capped page (none matching the target name), the
// always-on client-side SymbolName guard must prevent attaching to a wrong-named
// session — the resolver creates a NEW one instead. Deleting the client guard
// makes the resolver pick the lowest-id arbitrary-name session → this goes RED.
func TestHandleThinkClient_PredicateBlindServer_NoWrongAttach(t *testing.T) {
	seeded := []*knowledgev1.Node{
		{Id: "aaa", Type: string(kgtypes.NodeThoughtSession), SymbolName: "aaa-session"},
		{Id: "bbb", Type: string(kgtypes.NodeThoughtSession), SymbolName: "bbb-session"},
		{Id: "ccc", Type: string(kgtypes.NodeThoughtSession), SymbolName: "ccc-session"},
	}
	fc := &backfillFakeCaller{
		sessions:              seeded,
		ignoreFieldPredicates: true, // model a predicate-blind server (drops field_predicates).
		// First create id → the new session; second → the thought.
		createQueue: [][]string{{"new-sess-id"}, {"th-new"}},
	}
	deps := interceptTestDeps{gc: fc}

	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"think","content":"a lonely thought","summary":"lonely session gist","session":"lonely-session"}`),
	})
	require.False(t, res.IsError, "predicate-blind think should succeed: %s", toolResultText(res))

	// (1) A NEW session was CREATED (the client guard rejected every off-name row).
	body := sessionCreateBody(fc)
	require.NotNil(t, body, "the client-side SymbolName guard must force a NEW session CREATE")
	assert.Equal(t, "lonely-session", body.GetName(),
		"the created session carries the requested name, not an arbitrary seeded one")

	// (2) The thought attaches to the NEWLY created session id, never a seeded
	// arbitrary-name session.
	createPlan := thoughtCreatePlan(fc)
	require.NotNil(t, createPlan, "a thought CREATE plan must exist")
	assert.True(t, hasContainsFrom(createPlan, "new-sess-id"),
		"the thought must attach to the newly created session")
	for _, id := range []string{"aaa", "bbb", "ccc"} {
		assert.False(t, hasContainsFrom(createPlan, id),
			"the thought must NEVER attach to the wrong-named seeded session %s", id)
	}
}
