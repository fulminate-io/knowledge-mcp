// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcriptanalytics"
	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// These tests drive the intercept against a REAL transcriptanalytics.Service over a real
// parquet cache in t.TempDir(), not against the canned-report fake. The behaviour under test
// is a negotiation between the two sides — the intercept resolves a name and reads the
// corpus block the analyzer computed from its own glob — so a fake on either side would be
// asserting the fixture rather than the seam.

const (
	laneHexA = "0123456789abcdef"
	laneHexB = "fedcba9876543210"
	// soloLaneID is a second lane under a DIFFERENT spawn name, so a fixture can hold an
	// unambiguous name and an ambiguous one at once.
	soloLaneID = "asolo-" + laneHexB
)

// laneName is the spawn name every fixture lane below is built from; laneCacheID spells that
// lane's id the way the CLI does: "a" + the spawn name + "-" + 16 hex.
const laneName = "planner"

func laneCacheID(hex string) string { return "a" + laneName + "-" + hex }

// laneRow builds one subagent record for a lane under its parent session.
func laneRow(session, agentID string, ts time.Time) transcripts.Row {
	return transcripts.Row{
		Model: "m", SessionID: session, RecordTS: ts,
		IsSidechain: true, AgentID: agentID, SubagentType: "worker", InputTokens: 1,
	}
}

// laneMainRow builds one non-sidechain record for a session's main lane.
func laneMainRow(session string, ts time.Time) transcripts.Row {
	return transcripts.Row{
		Model: "m", SessionID: session, RecordTS: ts,
		ToolName: "Bash", DurationMs: 10, InputTokens: 1,
	}
}

