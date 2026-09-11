// SPDX-License-Identifier: Apache-2.0

// practice_singleton_test.go — the observables the combined practice graph adds
// that no pre-existing test could have carried: the hub selector on the read and
// delete arms, the hub stamp on a write, the `language` refusal on every write
// arm, and the drop refusal's POSITION.
//
// WHY THEY ARE HERE RATHER THAN SPREAD ACROSS THE ARM FILES. Each is a property
// of the FAMILY rather than of one arm — the same hub key has to reach a browse
// predicate, a delete predicate and a segment accept closure, and the same
// refusal has to fire on every write arm. A reviewer auditing "is requirement 3
// met" reads one file.

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestPracticeBrowse_NarrowsByHub is requirement 3a, as a PAIRED control.
//
// A zero without a positive proves nothing: an arm that dropped the selector
// entirely and an arm that narrowed correctly both return zero rows for a hub
// that owns nothing. The positive leg is what separates them.
func TestPracticeBrowse_NarrowsByHub(t *testing.T) {
	hubPredicate := func(t *testing.T, a queryArgs) []*knowledgev1.MetadataPredicate {
		t.Helper()
		f := &browseExecFake{resp: &knowledgev1.ExecuteResponse{}}
		practiceBrowse(opCtx(), f.exec, a)
		return f.lastPlan(t).GetSelection().GetMetadataPredicates()
	}

	t.Run("a named hub lowers to an equality predicate on the hub key", func(t *testing.T) {
		preds := hubPredicate(t, queryArgs{Graph: "practice", Source: "hub-1"})
		require.Len(t, preds, 1, "the hub rides ONE predicate, on the metadata path the browse already lowered")
		assert.Equal(t, kgtypes.MetaKeySourceHub, preds[0].GetKey(),
			"the hub key is source_hub and never `source`, which practice nodes already use for their own provenance")
		assert.Equal(t, "hub-1", preds[0].GetValue())
		assert.Equal(t, knowledgev1.MetadataPredicate_OP_EQ, preds[0].GetOp())
	})

	t.Run("the presence sentinel asks which nodes have a hub at all", func(t *testing.T) {
		preds := hubPredicate(t, queryArgs{Graph: "practice", Source: "*"})
		require.Len(t, preds, 1)
		assert.Equal(t, knowledgev1.MetadataPredicate_OP_EXISTS, preds[0].GetOp(),
			`"*" is the key-presence sentinel LowerMetaPredicates already carries`)
	})

	// THE NEGATIVE CONTROL: no hub, no predicate. Without it every assertion
	// above is satisfied by an arm that stamped the predicate unconditionally.
	t.Run("no hub adds no predicate", func(t *testing.T) {
		assert.Empty(t, hubPredicate(t, queryArgs{Graph: "practice"}),
			"an unselected browse reads the whole combined graph and narrows by nothing")
	})

	// AND THE COLLISION IS REFUSED rather than resolved. `source` and an explicit
	// meta predicate on the hub key are two spellings of one filter; preferring
	// either silently is the coercion this repo does not do.
	t.Run("source and an explicit hub meta key together are refused", func(t *testing.T) {
		f := &browseExecFake{resp: &knowledgev1.ExecuteResponse{}}
		res := practiceBrowse(opCtx(), f.exec, queryArgs{
			Graph: "practice", Source: "hub-1",
			Meta: map[string]string{kgtypes.MetaKeySourceHub: "hub-2"},
		})
		require.True(t, res.IsError, "two spellings of one filter is bad input")
		body := textBodyTools(res)
		assert.Contains(t, body, "hub-1", "the refusal names the `source` value")
		assert.Contains(t, body, "hub-2", "and the metadata one, so the caller can drop the one it did not mean")
	})
}

