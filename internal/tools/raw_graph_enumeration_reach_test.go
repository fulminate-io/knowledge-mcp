// SPDX-License-Identifier: Apache-2.0

package tools

// raw_graph_enumeration_reach_test.go gates ONE root cause with two victims.
//
// HasRebuildableSegments used to be a strict SUBSET of SyncEligible, so a walk
// could iterate SyncEligibleGraphTypes() as its OUTER loop, filter by
// HasRebuildableSegments on the inside, and cover every segment-bearing graph by
// construction. The raw-graph enrollment breaks that nesting: web and pdf carry
// segments AND are deliberately never sync-eligible, so a sync-eligible outer loop
// drops them before any inner filter runs.
//
// An inner predicate cannot widen a set the loop already narrowed, which is why
// both fixes are to the LOOP and why both are gated here rather than by asserting
// on the predicate a third time.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// catalogByTypeFake answers a RETURN_MODE_GRAPH_NAMES read with the names seeded
// for the graph type the request TARGETS, so an enumeration that never asks about
// a type is observable as that type's absence from the result rather than as an
// empty catalog everywhere.
type catalogByTypeFake struct {
	byType map[string][]string
	asked  []string
}

func (f *catalogByTypeFake) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	gt := req.GetTarget().GetGraph()
	if req.GetQuery().GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	f.asked = append(f.asked, gt)
	infos := make([]*knowledgev1.GraphInfo, 0, len(f.byType[gt]))
	for _, n := range f.byType[gt] {
		infos = append(infos, &knowledgev1.GraphInfo{Name: n})
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
}

func (f *catalogByTypeFake) Stats(
	_ context.Context, _ *knowledgev1.StatsRequest,
) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{GraphStats: &knowledgev1.GraphStats{}}, nil
}

// rawEnumerationCatalog seeds one instance of every type the two walks could
// plausibly reach. The KNOWLEDGE and CODE entries are the controls: a walk that
// broke outright would lose them too, so their presence is what makes a missing
// web row evidence about web specifically rather than about the harness.
//
// THE LOGS ENTRY IS SEEDED FOR THE EXCLUSION CONTROL and for nothing else. The fake
// answers only for the type a request TARGETS, so an assertion that logs is absent
// from the coverage rows is VACUOUS unless the fake can answer for logs — without
// the seed, an over-wide walk asks about logs, receives an empty list and builds no
// row, so the assertion passes for the wrong reason and fences nothing.
func rawEnumerationCatalog() *catalogByTypeFake {
	return &catalogByTypeFake{byType: map[string][]string{
		string(kgtypes.GraphCode):     {"knowledge"},
		string(kgtypes.GraphCloud):    {"acct"},
		string(kgtypes.GraphPractice): {"go"},
		string(kgtypes.GraphLinkage):  {"default"},
		string(kgtypes.GraphWebRaw):   {"twelve-factor"},
		string(kgtypes.GraphPDFRaw):   {"stopford"},
		string(kgtypes.GraphLogs):     {"q-cloudwatch-1"},
	}}
}

// TestRawGraphEnumeration_ReachedByTheCoverageAndIdentityWalks proves both walks
// reach a raw graph.
func TestRawGraphEnumeration_ReachedByTheCoverageAndIdentityWalks(t *testing.T) {
	t.Run("coverage_row", func(t *testing.T) {
		// REJECTS THE SHIPPED DEFECT: coverageTargets walked SyncEligibleGraphTypes(),
		// so manage(status) built no coverage row for a collected document on ANY
		// backend — which is the ticket's own verification line ("manage status shows
		// binary vectors > 0 and segment coverage for the pdf graph") failing at the
		// enumeration, before the segment probe is ever consulted.
		fake := rawEnumerationCatalog()
		targets := coverageTargets(opCtx(), interceptTestDeps{gc: fake})

		got := map[string]bool{}
		for _, tg := range targets {
			got[string(tg.gt)+"/"+tg.name] = true
		}
		assert.True(t, got["web/twelve-factor"],
			"no coverage row for a collected WEB graph — the walk skipped the type entirely")
		assert.True(t, got["pdf/stopford"],
			"no coverage row for a collected PDF graph — the walk skipped the type entirely")

		// CONTROLS. The first pair is the known-positive that stops a broken walk
		// reading as a fixed one; the second is the regression guard on the widening,
		// since a fix that filtered by HasRebuildableSegments would repair the raw
		// graphs by DROPPING these two, which carry coverage counts and render today.
		assert.True(t, got["knowledge/default"], "the knowledge row vanished — the walk is broken, not widened")
		assert.True(t, got["code/knowledge"], "the code row vanished — the walk is broken, not widened")
		assert.True(t, got["linkage/default"],
			"linkage lost its row: the widening must not be a filter that drops non-segment graphs")

		// EXCLUSION CONTROL — the only assertion here that fires when the walk is too
		// WIDE. Every assertion above fires only on a walk that is too NARROW, so this
		// leg stayed green when the first fix overshot from the sync-eligible subset to
		// every builtin unfiltered, admitting logs along with web and pdf. A logs graph
		// is never summarized, embedded or synced, so its coverage cells are
		// structurally zero for its whole life and its row reads exactly like a
		// knowledge or code graph whose pipeline has completely failed. It is the one
		// builtin failing BOTH halves of the walk rule, which is what this pins.
		assert.False(t, got["logs/q-cloudwatch-1"],
			"a LOGS graph was walked: it is never summarized, embedded or synced, so its row is permanently 0%-covered and misreads as a broken pipeline")
	})

	t.Run("recorded_identity", func(t *testing.T) {
		// REJECTS THE SHIPPED DEFECT: RecordedGraphIdentities filtered by
		// HasRebuildableSegments INSIDE a SyncEligibleGraphTypes() loop, so the
		// doctor's embed-identity check never saw a raw graph however many vectors
		// it held.
		fake := rawEnumerationCatalog()
		ids, err := RecordedGraphIdentities(opCtx(), fake.Execute)
		require.NoError(t, err)

		got := map[string]bool{}
		for _, id := range ids {
			got[id.GraphType+"/"+id.Name] = true
		}
		assert.True(t, got["web/twelve-factor"],
			"a WEB graph is absent from the recorded-identity enumeration — the outer loop skipped the type")
		assert.True(t, got["pdf/stopford"],
			"a PDF graph is absent from the recorded-identity enumeration — the outer loop skipped the type")

		// KNOWN-POSITIVE control, and a NEGATIVE one: this walk is deliberately the
		// segment-bearing set, so linkage must stay OUT. Without the negative,
		// walking every builtin unfiltered would satisfy the assertions above while
		// costing a round trip per type that can hold no vector.
		assert.True(t, got["code/knowledge"], "the code entry vanished — the walk is broken, not widened")
		assert.False(t, got["linkage/default"],
			"linkage carries no vectors and must not be enumerated for an embed identity")
	})
}
