// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// The OPERATION axis of analyze_usage, which the selector work runs inside: where the
// operation check sits between argument validation and lane resolution, and that the second
// operation honors the selector rules the first one is asserted on. The fixture helpers
// live in intercept_analyze_usage_lane_test.go.
// TestInterceptAnalyzeUsage_UnknownOperationIsRefusedBeforeAnySelectorWork pins WHERE the
// operation check sits between the two things that can also answer a call: argument
// validation, which reads no cache, and lane resolution, which reads the cache and can
// return a selector refusal or the cold-cache hint on its own.
//
// It sits between them, and the matrix below is that ordering rather than one row of it.
// Ahead of resolution, because a caller who mistyped "run-detectors" was being told their
// SELECTOR was wrong, or on an empty cache to re-seed a cache that was full — the
// mis-attribution requirement 3 removes, arriving on a different axis. BEHIND argument
// validation, because no requirement names operation-versus-argument precedence, so a call
// carrying an unknown operation AND unusable arguments must answer exactly as it did before
// this work: with the argument. Each expected string is written out rather than composed
// from the helper the code uses, and each is what the pre-change tree returned.
func TestInterceptAnalyzeUsage_UnknownOperationIsRefusedBeforeAnySelectorWork(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	id := laneCacheID(laneHexA)
	const wantOpErr = `analyze_usage: unknown operation "bogus" — valid operations: run-detectors, recommend`

	// Argument validity x expected answer. Every row here is byte-identical to the base tree's
	// answer; what this change adds is the resolution step that would otherwise have taken the
	// valid row, so the row staying unchanged is the assertion.
	args := []struct {
		name string
		call string
		want string
	}{
		{
			"valid arguments — the operation answers",
			`{"operation":"bogus","scope":"single","agent":"reviewer"}`,
			wantOpErr,
		},
		{
			"an invalid scope — the argument answers, as it did before",
			`{"operation":"bogus","scope":"bogus-scope"}`,
			`transcriptanalytics: unknown scope "bogus-scope"; accepted values are all, session-tree, single, time-range`,
		},
		{
			"a selector the scope does not consume — the argument answers",
			`{"operation":"bogus","scope":"all","session":"SA"}`,
			`transcriptanalytics: scope "all" does not accept session, agent, since and until; accepted scope values are all, session-tree, single, time-range`,
		},
	}
	// Population. Neither the operation gate nor argument validation reads the cache, so
	// every cell must answer identically on both — which is also what proves the gate is not
	// standing where resolution stands.
	pops := []struct {
		name string
		deps ClientDeps
	}{
		{"a populated cache", laneCacheDeps(t, map[string][]transcripts.Row{
			"agent-" + id: {laneRow("SA", id, base)},
		})},
		{"an empty cache", laneCacheDeps(t, nil)},
	}

	for _, pop := range pops {
		for _, arg := range args {
			t.Run(pop.name+" / "+arg.name, func(t *testing.T) {
				handled, body, isErr := callAnalyzeUsage(t, pop.deps, arg.call)
				require.True(t, handled)
				require.True(t, isErr, "every cell here is a caller error, not a cache state")
				assert.Equal(t, arg.want, body)
				assert.NotContains(t, body, "--seed")
			})
		}
	}

	// The known-positive on the same instrument: a VALID operation over the same selector
	// still reaches the selector logic, so the gate refuses the operation rather than
	// everything that carries an agent.
	handled, body, isErr := callAnalyzeUsage(t, laneCacheDeps(t, map[string][]transcripts.Row{
		"agent-" + id: {laneRow("SA", id, base)},
	}), `{"operation":"run-detectors","scope":"single","agent":"reviewer"}`)
	require.True(t, handled)
	require.True(t, isErr)
	assert.Contains(t, body, "matched no records", "control: the selector refusal is still reachable")

	// The EMPTY operation is run-detectors' documented default spelling and predates this
	// gate, so the gate must admit it rather than refuse it as unknown.
	handled, body, isErr = callAnalyzeUsage(t, laneCacheDeps(t, map[string][]transcripts.Row{
		"agent-" + id: {laneRow("SA", id, base)},
	}), `{"operation":"","scope":"single","agent":"planner"}`)
	require.True(t, handled)
	require.False(t, isErr, "an omitted operation still runs detectors; got %s", body)
	assert.Contains(t, body, `"selector":"`+id+`"`)
}