// TestPracticeWriteArms_RefuseLanguageNamingTheReplacement is requirement 4
// across the write arms, asserted on the MESSAGE rather than on non-nil.
//
// A refusal that does not name the replacement leaves an automated caller
// retrying blind, which is the shape this package's refusal file exists to
// prevent.
func TestPracticeWriteArms_RefuseLanguageNamingTheReplacement(t *testing.T) {
	for _, payload := range []string{
		`{"operation":"create","graph":"practice","language":"go","type":"pattern","name":"P","summary":"s"}`,
		`{"operation":"create_batch","graph":"practice","language":"go","nodes":[{"type":"pattern","name":"P","summary":"s"}]}`,
		`{"operation":"update","graph":"practice","language":"go","id":"p1","status":"active"}`,
		`{"operation":"delete","graph":"practice","language":"go","ids":["p1"]}`,
		`{"operation":"link","graph":"practice","language":"go","from":"p1","to":"p2","relationship":"contains"}`,
	} {
		var probe struct {
			Operation string `json:"operation"`
		}
		require.NoError(t, json.Unmarshal([]byte(payload), &probe))

		t.Run(probe.Operation, func(t *testing.T) {
			fc := &fakeGraphCaller{}
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate", Arguments: json.RawMessage(payload),
			})
			require.True(t, handled, "the arm must CLAIM the call in order to refuse it legibly")
			require.True(t, res.IsError, "`language` on a practice write is refused, never dropped")

			body := toolResultText(res)
			assert.Contains(t, body, "source_hub",
				"the refusal names the replacement — a caller reaching for `language` wants to group the write")
			assert.Contains(t, body, "on a read or on a write",
				"and says the rule is total, so the caller does not go looking for an arm that still takes it")
			assert.NotContains(t, body, "READ arms",
				"the message no longer points at read arms: they refuse it too")
			assert.Empty(t, fc.execMutations, "a refused write issues no mutation")
		})
	}
}

// TestPracticeGraphLabel_DistinguishesTheTwoReads is the render observable on
// the by-id and browse cells.
//
// THERE WERE THREE SPELLINGS AND THERE ARE TWO. A bare `practice` is the
// whole-corpus read and `practice:<hub>` is a hub-scoped one; the third was
// `practice:<language>`, a read of one pre-singleton graph, and it went with
// those graphs. The two that remain must still DIFFER: rendering the same word
// for both makes them indistinguishable in the one place a caller looks.
//
// AND THE RETIRED SPELLING MUST NOT COME BACK. A `language` reaching the label
// composer would mean the refusal ahead of it had been removed, so the absence
// is asserted rather than left to the refusal's own test.
func TestPracticeGraphLabel_DistinguishesTheTwoReads(t *testing.T) {
	whole := domainGraphLabel(queryArgs{Graph: "practice"})
	hub := domainGraphLabel(queryArgs{Graph: "practice", Source: "hub-1"})

	assert.Equal(t, "practice", whole, "an unselected read names the family alone")
	assert.Equal(t, "practice:hub-1", hub, "a hub-scoped read names the hub")

	// THE DISCRIMINATING ASSERTION: the two differ. Without it a renderer that
	// returned the family name for everything satisfies the first row and reads
	// as correct.
	require.NotEqual(t, whole, hub)

	// THE RETIRED SPELLING: a language qualifies nothing.
	assert.Equal(t, "practice", domainGraphLabel(queryArgs{Graph: "practice", Language: "go"}),
		"`language` composes no label — it addresses no practice graph and is refused before a read runs")
}

