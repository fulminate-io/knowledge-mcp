// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// errTestPerGraphClear is the synthetic per-graph clear failure the
// per-graph-error-surfacing test injects via mutateErrByTargetName.
var errTestPerGraphClear = errors.New("synthetic per-graph clear failure")

// mutationExecRequests filters the fake's recorded ExecuteRequests down to the
// mutation-carrying ones, dropping the catalog-enumeration GRAPH_NAMES reads the
// overlay fan-out now issues. Lets the clear tests assert mutation count/targets
// without the enumeration reads inflating the slice.
func mutationExecRequests(fc *fakeGraphCaller) []*knowledgev1.ExecuteRequest {
	var out []*knowledgev1.ExecuteRequest
	for _, r := range fc.execRequests {
		if r.GetMutation() != nil {
			out = append(out, r)
		}
	}
	return out
}

// TestMutateComposers_ClearLLMFailures_SingleGraph asserts the composer issues
// ONE predicate UPDATE PER MARKER KEY against a single named graph: an OP_EXISTS
// predicate + set_metadata empty clear per key. Matches the server
// clearLLMFailuresInGraph node-selection semantics via generic MutationPlans.
//
// THE EXPECTED COUNT IS DERIVED FROM llmFailureKeys, never written as a literal.
// The set gained a third key when the terminal summary marker landed, and a
// literal 2 here would have made every row in this file red for a reason that has
// nothing to do with what the row asserts.
func TestMutateComposers_ClearLLMFailures_SingleGraph(t *testing.T) {
	// The named target must resolve against the enumerated catalog before any
	// UPDATE (the unresolvable-name loud-miss guard), so seed code/knowledge.
	fc := &fakeGraphCaller{
		mutateAffected:   3,
		listGraphsResult: listGraphsResultJSON(t, [2]string{"code", "knowledge"}),
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"clear_llm_failures","graph":"code","name":"knowledge"}`),
	})
	require.True(t, handled, "clear_llm_failures is claimed client-side")
	require.False(t, res.IsError, "clear: %s", toolResultText(res))

	require.Len(t, fc.execMutations, len(llmFailureKeys), "one predicate UPDATE per marker key in llmFailureKeys")
	gotKeys := map[string]bool{}
	for _, plan := range fc.execMutations {
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, plan.GetKind())
		require.NotNil(t, plan.GetSelection())
		preds := plan.GetSelection().GetMetadataPredicates()
		require.Len(t, preds, 1, "one OP_EXISTS predicate")
		assert.Equal(t, knowledgev1.MetadataPredicate_OP_EXISTS, preds[0].GetOp())
		key := preds[0].GetKey()
		// set_metadata clears the SAME key to empty string.
		md := plan.GetSetMetadata()
		require.Contains(t, md, key)
		assert.Empty(t, md[key], "clear writes empty string, not a delete")
		gotKeys[key] = true
	}
	for _, key := range llmFailureKeys {
		assert.True(t, gotKeys[key], "%s must be cleared: every key on llmFailureKeys gets its UPDATE", key)
	}
	// SPELLED OUT TOO, so a set that silently lost a key does not make the loop
	// above vacuous. The terminal summary marker is the one that MUST be here: the
	// collect self-heal keeps it across an unchanged collect, so this clear is the
	// only way a node whose content never changes re-enters the pipeline.
	assert.True(t, gotKeys[kgtypes.MetaKeySummaryFailureReason], "summary_failure_reason cleared")
	assert.True(t, gotKeys[kgtypes.MetaKeyEmbedFailureReason], "embed_failure_reason cleared")
	assert.True(t, gotKeys[kgtypes.MetaKeySummaryFailureTerminal], "summary_failure_terminal cleared")

	// Each mutation Execute request targets the named graph. The code graph routes
	// its name via Repo (not Name) — the engine's resolveCode rejects sel.Name on a
	// code selector with "graph=code requires repo: graph selector invalid",
	// which is the bug clearTarget previously tripped silently. (execRequests also
	// carries the catalog-enumeration GRAPH_NAMES reads; filter to the mutations.)
	mutReqs := mutationExecRequests(fc)
	require.Len(t, mutReqs, len(llmFailureKeys))
	for _, req := range mutReqs {
		require.NotNil(t, req.GetTarget())
		assert.Equal(t, "code", req.GetTarget().GetGraph())
		assert.Equal(t, "knowledge", req.GetTarget().GetRepo())
		assert.Empty(t, req.GetTarget().GetName(), "code selector must NOT also carry Name")
	}
	// One UPDATE per key * 3 affected each, with the per-key breakdown beside the
	// total so an operator can see WHICH marker cleared.
	assert.Contains(t, toolResultText(res), fmt.Sprintf("cleared %d marker(s)", 3*len(llmFailureKeys)))
	assert.Contains(t, toolResultText(res), kgtypes.MetaKeySummaryFailureTerminal+"=3",
		"the terminal marker's own cleared count must be reported, not folded away")
}

// TestMutateComposers_ClearLLMFailures_SkippedCountSurfaced asserts the
// not_found skip count (ExecuteResponse.skipped_count) is aggregated
// across the per-marker UPDATEs and surfaced in the operator-visible result text
// when > 0. The fake reports mutateSkipped=2 per UPDATE, so the expected total is
// 2 * len(llmFailureKeys), derived rather than written. Fails-when-absent: drop
// the totalSkipped accumulation/append and this assertion goes red.
func TestMutateComposers_ClearLLMFailures_SkippedCountSurfaced(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateAffected:   3,
		mutateSkipped:    2, // each marker UPDATE skips 2 not_found nodes.
		listGraphsResult: listGraphsResultJSON(t, [2]string{"code", "knowledge"}),
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"clear_llm_failures","graph":"code","name":"knowledge"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "clear: %s", toolResultText(res))
	txt := toolResultText(res)
	assert.Contains(t, txt, fmt.Sprintf("skipped %d not_found marker(s)", 2*len(llmFailureKeys)),
		"the aggregated skipped count must surface in the operator-visible result")
	assert.Contains(t, txt, fmt.Sprintf("cleared %d marker(s)", 3*len(llmFailureKeys)),
		"the cleared counts are still reported alongside the skip count")
}

// TestMutateComposers_ClearLLMFailures_NoSkipNoSkipText asserts the skip clause
// is ABSENT from the result when nothing was skipped (the common case — every
// marker found its node). Guards against an always-on "skipped 0" noise line.
func TestMutateComposers_ClearLLMFailures_NoSkipNoSkipText(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateAffected:   3,
		mutateSkipped:    0, // no not_found markers.
		listGraphsResult: listGraphsResultJSON(t, [2]string{"code", "knowledge"}),
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"clear_llm_failures","graph":"code","name":"knowledge"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "clear: %s", toolResultText(res))
	assert.NotContains(t, toolResultText(res), "skipped",
		"no skip clause when nothing was skipped")
}

// TestMutateComposers_ClearLLMFailures_OnlyPhantomMarkers asserts the all-zero-
// cleared path STILL surfaces the skip count when a graph's only markers were
// phantom (cleared 0, skipped N) — rather than the misleading bare "nothing to
// clear". Fails-when-absent: the totalSkipped > 0 branch in the zero-cleared
// path is the only thing that distinguishes this from a clean no-op.
func TestMutateComposers_ClearLLMFailures_OnlyPhantomMarkers(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateAffected:   0, // nothing actually cleared.
		mutateSkipped:    1, // each UPDATE skips one phantom marker.
		listGraphsResult: listGraphsResultJSON(t, [2]string{"code", "knowledge"}),
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"clear_llm_failures","graph":"code","name":"knowledge"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "an all-skip sweep is not an error")
	txt := toolResultText(res)
	assert.Contains(t, txt, fmt.Sprintf("skipped %d not_found marker(s)", len(llmFailureKeys)),
		"the zero-cleared path must still surface the skip count")
	assert.NotContains(t, txt, "no failure markers found",
		"a pure-phantom sweep must NOT read as a clean nothing-to-clear")
}

// TestMutateComposers_ClearLLMFailures_KnowledgeRootNilTarget asserts the
// knowledge/default root graph clears with a nil GraphSelector (the engine's
// empty-graph=knowledge default) rather than an explicit name.
func TestMutateComposers_ClearLLMFailures_KnowledgeRootNilTarget(t *testing.T) {
	// Seed the knowledge catalog so the named root target resolves the catalog
	// membership check; the clear then still routes to the nil/default selector.
	fc := &fakeGraphCaller{
		mutateAffected:   1,
		listGraphsResult: listGraphsResultJSON(t, [2]string{"knowledge", "knowledge"}),
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"clear_llm_failures","graph":"knowledge","name":"knowledge"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "clear: %s", toolResultText(res))
	// Filter to the mutation requests (execRequests also carries the catalog
	// GRAPH_NAMES reads, which DO carry a {graph:"knowledge"} target).
	mutReqs := mutationExecRequests(fc)
	require.Len(t, mutReqs, len(llmFailureKeys))
	for _, req := range mutReqs {
		assert.Nil(t, req.GetTarget(), "knowledge root clears against the nil/default selector")
	}
}

// TestMutateComposers_ClearLLMFailures_KnowledgeOverlayNilTarget asserts the
// OVERLAY half of the same contract: the knowledge family is a singleton, so a
// knowledge session overlay clears against the nil selector exactly like the
// base does. Fails-when-absent: the root test above seeds no overlay key, so it
// passes whether or not the overlay branch emits a Name.
func TestMutateComposers_ClearLLMFailures_KnowledgeOverlayNilTarget(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateAffected:    1,
		listGraphsResult:  listGraphsResultJSON(t, [2]string{"knowledge", "knowledge"}),
		overlayKeysByBase: map[string][]string{"knowledge": {"knowledge@session-x"}},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"clear_llm_failures","graph":"knowledge","name":"knowledge"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "clear: %s", toolResultText(res))
	// One UPDATE per marker key * 2 resolved keys (base knowledge + overlay
	// knowledge@session-x). The RESOLVED-KEY count is the fixture's; the marker
	// count is derived from the declaration.
	mutReqs := mutationExecRequests(fc)
	require.Len(t, mutReqs, 2*len(llmFailureKeys), "one UPDATE per marker key per resolved key (base + overlay)")
	for _, req := range mutReqs {
		assert.Nil(t, req.GetTarget(), "every knowledge clear target resolves to the nil selector")
	}
}

// TestMutateComposers_ClearLLMFailures_MultiGraphFanOut asserts the empty-graph
// case resolves the loaded graphs via pipeline_list_graphs then issues the two
// predicate UPDATEs PER resolved graph (two graphs → 2 * len(llmFailureKeys)).
func TestMutateComposers_ClearLLMFailures_MultiGraphFanOut(t *testing.T) {
	listResult := kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{"graphs":[
		{"graph_type":"code","graph_name":"knowledge"},
		{"graph_type":"practice","graph_name":"go"}
	]}`}}}
	fc := &fakeGraphCaller{mutateAffected: 1, listGraphsResult: &listResult}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"clear_llm_failures"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "clear: %s", toolResultText(res))
	assert.Len(t, fc.execMutations, 2*len(llmFailureKeys), "one UPDATE per marker key per resolved graph")

	// THE PREDICATE UPDATES ONLY. The sweep also issues catalog QUERIES, whose
	// practice target carries no instance field whatever clearTarget does — so a
	// loop over every recorded ExecuteRequest is satisfied by the reads and never
	// observes the writes this test is about. The discriminating end-to-end guard
	// for clearTarget's practice arm lives in
	// TestClearLLMFailures_PracticeWriteTargetsCarryNoInstanceField; this one keeps
	// the fan-out count and the per-family routing honest beside it.
	var sawPracticeSingleton, sawCodeRepo bool
	for _, req := range mutationExecRequests(fc) {
		tgt := req.GetTarget()
		require.NotNil(t, tgt)
		switch tgt.GetGraph() {
		case "practice":
			// INVERTED WITH THE COMBINED GRAPH. This assertion used to require the
			// practice target to carry its name on Language, and a green run of it
			// in that shape was positive evidence that clearTarget had NOT been
			// converted to derive through graphsel. Practice is a singleton now: the
			// target names the family and carries NO instance field, which is what
			// the server's practice policy row accepts.
			if tgt.GetLanguage() == "" && tgt.GetName() == "" && tgt.GetRepo() == "" {
				sawPracticeSingleton = true
			}
		case "code":
			if tgt.GetRepo() == "knowledge" {
				sawCodeRepo = true
			}
		}
	}
	assert.True(t, sawPracticeSingleton,
		"practice target carries no instance field: the family is one graph and its policy row consumes none")
	assert.True(t, sawCodeRepo, "code target routes name via Repo")
}

