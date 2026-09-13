// SPDX-License-Identifier: Apache-2.0

package tools

// practice_singleton_arm_result_test.go holds the END-TO-END ARM assertions for
// the two client-module practice selector switches whose conversion nothing else
// in this package can observe, plus the four manage/sync arms manageGraphSelector
// routes.
//
// CELL NINE IS THE ONE ARM THAT DIVERGED AND HAS CONVERGED. sync push and sync
// pull used to address a LEGACY practice graph by the name the caller gave,
// while the eight pre-singleton graph IMAGES still existed on both sides; those
// images are gone, so every one of the four arms addresses the combined graph
// and carries no instance field. Cell nine therefore asserts the REFUSAL for a
// NAMED push and the singleton shape for an unselected one, which is a stronger
// pair than the single assertion it replaced: the old cell was satisfied by an
// arm that dropped the name on the floor, which is exactly what it was doing.
//
// WHY A SEPARATE FILE, AND WHY THE ASSERTIONS LOOK THE WAY THEY DO. Two earlier
// rounds of this change left a converted switch guarded by a test that could not
// fail, twice for the same two reasons, and both are structural rather than
// careless:
//
//  1. A LOOP OVER EVERY RECORDED REQUEST IS NOT A LOOP OVER THE WRITES. The clear
//     sweep issues catalog QUERIES as well as predicate UPDATEs, and a practice
//     query target carries no instance field whatever the write builder does — so
//     an existence assertion scanning fc.execRequests is satisfied by the reads
//     and never observes the writes at all. Every assertion below reads
//     mutationExecRequests, and asserts over EVERY practice write rather than
//     over the first one that happens to match.
//
//  2. AN EMPTY OPERATOR NAME HIDES A RESTORED SWITCH. The retired arms all read
//     `sel.Language = name`; with an empty name that is indistinguishable from the
//     derived shape, so an arm driven with no name is green under the mutation it
//     is supposed to catch. Every arm below is driven with a NON-EMPTY name, which
//     is the only shape in which the two builders differ.
//
// The Family assertion is the second, independent discriminant: only
// graphsel.GraphSelectorFor sets the typed enum, so a hand-written arm that
// rebuilds the selector literal leaves it UNSPECIFIED even when its instance
// field happens to be empty.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// assertPracticeSingletonTarget is the shared shape assertion: a practice target
// names the family, carries NO instance field on any of the three spellings a
// family has ever been keyed by, and carries the typed Family enum that only the
// derivation sets.
func assertPracticeSingletonTarget(t *testing.T, tgt *knowledgev1.GraphSelector, where string) {
	t.Helper()
	require.NotNil(t, tgt, "%s: a practice target was built", where)
	assert.Equal(t, "practice", tgt.GetGraph(), "%s: the target names the practice family", where)
	assert.Empty(t, tgt.GetLanguage(),
		"%s: practice is one combined graph — the server's policy row consumes no language", where)
	assert.Empty(t, tgt.GetName(), "%s: and the name must not fall through to the default arm either", where)
	assert.Empty(t, tgt.GetRepo(), "%s: nor onto repo", where)
	assert.Equal(t, knowledgev1.GraphFamily_GRAPH_FAMILY_PRACTICE, tgt.GetFamily(),
		"%s: the typed family rides the derivation; a hand-rebuilt selector literal leaves it unspecified", where)
}

