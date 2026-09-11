// SPDX-License-Identifier: Apache-2.0

package tools

// practice_language_refused_read_test.go — `language` is refused on every
// practice READ arm, naming the hub selector.
//
// WHY A MATRIX RATHER THAN ONE TEST. The arms are the population a reviewer has
// to enumerate to know the rule holds, and each one claims the payload at a
// different gate: the fan-out sentinel was refused ahead of everything, the
// mode-bearing arms are claimed by name, the browse is claimed by the absence of
// text, and the search tool has its own entry point entirely. A single test
// through one arm is satisfied by a refusal installed in one gate, which is how
// the write half of this rule reached production with four arms uncovered.
//
// THE VALUE COLUMNS ARE PART OF THE CLAIM. `language` is refused for its
// PRESENCE on a read, so the former legacy name ("go"), the combined graph's own
// name ("default") and a garbage value are all refused identically — there is no
// value of the parameter that addresses anything. The empty/absent column is the
// both-directions leg: it must still read the combined graph, or every assertion
// here is satisfied by an implementation that refuses every practice read.

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// practiceLanguageValues are the value classes a read arm must refuse: a former
// pre-singleton name, the combined graph's own name, the retired fan-out
// sentinel, and a value that never named anything.
var practiceLanguageValues = []string{"go", "default", "all", "not-a-graph", "Design Patterns"}

// assertPracticeLanguageRefused is the shared verdict: the call errored, the
// message states the rule and names the hub param, and no read was issued.
func assertPracticeLanguageRefused(t *testing.T, res kgtools.ToolResult, hubParam string) {
	t.Helper()
	require.True(t, res.IsError, "`language` on a practice read is refused: %s", textBodyTools(res))
	body := textBodyTools(res)
	assert.Contains(t, body, "does not accept `language`",
		"the refusal names the field it refused")
	assert.Contains(t, body, hubParam,
		"and names the hub selector the arm publishes, which is the replacement")
	assert.Contains(t, body, "ONE combined graph",
		"and says why, so a caller is not left guessing whether the value was wrong")
}

// TestPracticeRead_LanguageRefusedOnEveryArm is the matrix over the query tool's
// five practice arms.
func TestPracticeRead_LanguageRefusedOnEveryArm(t *testing.T) {
	arms := map[string]map[string]any{
		"browse":      {"graph": "practice", "type": "pattern"},
		"search":      {"graph": "practice", "text": "pool"},
		"stats":       {"graph": "practice", "mode": "stats"},
		"modules":     {"graph": "practice", "mode": "modules"},
		"style_index": {"graph": "practice", "mode": "style_index"},
	}
	for arm, base := range arms {
		for _, value := range practiceLanguageValues {
			t.Run(arm+"/"+value, func(t *testing.T) {
				args := map[string]any{}
				maps.Copy(args, base)
				args["language"] = value
				handled, res := drivePracticeSelector(t, args)
				require.True(t, handled, "the practice entry point claims the payload")
				assertPracticeLanguageRefused(t, res, practiceHubParamFree)
			})
		}
	}
}

// TestPracticeSearchTool_LanguageRefused covers the `search` tool's own practice
// arm, which has a separate entry point and publishes `source_hub` rather than
// `source` — a refusal naming the wrong spelling sends the caller to a param the
// arm does not declare.
func TestPracticeSearchTool_LanguageRefused(t *testing.T) {
	for _, value := range practiceLanguageValues {
		t.Run(value, func(t *testing.T) {
			gc := newFanOutHarness(t, []string{"default"},
				practiceNode("p:go", "GoWorkerPool", "bounded goroutines"))
			mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
				"default": {{ID: "p:go", Score: 0.99}},
			})
			deps := &interceptDeps{gc: gc, segMgr: mgr}
			handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
				"graph": "practice", "language": value, "query": "pool",
			}))
			require.True(t, handled)
			assertPracticeLanguageRefused(t, out, practiceHubParamOnWrites)
			assert.Empty(t, mgr.searchedNames(), "the refusal costs no read")
		})
	}
}

// TestPracticeRead_NoLanguageStillReadsTheCombinedGraph is the both-directions
// leg for all five arms. Without it every assertion above is satisfied by an
// implementation that refuses every practice read.
func TestPracticeRead_NoLanguageStillReadsTheCombinedGraph(t *testing.T) {
	for arm, args := range map[string]map[string]any{
		"browse":      {"graph": "practice", "type": "pattern"},
		"search":      {"graph": "practice", "text": "pool"},
		"modules":     {"graph": "practice", "mode": "modules"},
		"style_index": {"graph": "practice", "mode": "style_index"},
	} {
		t.Run(arm, func(t *testing.T) {
			handled, res := drivePracticeSelector(t, args)
			require.True(t, handled)
			assert.False(t, res.IsError,
				"an unselected practice read addresses the combined graph: %s", textBodyTools(res))
		})
	}

	// THE STATS ARM IS ASSERTED DIFFERENTLY BECAUSE THE HARNESS CANNOT SERVE IT.
	// This package's practice fake carries no Stats RPC, so an unselected stats
	// read fails with "unimplemented" here whatever the selector rule does. The
	// claim that is still available, and the one this leg needs, is that the
	// failure is the MISSING SEAM rather than the selector refusal — a refusal
	// installed for every value including the absent one would change this body.
	t.Run("stats", func(t *testing.T) {
		handled, res := drivePracticeSelector(t, map[string]any{"graph": "practice", "mode": "stats"})
		require.True(t, handled)
		assert.NotContains(t, textBodyTools(res), "does not accept `language`",
			"an unselected stats read is not refused for a selector it did not supply")
	})
}

// TestPracticeRead_RefusalCarriesNoLegacyNotice pins the ABSENCE half of
// requirement 5 on the read path: the legacy-corpus notice is gone rather than
// reworded, so no read renders a sentence about pre-singleton graphs.
func TestPracticeRead_RefusalCarriesNoLegacyNotice(t *testing.T) {
	handled, res := drivePracticeSelector(t, map[string]any{
		"graph": "practice", "type": "pattern",
	})
	require.True(t, handled)
	require.False(t, res.IsError, "control: the unselected browse is served: %s", textBodyTools(res))
	body := textBodyTools(res)
	assert.NotContains(t, body, "pre-singleton", "no read speaks of the pre-singleton graphs")
	assert.NotContains(t, body, "LEGACY practice graph", "and none carries the legacy notice")
}