// TestInterceptAnalyzeUsage_RecommendHonorsTheLaneSelector drives the SECOND operation over
// the same selector axis the first one is asserted on.
//
// recommend resolves names and refuses empty populations through exactly the same code, and
// that is the point: with no test here, reverting recommend's arm to the cold-cache hint, or
// resolving names for run-detectors only, both left the suite green. Each row below is one of
// those two mutations' red.
func TestInterceptAnalyzeUsage_RecommendHonorsTheLaneSelector(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	idA, idB := laneCacheID(laneHexA), laneCacheID(laneHexB)
	populated := laneCacheDeps(t, map[string][]transcripts.Row{
		"agent-" + soloLaneID: {laneRow("SA", soloLaneID, base), laneRow("SA", soloLaneID, base.Add(time.Second))},
		"agent-" + idA:        {laneRow("SA", idA, base.Add(2*time.Second))},
		"agent-" + idB:        {laneRow("SB", idB, base.Add(3*time.Second))},
	})

	t.Run("a unique name resolves and the report renders", func(t *testing.T) {
		handled, body, isErr := callAnalyzeUsage(t, populated, `{"operation":"recommend","scope":"single","agent":"solo"}`)
		require.True(t, handled)
		require.False(t, isErr, body)
		assert.Contains(t, body, `"detectors"`, "recommend renders the detector report it resolved")
		assert.Contains(t, body, `"selector":"`+soloLaneID+`"`, "under the resolved cache lane id")
		assert.Contains(t, body, `"recommendations"`)
	})

	t.Run("an ambiguous name is refused with every candidate", func(t *testing.T) {
		handled, body, isErr := callAnalyzeUsage(t, populated, `{"operation":"recommend","scope":"single","agent":"planner"}`)
		require.True(t, handled)
		require.True(t, isErr, "recommend must not pick a lane either; got %s", body)
		for _, want := range []string{idA, idB, "SA", "SB"} {
			assert.Contains(t, body, want)
		}
		assert.NotContains(t, body, "--seed")
	})

	// The two empty-population rows below reach the refusal by DIFFERENT paths, and one of
	// them is not a substitute for the other. An unresolvable NAME is answered by the
	// resolver, above the operation switch; an unmatched cache lane id or session runs a
	// report and is answered by recommend's OWN reportEmpty arm. A suite carrying only the
	// name row leaves that arm free to revert to the cold-cache hint.
	t.Run("an unresolvable name is refused with the corpus block", func(t *testing.T) {
		handled, body, isErr := callAnalyzeUsage(t, populated, `{"operation":"recommend","scope":"single","agent":"reviewer"}`)
		require.True(t, handled)
		require.True(t, isErr, body)
		assert.Contains(t, body, `scope "single" matched no records`)
		assert.Contains(t, body, "lane_count=3")
		assert.Contains(t, body, "record_count=0")
		assert.NotContains(t, body, "--seed", "the hint belongs to an empty cache, on this operation too")
	})

	t.Run("an unmatched cache lane id is refused by recommend's own report arm", func(t *testing.T) {
		unknownID := "aabsent-" + laneHexA
		handled, body, isErr := callAnalyzeUsage(t, populated,
			`{"operation":"recommend","scope":"single","agent":"`+unknownID+`"}`)
		require.True(t, handled)
		require.True(t, isErr, "an id needs no resolution, so this reaches recommend's own arm; got %s", body)
		assert.Contains(t, body, `scope "single" matched no records`)
		assert.Contains(t, body, unknownID, "and the refusal names the id it searched")
		assert.Contains(t, body, "lane_count=3")
		assert.Contains(t, body, "record_count=0")
		assert.NotContains(t, body, "--seed")
	})

	t.Run("an unmatched session-tree session is refused on this operation too", func(t *testing.T) {
		handled, body, isErr := callAnalyzeUsage(t, populated, `{"operation":"recommend","scope":"session-tree","session":"SZ"}`)
		require.True(t, handled)
		require.True(t, isErr, body)
		assert.Contains(t, body, `scope "session-tree" matched no records`)
		assert.Contains(t, body, "SZ")
		assert.Contains(t, body, "lane_count=3")
		assert.NotContains(t, body, "--seed")
	})

	t.Run("an empty cache still renders the hint", func(t *testing.T) {
		handled, body, isErr := callAnalyzeUsage(t, laneCacheDeps(t, nil), `{"operation":"recommend","scope":"single","agent":"planner"}`)
		require.True(t, handled)
		require.False(t, isErr, body)
		assert.Contains(t, body, "--seed", "resolution sits behind the lane-count check on this operation too")
	})
}