// TestMutateComposers_ClearLLMFailures_OverlayFanOut asserts the clear fans a
// resolved base out across its overlay keys: for code/agent with one overlay key,
// it issues one predicate UPDATE per marker key per resolved key, and the overlay
// UPDATE's GraphSelector carries Branch=feature-x (the code overlay routes via
// Branch → server repo@branch Scope), while the base UPDATE carries an empty
// Branch. Fails-when-absent: a base-only fan-out leaves the overlay markers
// unreachable (only 2 UPDATEs, no Branch).
//
// TABLE OVER THE TWO CATALOG KEY FORMS, because the two backends genuinely report
// the SAME overlay differently — cloud as the composed "agent@feature-x" key, OSS
// and local as the bare "feature-x" name — and the resolver must be blind to which
// one arrived. The two cases must produce IDENTICAL results; that equivalence IS
// the assertion. The bare case is the regression gate on appendOverlayTargets'
// former atSplit-based skip, which dropped EVERY OSS overlay silently: the landed
// version of this test seeded the cloud form only, a single-shape probe that was
// generalized to both backends.
func TestMutateComposers_ClearLLMFailures_OverlayFanOut(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"cloud form key", "agent@feature-x"},
		{"bare OSS key", "feature-x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeGraphCaller{
				mutateAffected:    1,
				listGraphsResult:  listGraphsResultJSON(t, [2]string{"code", "agent"}),
				overlayKeysByBase: map[string][]string{"agent": {tc.key}},
			}
			deps := interceptTestDeps{gc: fc}
			handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
				Name:      "manage",
				Arguments: json.RawMessage(`{"operation":"clear_llm_failures","graph":"code","name":"agent"}`),
			})
			require.True(t, handled)
			require.False(t, res.IsError, "clear: %s", toolResultText(res))

			// 2 resolved keys (base agent + the overlay), one UPDATE per marker key.
			require.Len(t, fc.execMutations, 2*len(llmFailureKeys),
				"one UPDATE per marker key per resolved key (base + overlay)")

			var baseUpdates, overlayUpdates int
			for _, req := range mutationExecRequests(fc) {
				tgt := req.GetTarget()
				require.NotNil(t, tgt)
				assert.Equal(t, "code", tgt.GetGraph())
				assert.Equal(t, "agent", tgt.GetRepo(), "overlay routes via Branch, not a composed Repo")
				switch tgt.GetBranch() {
				case "":
					baseUpdates++
				case "feature-x":
					overlayUpdates++
				default:
					t.Errorf("unexpected branch %q on clear UPDATE", tgt.GetBranch())
				}
			}
			assert.Equal(t, len(llmFailureKeys), baseUpdates, "one base UPDATE per marker key")
			assert.Equal(t, len(llmFailureKeys), overlayUpdates,
				"one overlay UPDATE per marker key, carrying Branch=feature-x")
		})
	}
}