// laneCacheDeps writes one parquet per entry into a temp cache root (the cache's real
// one-file-per-lane layout) and returns ClientDeps carrying a real analyzer over it. A nil
// map is the EMPTY cache. Nothing here touches the operator's cache or any daemon.
func laneCacheDeps(t *testing.T, files map[string][]transcripts.Row) ClientDeps {
	t.Helper()
	root := t.TempDir()
	for name, rows := range files {
		dir := filepath.Join(root, "claude")
		require.NoError(t, os.MkdirAll(dir, 0o750))
		f, err := os.Create(filepath.Join(dir, name+".parquet")) //nolint:gosec // t.TempDir() path.
		require.NoError(t, err)
		require.NoError(t, transcripts.WriteSessionParquet(rows, f))
		require.NoError(t, f.Close())
	}
	svc, err := transcriptanalytics.NewService(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	return analyzeUsageTestDeps{analyzer: svc}
}

// oneLaneCache is the shared single-lane fixture: session SA's main lane plus one subagent
// lane spawned as "planner".
func oneLaneCache(t *testing.T) ClientDeps {
	t.Helper()
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	id := laneCacheID(laneHexA)
	return laneCacheDeps(t, map[string][]transcripts.Row{
		"SA": {laneMainRow("SA", base), laneMainRow("SA", base.Add(time.Second))},
		"agent-" + id: {
			laneRow("SA", id, base.Add(2*time.Second)),
			laneRow("SA", id, base.Add(3*time.Second)),
		},
	})
}

// TestInterceptAnalyzeUsage_NameSelectsTheSameLaneAsTheID is requirement 1: the name an
// operator was given at spawn selects the lane the cache id selects, with the same report.
//
// The two bodies are compared from ONE process over ONE cache root because the report
// renders that root, so a stored golden could not exist. Comparing them alone would be a
// subject supplying its own answer key, so the by-id arm's own numbers are asserted against
// the fixture independently first.
func TestInterceptAnalyzeUsage_NameSelectsTheSameLaneAsTheID(t *testing.T) {
	deps := oneLaneCache(t)
	id := laneCacheID(laneHexA)

	handled, byID, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scope":"single","agent":"`+id+`"}`)
	require.True(t, handled)
	require.False(t, isErr, byID)
	assert.Contains(t, byID, `"record_count":2`, "the lane's two rows")
	assert.Contains(t, byID, `"agent_count":1`)
	assert.Contains(t, byID, `"selector":"`+id+`"`)
	assert.Contains(t, byID, `"lane_detail"`, "single returns the lane breakdown")

	handled, byName, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scope":"single","agent":"planner"}`)
	require.True(t, handled)
	require.False(t, isErr, byName)
	assert.Equal(t, byID, byName, "a name and the lane's cache id must return the same report, byte for byte")
}

// TestInterceptAnalyzeUsage_TheResolutionSessionDoesNotNarrowTheReport pins the session
// argument's role for a name: it scopes the LOOKUP, not the population.
//
// The fixture is a lane whose rows span two sessions, which is the only shape that can tell
// the two readings apart. Requirement 1 says the by-name call returns what the by-id call
// returns, and the by-id call carries no session — so a session left on the filters after
// resolution would silently report a fraction of the lane under the lane's own id.
func TestInterceptAnalyzeUsage_TheResolutionSessionDoesNotNarrowTheReport(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	id := laneCacheID(laneHexA)
	deps := laneCacheDeps(t, map[string][]transcripts.Row{
		"agent-" + id: {laneRow("SA", id, base), laneRow("SB", id, base.Add(time.Second))},
	})

	handled, byID, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scope":"single","agent":"`+id+`"}`)
	require.True(t, handled)
	require.False(t, isErr, byID)
	assert.Contains(t, byID, `"record_count":2`, "the whole lane, both of its sessions")

	handled, byName, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scope":"single","agent":"planner","session":"SA"}`)
	require.True(t, handled)
	require.False(t, isErr, byName)
	assert.Equal(t, byID, byName, "the session resolved the name and then stopped applying")
}

// TestInterceptAnalyzeUsage_AmbiguousNameIsRefusedWithEveryCandidate is requirement 2's
// refusal: a spawn name is reused across sessions while the cache id is unique, so more than
// one match is refused with every candidate named. Each of the four strings is asserted on
// its own — a single substring check would pass on a message that named one candidate twice.
func TestInterceptAnalyzeUsage_AmbiguousNameIsRefusedWithEveryCandidate(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	idA, idB := laneCacheID(laneHexA), laneCacheID(laneHexB)
	deps := laneCacheDeps(t, map[string][]transcripts.Row{
		"agent-" + idA: {laneRow("SA", idA, base), laneRow("SC", idA, base.Add(time.Second))},
		"agent-" + idB: {laneRow("SB", idB, base.Add(2*time.Second)), laneRow("SB", idB, base.Add(3*time.Second))},
	})

	handled, body, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scope":"single","agent":"planner"}`)
	require.True(t, handled)
	require.True(t, isErr, "an ambiguous name must never be resolved by picking one")
	for _, want := range []string{idA, idB, "SA", "SC", "SB"} {
		assert.Contains(t, body, want,
			"the refusal names every candidate id and EVERY session it carries — lane A spans two")
	}
	// The listing is ORDERED, by candidate id and then by session within a candidate. Both
	// come out of Go maps, whose iteration order is randomized per run, so without the sorts
	// this message would differ between two runs over one cache and any assertion on it
	// would be flaky rather than wrong. Asserting the order is what holds the sorts in place.
	assert.Less(t, strings.Index(body, idA), strings.Index(body, idB),
		"candidates are listed in id order, not in cache-walk order")
	assert.Less(t, strings.Index(body, `"SA"`), strings.Index(body, `"SC"`),
		"and a candidate's sessions are listed in order too")

	// The ORDER is stable across runs, which is what the two sorts buy. Both listings come
	// out of Go maps, whose iteration order is randomized per call, so ONE ordered body is
	// consistent with an unsorted resolver that got lucky — measured, a two-candidate fixture
	// reproduces the sorted order most of the time. Thirty resolutions of one cache agreeing
	// byte for byte is not.
	for i := range 30 {
		_, again, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scope":"single","agent":"planner"}`)
		require.True(t, isErr)
		require.Equal(t, body, again, "resolution %d rendered a different order; the candidate listing is unsorted", i)
	}
	assert.NotContains(t, body, "--seed", "a populated cache never renders the cold-cache hint")

	t.Run("a session narrows the name to one lane", func(t *testing.T) {
		handled, byName, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scope":"single","agent":"planner","session":"SB"}`)
		require.True(t, handled)
		require.False(t, isErr, byName)
		handled, byID, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scope":"single","agent":"`+idB+`"}`)
		require.True(t, handled)
		require.False(t, isErr, byID)
		assert.Equal(t, byID, byName, "the session-scoped name resolves to SB's lane and reports as that id does")
	})

	t.Run("a name absent from the named session is refused, naming the session", func(t *testing.T) {
		handled, body, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scope":"single","agent":"planner","session":"SD"}`)
		require.True(t, handled)
		require.True(t, isErr)
		assert.Contains(t, body, "SD", "the refusal names the session it searched")
		assert.Contains(t, body, "planner")
		assert.NotContains(t, body, "--seed")
	})
}

// TestInterceptAnalyzeUsage_ANameAmbiguousWithinOneSessionIsStillRefused covers the cell a
// session cannot rescue: two lanes spawned under one name in ONE session.
//
// The session is the resolution scope, so narrowing by it is the documented way out of an
// ambiguity — and here it does not work, because both candidates are inside it. A resolver
// that treated "a session was supplied" as license to pick would silently analyze one of two
// lanes; the refusal has to survive the narrowing.
func TestInterceptAnalyzeUsage_ANameAmbiguousWithinOneSessionIsStillRefused(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	idA, idB := laneCacheID(laneHexA), laneCacheID(laneHexB)
	deps := laneCacheDeps(t, map[string][]transcripts.Row{
		"agent-" + idA: {laneRow("SA", idA, base)},
		"agent-" + idB: {laneRow("SA", idB, base.Add(time.Second))},
	})

	for _, args := range []string{
		`{"operation":"run-detectors","scope":"single","agent":"planner"}`,
		`{"operation":"run-detectors","scope":"single","agent":"planner","session":"SA"}`,
	} {
		handled, body, isErr := callAnalyzeUsage(t, deps, args)
		require.True(t, handled)
		require.True(t, isErr, "two lanes of one name in one session stay ambiguous; got %s", body)
		assert.Contains(t, body, idA)
		assert.Contains(t, body, idB)
		assert.Contains(t, body, "SA")
	}

	// The known-positive: the same cache resolves the OTHER lane's name, so the refusals
	// above are about the ambiguity rather than a resolver that refuses everything.
	deps2 := laneCacheDeps(t, map[string][]transcripts.Row{
		"agent-" + soloLaneID: {laneRow("SA", soloLaneID, base)},
	})
	handled, body, isErr := callAnalyzeUsage(t, deps2, `{"operation":"run-detectors","scope":"single","agent":"solo"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.Contains(t, body, `"selector":"`+soloLaneID+`"`)
}