// TestClearLLMFailures_PracticeWriteTargetsCarryNoInstanceField is the
// discriminating guard for clearTarget's practice arm.
//
// FAILS WHEN ABSENT: restore `sel.Language = overlayName(tgt.name, tgt.branch)`
// on clearTarget's practice arm and every practice UPDATE below carries
// Language="go", which is the field the server's practice policy row no longer
// consumes. The sibling fan-out test cannot see that: it scans every recorded
// ExecuteRequest, and the two practice CATALOG QUERIES the sweep issues satisfy
// an empty-instance existence check regardless of what the write builder did.
func TestClearLLMFailures_PracticeWriteTargetsCarryNoInstanceField(t *testing.T) {
	listResult := kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{"graphs":[
		{"graph_type":"code","graph_name":"knowledge"},
		{"graph_type":"practice","graph_name":"go"}
	]}`}}}
	fc := &fakeGraphCaller{mutateAffected: 1, listGraphsResult: &listResult}
	handled, res := InterceptManage(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"clear_llm_failures"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "clear: %s", toolResultText(res))

	// THE WRITES ONLY. A catalog read's target proves nothing about the builder
	// under test; the predicate UPDATEs are the ones clearTarget builds.
	var practiceWrites int
	for i, req := range mutationExecRequests(fc) {
		tgt := req.GetTarget()
		if tgt.GetGraph() != "practice" {
			continue
		}
		practiceWrites++
		assertPracticeSingletonTarget(t, tgt, "clear_llm_failures practice UPDATE")
		require.NotNil(t, req.GetMutation(), "recorded request %d is a mutation", i)
	}
	// A ZERO NEEDS A CONTROL: without this the loop above passes vacuously on a
	// sweep that never reached practice at all.
	require.Equal(t, len(llmFailureKeys), practiceWrites,
		"the practice graph is swept by default and takes one predicate UPDATE per marker key in llmFailureKeys")
}

// TestPromoteMetadata_PracticeMigrationTargetCarriesNoInstanceField is the
// end-to-end guard for metadataBackfillTarget, which nothing in the tree named
// before this test existed.
//
// FAILS WHEN ABSENT: restore the `Language: name` field on
// metadataBackfillTarget's practice arm and the dispatched MIGRATE_META_REPR
// carries Language="go".
//
// The arm is driven through the manage intercept with a forced key so the
// decision loop actually dispatches — a KEEP recommendation issues no mutation
// and would leave this assertion with nothing to read.
func TestPromoteMetadata_PracticeMigrationTargetCarriesNoInstanceField(t *testing.T) {
	fc := &fakeGraphCaller{
		metadataStatsResp: metadataStatsResp(t, map[string]*knowledgev1.KeyStats{
			"source_hub": {DistinctValues: 4, MedianNodesPerValue: 10},
		}, &knowledgev1.OverrideConfig{ForceEdge: []string{"source_hub"}}),
	}
	handled, res := InterceptManage(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"promote_metadata","graph":"practice","name":"go"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "promote_metadata: %s", toolResultText(res))

	var migrations int
	for _, req := range mutationExecRequests(fc) {
		if req.GetMutation().GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_MIGRATE_META_REPR {
			continue
		}
		migrations++
		assertPracticeSingletonTarget(t, req.GetTarget(), "promote_metadata MIGRATE_META_REPR")
	}
	require.Equal(t, 1, migrations, "the forced key dispatches exactly one migration")
}

// TestManageArms_PracticeTargetsCarryNoInstanceField covers requirement 6 cells
// six through nine — the four arms manageGraphSelector feeds — on each ARM'S
// RESULT, driven through the tool intercept rather than by calling the builder.
//
// EACH SUBTEST SUPPLIES A NON-EMPTY name. That is the whole discriminating power
// of these tests: `sel.Language = name` and the derivation agree when name is
// empty, which is why the pre-existing prune routing test stayed green under the
// restored switch.
func TestManageArms_PracticeTargetsCarryNoInstanceField(t *testing.T) {
	t.Run("cell six: set_metadata_overrides", func(t *testing.T) {
		ix := &fakeIndexer{}
		handled, res := manageCall(t, ix,
			`{"operation":"set_metadata_overrides","graph":"practice","name":"go","force_scalar":["kind"]}`)
		require.True(t, handled)
		require.False(t, res.IsError, "set_metadata_overrides: %s", toolResultText(res))

		reqs := ix.requests()
		require.Len(t, reqs, 1, "exactly one Index RPC")
		assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_SET_METADATA_OVERRIDES, reqs[0].GetOperation())
		assertPracticeSingletonTarget(t, reqs[0].GetTarget(), "set_metadata_overrides")
		// The arm's own payload still lands: the target losing its instance field
		// must not cost the override config it exists to carry.
		assert.Equal(t, "kind", reqs[0].GetParams()["force_scalar"])
		assert.Contains(t, toolResultText(res), "metadata override config saved for practice/go")
	})

	t.Run("cell seven: rebuild_cache refuses practice at its own graph guard", func(t *testing.T) {
		// STATED RATHER THAN ASSUMED. The prefill's cell seven expects this arm to
		// succeed against the singleton; it does not, and never did — rebuild_cache
		// serves the content-hash caches, which exist for code and knowledge only,
		// so it refuses practice BEFORE manageGraphSelector is reached. The arm
		// result asserted here is therefore the refusal, and this cell carries no
		// selector red: there is no selector to observe.
		ix := &fakeIndexer{}
		handled, res := manageCall(t, ix, `{"operation":"rebuild_cache","graph":"practice","name":"go"}`)
		require.True(t, handled)
		require.True(t, res.IsError, "rebuild_cache on practice is refused by the arm itself")
		assert.Contains(t, toolResultText(res), "the content-hash caches are builtin-graph only")
		assert.Empty(t, ix.requests(), "the refusal precedes the Index RPC, so no selector is built")
	})

	t.Run("cell eight: prune", func(t *testing.T) {
		ix := &fakeIndexer{affectedCount: 2}
		handled, res := manageCall(t, ix, `{"operation":"prune","graph":"practice","name":"go"}`)
		require.True(t, handled)
		require.False(t, res.IsError, "prune: %s", toolResultText(res))

		reqs := ix.requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_PRUNE, reqs[0].GetOperation())
		assertPracticeSingletonTarget(t, reqs[0].GetTarget(), "prune")
		// prune is destructive at the NODE level and stays that way: it must not
		// have become a graph-level drop, which requirement 7 forbids. The drop is
		// a MUTATION, not an Index op, so the observable is that the arm issued no
		// mutation at all.
		assert.Equal(t, int64(1), ix.indexCalls.Load(), "one Index RPC and nothing else")
	})

	t.Run("cell_nine_sync_push_carries_no_instance_field_either", func(t *testing.T) {
		// THIS CELL INVERTED. It used to pin that a push of a LEGACY practice name
		// carried that name on the `language` field — the one divergence from every
		// other practice arm, because sync moves whole graph IMAGES and the eight
		// pre-singleton images still existed on both sides. They are gone: the only
		// practice image a push can move is the combined graph's, so the target
		// carries no instance field and matches the other eight cells.
		//
		// The export is the first thing pushGraph does and the only step that reads
		// the selector, so failing it after the record keeps this test off the
		// network while still driving the real arm through InterceptSync.
		exp := &fakeExporter{exportErr: errors.New("scripted export stop")}
		// The control-plane transport is built BEFORE pushGraph runs, so the arm is
		// unreachable without swapping it; the scripted export failure returns
		// before the transport is ever dereferenced.
		withTransport(t, func() (*auth.Transport, error) { return nil, nil })
		handled, res := InterceptSync(opCtx(), interceptTestDeps{gc: exp},
			syncParams(t, map[string]any{"operation": "push", "graph": "practice"}))
		require.True(t, handled)
		require.Equal(t, 1, exp.exportCalls, "the push arm reached ExportGraph")
		tgt := exp.lastTarget
		require.NotNil(t, tgt)
		assert.Equal(t, "practice", tgt.GetGraph())
		assert.Empty(t, tgt.GetLanguage(), "no language is composed for any practice push")
		assert.Empty(t, tgt.GetName(), "and the name must not fall through to the default arm")
		assert.Empty(t, tgt.GetRepo(), "nor onto repo")
		assert.Equal(t, knowledgev1.GraphFamily_GRAPH_FAMILY_PRACTICE, tgt.GetFamily(),
			"the typed family rides this builder too")
		assert.Contains(t, toolResultText(res), "scripted export stop",
			"and the arm surfaced the export failure rather than proceeding")

		// THE REFUSAL HALF, so the emptiness above is the family's rule rather than
		// a name this call happened not to send: a push that DOES name a graph is
		// refused before the export.
		refused := &fakeExporter{exportErr: errors.New("unreachable")}
		_, rres := InterceptSync(opCtx(), interceptTestDeps{gc: refused},
			syncParams(t, map[string]any{"operation": "push", "graph": "practice", "name": "go"}))
		require.True(t, rres.IsError, "a named practice push is refused")
		assert.Zero(t, refused.exportCalls, "and refused before the export")
	})

	t.Run("cell nine control: an unselected sync push is the combined graph", func(t *testing.T) {
		// The same arm with NO name is the singleton shape, so the divergence above
		// is the legacy name and not a sync arm that forgot the family is combined.
		exp := &fakeExporter{exportErr: errors.New("scripted export stop")}
		withTransport(t, func() (*auth.Transport, error) { return nil, nil })
		handled, _ := InterceptSync(opCtx(), interceptTestDeps{gc: exp},
			syncParams(t, map[string]any{"operation": "push", "graph": "practice"}))
		require.True(t, handled)
		require.Equal(t, 1, exp.exportCalls, "the push arm reached ExportGraph")
		assertPracticeSingletonTarget(t, exp.lastTarget, "sync push with no name")
	})
}