// TestDropGraph_PracticeRefusedBeforeTheExecute is requirement 7, and the
// assertion that matters is the ZERO MUTATIONS one rather than the error text.
//
// The local segment-cache teardown is unconditional once the server half
// succeeds, so a refusal moved into the ack still produces an error while the
// graph and its cache are already gone. An error-text assertion passes under
// that mutation; a zero-mutations one does not.
func TestDropGraph_PracticeRefusedBeforeTheExecute(t *testing.T) {
	for _, payload := range []string{
		`{"operation":"drop_graph","graph":"practice"}`,
		`{"operation":"drop_graph","graph":"practice","dry_run":true}`,
	} {
		t.Run(payload, func(t *testing.T) {
			fc := &fakeGraphCaller{}
			handled, res := InterceptManage(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "manage", Arguments: json.RawMessage(payload),
			})
			require.True(t, handled)
			require.True(t, res.IsError, "a practice drop is refused")
			assert.Empty(t, fc.execMutations,
				"the refusal lands BEFORE the Execute: the local cache teardown is unconditional after it")

			body := toolResultText(res)
			assert.Contains(t, body, "NEVER a destructive target",
				"the refusal names the rule rather than reading as a routing failure")
			assert.Contains(t, body, "source",
				"and names the call that removes practice nodes, which is a delete by hub")
		})
	}

	// THE FAMILY CONTROL. Without it, a drop_graph that refused everything would
	// satisfy both rows above.
	t.Run("code is still droppable", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		_, res := InterceptManage(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "manage",
			Arguments: json.RawMessage(`{"operation":"drop_graph","graph":"code","name":"myrepo","dry_run":true}`),
		})
		assert.False(t, res.IsError, "the refusal is practice-keyed: %s", toolResultText(res))
	})
}

// TestPracticeWriteArms_EveryArmRefusesLanguage is requirement 4 over the WHOLE
// write population rather than the five arms that happened to share a gate.
//
// WHY A TABLE OVER TEN ROWS. The refusal used to live on the two arms that claim
// a practice CRUD mutation, so upsert, unlink, update_batch and
// bulk_update_metadata — which decline past that arm to the non-knowledge
// fallthrough — dropped `language` silently and compiled a target with
// language="" while the caller believed it had addressed practice/go. A drop is
// the coercion this repo refuses, and an arm-by-arm test cannot see the arms it
// forgot to list, so the population is enumerated here and each row asserts the
// call was CLAIMED as well as refused: an unclaimed call is the shape that
// dropped the field.
func TestPracticeWriteArms_EveryArmRefusesLanguage(t *testing.T) {
	for _, row := range []struct{ arm, payload string }{
		{"create", `{"operation":"create","graph":"practice","language":"go","type":"pattern","name":"P","summary":"s"}`},
		{"create_batch", `{"operation":"create_batch","graph":"practice","language":"go","nodes":[{"type":"pattern","name":"P","summary":"s"}]}`},
		{"update", `{"operation":"update","graph":"practice","language":"go","id":"p1","status":"active"}`},
		{"update_batch", `{"operation":"update_batch","graph":"practice","language":"go","items":[{"id":"p1","summary":"s"}]}`},
		{"bulk_update_metadata", `{"operation":"bulk_update_metadata","graph":"practice","language":"go","updates":[{"id":"p1","metadata":{"k":"v"}}]}`},
		{"upsert", `{"operation":"upsert","graph":"practice","language":"go","id":"p1","type":"pattern","name":"P","summary":"s"}`},
		{"link", `{"operation":"link","graph":"practice","language":"go","from":"p1","to":"p2","relationship":"contains"}`},
		{"unlink", `{"operation":"unlink","graph":"practice","language":"go","from":"p1","to":"p2","relationship":"contains"}`},
		{"delete", `{"operation":"delete","graph":"practice","language":"go","ids":["p1"]}`},
	} {
		t.Run("mutate/"+row.arm, func(t *testing.T) {
			fc := &fakeGraphCaller{}
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate", Arguments: json.RawMessage(row.payload),
			})
			require.True(t, handled, "the arm must CLAIM the call in order to refuse it legibly")
			require.True(t, res.IsError, "`language` on a practice write is refused, never dropped")

			body := toolResultText(res)
			assert.Contains(t, body, "source_hub",
				"the refusal names the replacement on a write arm, where `source` is the node's own provenance")
			assert.Contains(t, body, "on a read or on a write",
				"and says the rule is total, so the caller does not go looking for an arm that still takes it")
			assert.NotContains(t, body, "READ arms",
				"the message no longer points at read arms: they refuse it too")
			assert.Empty(t, fc.execMutations, "a refused write issues no mutation")
		})
	}

	// THE DELETE TOOL is the tenth arm and reaches none of the mutate paths: its
	// arguments are decoded in the engine package and its only client-side gate
	// is the param guard, so a `language` on it was accepted and then dropped by
	// the compiler exactly as the mutate fallthrough arms did.
	t.Run("delete tool", func(t *testing.T) {
		handled, res := InterceptDeleteGuard(opCtx(), interceptTestDeps{gc: &fakeGraphCaller{}}, kgtools.CallToolParams{
			Name:      "delete",
			Arguments: json.RawMessage(`{"graph":"practice","language":"go","ids":["p1"]}`),
		})
		require.True(t, handled, "the delete tool must claim the call to refuse it")
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, "pass source:",
			"the delete tool's hub selector is spelled `source`, which is free on that schema")
		assert.NotContains(t, body, "source_hub",
			"and NOT source_hub, which is the mutate/search spelling and is not declared on this tool")
		assert.Contains(t, body, "on a read or on a write")
	})

	// THE FAMILY CONTROL, on the two arms the refusal newly reaches. Without it a
	// guard that refused `language` on every graph satisfies every row above.
	t.Run("control: language on a non-practice graph is not this refusal", func(t *testing.T) {
		handled, res := InterceptDeleteGuard(opCtx(), interceptTestDeps{gc: &fakeGraphCaller{}}, kgtools.CallToolParams{
			Name:      "delete",
			Arguments: json.RawMessage(`{"graph":"checks","ids":["p1"]}`),
		})
		assert.False(t, handled, "a clean delete falls through to the dispatcher unchanged")
		assert.False(t, res.IsError)
	})
}