// TestMutateComposers_ClearLLMFailures_UnresolvableName asserts a graph+name
// target absent from the enumerated catalog fails LOUD (res.IsError naming the
// bad target) BEFORE any UPDATE, rather than the silent "no failure markers
// found across 1 graph(s)". Fails-when-absent: the bland non-error path returns.
func TestMutateComposers_ClearLLMFailures_UnresolvableName(t *testing.T) {
	fc := &fakeGraphCaller{
		// The code catalog has "agent" only — "nonexistent" must not resolve.
		listGraphsResult: listGraphsResultJSON(t, [2]string{"code", "agent"}),
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"clear_llm_failures","graph":"code","name":"nonexistent"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError, "an unresolvable name must surface as an error result")
	assert.Contains(t, toolResultText(res), "nonexistent", "the error names the bad target")
	assert.Empty(t, fc.execMutations, "no UPDATE issued for an unresolvable target")
}

// TestMutateComposers_ClearLLMFailures_PerGraphErrorSurfaced asserts a per-graph
// clear error is surfaced in the operator-visible result text (not only slog).
// One base (code/good) clears fine; another (code/bad) errors its UPDATE — the
// error line must appear in the summary. Fails-when-absent: the error is swallowed
// into slog and the summary reads a bland success/zero.
func TestMutateComposers_ClearLLMFailures_PerGraphErrorSurfaced(t *testing.T) {
	fc := &fakeGraphCaller{
		mutateAffected:        2,
		listGraphsResult:      listGraphsResultJSON(t, [2]string{"code", "good"}, [2]string{"code", "bad"}),
		mutateErrByTargetName: map[string]error{"bad": errTestPerGraphClear},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name:      "manage",
		Arguments: json.RawMessage(`{"operation":"clear_llm_failures","graph":"code"}`),
	})
	require.True(t, handled)
	// good cleared markers (partial success), so this is a non-error result that
	// STILL surfaces the bad-graph error in its text.
	require.False(t, res.IsError, "partial success is a non-error result")
	txt := toolResultText(res)
	assert.Contains(t, txt, "errors:", "the per-graph error must be surfaced in the result text")
	assert.Contains(t, txt, "bad", "the failing target is named in the surfaced error")
}