// TestPivotEngineKey_SingletonsKeyUnderDefault is a defect this round found
// rather than a mechanism it built, and it is here because the defect is the
// same shape as the one requirement 11 exists to prevent.
//
// pivotEngineKey resolves which segment pool the pivot seed search reads. It
// hard-coded knowledge→"default" and fell through to `name` for every other
// family declaring no instance field — which was correct while knowledge was the
// only such family, and returned the EMPTY STRING for an unselected practice read
// the moment practice joined them. The collector seals the combined graph under
// "default", so an empty key addresses a pool that does not exist and the search
// reports no hits rather than an error: a silent wrong answer on a read path.
func TestPivotEngineKey_SingletonsKeyUnderDefault(t *testing.T) {
	gt, name := pivotEngineKey(queryArgs{Graph: "practice"})
	assert.Equal(t, kgtypes.GraphPractice, gt)
	assert.Equal(t, "default", name,
		"an unselected practice read keys the ONE pool the collector seals, never the empty string")

	// A `language` KEYS NOTHING. It used to name the pre-singleton graph a legacy
	// read addressed, and pivotEngineKey read it ahead of the normalizer for that
	// reason; the field addresses no practice graph now and the wire read carrying
	// it is refused, so keying the engine on it would point the seed search at a
	// pool no wire read can reach.
	_, withLanguage := pivotEngineKey(queryArgs{Graph: "practice", Language: "go"})
	assert.Equal(t, "default", withLanguage,
		"a practice pivot keys the one combined pool whatever `language` carries")

	// THE CONTROLS: the families whose instance field is a real one are
	// unaffected, and knowledge — the case the hard-coded arm existed for — still
	// resolves the same way now that it goes through the normalizer.
	_, kn := pivotEngineKey(queryArgs{})
	assert.Equal(t, "default", kn, "knowledge keeps the name its own arm used to hard-code")
	_, repo := pivotEngineKey(queryArgs{Graph: "code", Repo: "myrepo"})
	assert.Equal(t, "myrepo", repo)

	// AND AN ACCOUNT KEYS NOTHING. No family is account-keyed since the
	// account-keyed inventory families retired, so a caller-supplied account must
	// not become an engine key — the row asserts the absence rather than
	// disappearing with the family it used to be spelled with.
	_, acct := pivotEngineKey(queryArgs{Graph: "web", Account: "acct"})
	assert.NotEqual(t, "acct", acct,
		"an account reaches no engine key: no family consumes it")
}
