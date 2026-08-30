// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// mainRow builds one non-sidechain record for session at ts.
func mainRow(session string, ts time.Time) transcripts.Row {
	return transcripts.Row{
		Model: "m", SessionID: session, RecordTS: ts,
		ToolName: "Bash", DurationMs: 10, InputTokens: 1,
	}
}

// twoSessionTreeRows builds two independent session trees over one cache: each session has a
// main lane and one subagent lane. A subagent record carries its PARENT session id, which is
// what session-tree scope keys on.
func twoSessionTreeRows(t *testing.T) []transcripts.Row {
	t.Helper()
	base := mustTS(t, "2026-06-01T10:00:00Z")
	return []transcripts.Row{
		mainRow("SA", base),
		mainRow("SA", base.Add(time.Second)),
		subagentRow("SA", "agent-a", base.Add(2*time.Second)),
		subagentRow("SA", "agent-a", base.Add(3*time.Second)),
		mainRow("SB", base.Add(4*time.Second)),
		subagentRow("SB", "agent-b", base.Add(5*time.Second)),
		subagentRow("SB", "agent-b", base.Add(6*time.Second)),
	}
}

// TestScope_SessionTreeIncludesMainLaneAndSubagentsOnly pins the whole point of the
// session-tree scope: it is a session AND its subagents, and nothing from a sibling session.
// The two-session fixture is what makes the exclusion half falsifiable — over a one-session
// corpus, "everything" and "this session's tree" are the same answer.
func TestScope_SessionTreeIncludesMainLaneAndSubagentsOnly(t *testing.T) {
	svc := serviceOverRows(t, twoSessionTreeRows(t))

	rep, err := svc.RunDetectors(context.Background(), Filters{Scope: ScopeSessionTree, SessionID: "SA"})
	require.NoError(t, err)

	assert.Equal(t, int64(1), rep.Corpus.SessionCount, "only SA survives intake")
	assert.Equal(t, int64(4), rep.Corpus.RecordCount, "SA's 2 main rows plus agent-a's 2")
	assert.Equal(t, int64(1), rep.Corpus.AgentCount)
	require.Len(t, rep.SubagentWallTime, 1)
	assert.Equal(t, "agent-a", rep.SubagentWallTime[0].AgentID, "SB's agent-b is not in this tree")

	assert.Equal(t, string(ScopeSessionTree), rep.Corpus.Scope, "the report names the scope it resolved")
	assert.Equal(t, "SA", rep.Corpus.Selector)

	// The known-positive on the same measurement: the whole corpus really does hold both.
	all, err := svc.RunDetectors(context.Background(), Filters{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), all.Corpus.SessionCount)
	assert.Equal(t, int64(7), all.Corpus.RecordCount)
	assert.Equal(t, int64(2), all.Corpus.AgentCount)
	assert.Equal(t, string(ScopeAll), all.Corpus.Scope)
	assert.Empty(t, all.Corpus.Selector, "the whole corpus narrows by nothing, so there is nothing to name")
}

// TestScope_SingleIsolatesOneLane drives the two selectors that single accepts on the SAME
// fixture, because that is what proves they select different lanes rather than both
// resolving to whatever the fixture happens to contain.
func TestScope_SingleIsolatesOneLane(t *testing.T) {
	svc := serviceOverRows(t, twoSessionTreeRows(t))

	t.Run("by session id: the main lane alone", func(t *testing.T) {
		rep, err := svc.RunDetectors(context.Background(), Filters{Scope: ScopeSingle, SessionID: "SA"})
		require.NoError(t, err)

		assert.Equal(t, int64(2), rep.Corpus.RecordCount, "SA's two main rows, not its subagent's")
		assert.Equal(t, int64(0), rep.Corpus.AgentCount, "single-by-session excludes the sidechain lanes")
		assert.Empty(t, rep.SubagentWallTime)
		assert.Equal(t, "SA", rep.Corpus.Selector)
	})

	t.Run("by agent id: that agent's lane alone", func(t *testing.T) {
		rep, err := svc.RunDetectors(context.Background(), Filters{Scope: ScopeSingle, AgentID: "agent-b"})
		require.NoError(t, err)

		assert.Equal(t, int64(2), rep.Corpus.RecordCount, "agent-b's two rows and nothing else")
		assert.Equal(t, int64(1), rep.Corpus.AgentCount)
		require.Len(t, rep.SubagentWallTime, 1)
		assert.Equal(t, "agent-b", rep.SubagentWallTime[0].AgentID)
		assert.Equal(t, "agent-b", rep.Corpus.Selector)
	})
}

// TestScope_TimeRangeBoundsTheCorpus exercises the boundaries, which is the only part of
// this scope with a behaviour of its own: since is INCLUSIVE and until is EXCLUSIVE. The
// fixture places a row exactly on each boundary so an off-by-one in either direction moves
// the count.
func TestScope_TimeRangeBoundsTheCorpus(t *testing.T) {
	lo := mustTS(t, "2026-06-01T10:00:00Z")
	mid := mustTS(t, "2026-06-01T11:00:00Z")
	hi := mustTS(t, "2026-06-01T12:00:00Z")

	svc := serviceOverRows(t, []transcripts.Row{
		mainRow("SA", lo.Add(-time.Hour)), // before the range
		mainRow("SA", lo),                 // exactly on since: INCLUDED
		mainRow("SA", mid),                // inside
		mainRow("SA", hi),                 // exactly on until: EXCLUDED
		mainRow("SA", hi.Add(time.Hour)),  // after the range
	})

	t.Run("both sides", func(t *testing.T) {
		rep, err := svc.RunDetectors(context.Background(), Filters{Scope: ScopeTimeRange, Since: lo, Until: hi})
		require.NoError(t, err)
		assert.Equal(t, int64(2), rep.Corpus.RecordCount,
			"the row on since is kept and the row on until is dropped")
		assert.Equal(t, "2026-06-01T10:00:00Z", rep.Corpus.FirstRecordTS)
		assert.Equal(t, "2026-06-01T11:00:00Z", rep.Corpus.LastRecordTS)
		assert.Contains(t, rep.Corpus.Selector, "since=2026-06-01T10:00:00Z")
		assert.Contains(t, rep.Corpus.Selector, "until=2026-06-01T12:00:00Z")
	})

	t.Run("since only", func(t *testing.T) {
		rep, err := svc.RunDetectors(context.Background(), Filters{Scope: ScopeTimeRange, Since: mid})
		require.NoError(t, err)
		assert.Equal(t, int64(3), rep.Corpus.RecordCount, "mid, hi and the row after it")
		assert.NotContains(t, rep.Corpus.Selector, "until=")
	})

	t.Run("until only", func(t *testing.T) {
		rep, err := svc.RunDetectors(context.Background(), Filters{Scope: ScopeTimeRange, Until: mid})
		require.NoError(t, err)
		assert.Equal(t, int64(2), rep.Corpus.RecordCount, "the two rows strictly before mid")
		assert.NotContains(t, rep.Corpus.Selector, "since=")
	})

	// The known-positive: unbounded, the same fixture holds five rows.
	rep, err := svc.RunDetectors(context.Background(), Filters{})
	require.NoError(t, err)
	assert.Equal(t, int64(5), rep.Corpus.RecordCount)
}

// TestScope_RejectsUnknownScopeAndMissingSelector asserts that every unusable combination
// ERRORS. The failure this guards is not a crash — it is a scope that silently widens to the
// whole corpus and returns a perfectly plausible report about the wrong population, which
// the caller has no way to detect. So each case asserts an error AND that the corpus was
// never read.
func TestScope_RejectsUnknownScopeAndMissingSelector(t *testing.T) {
	svc := serviceOverRows(t, twoSessionTreeRows(t))
	since := mustTS(t, "2026-06-01T10:00:00Z")

	cases := []struct {
		name           string
		filters        Filters
		wantVocabulary bool
	}{
		{"unknown scope", Filters{Scope: "everything"}, true},
		{"session-tree with no session", Filters{Scope: ScopeSessionTree}, false},
		{"session-tree with an agent", Filters{Scope: ScopeSessionTree, SessionID: "SA", AgentID: "agent-a"}, true},
		{"single with neither selector", Filters{Scope: ScopeSingle}, false},
		{"single with both selectors", Filters{Scope: ScopeSingle, SessionID: "SA", AgentID: "agent-a"}, false},
		{"single with a time bound", Filters{Scope: ScopeSingle, SessionID: "SA", Since: since}, true},
		{"time-range with neither bound", Filters{Scope: ScopeTimeRange}, false},
		{"time-range with a session", Filters{Scope: ScopeTimeRange, Since: since, SessionID: "SA"}, true},
		{"all with a session", Filters{Scope: ScopeAll, SessionID: "SA"}, true},
		{"empty scope with an agent", Filters{AgentID: "agent-a"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep, err := svc.RunDetectors(context.Background(), tc.filters)
			require.Error(t, err, "an unusable scope must never widen to the whole corpus")
			assert.Nil(t, rep, "and must not return a report at all")
			if tc.wantVocabulary {
				for _, v := range ScopeValues() {
					assert.Contains(t, err.Error(), v,
						"the error names the accepted vocabulary so the caller can correct the call")
				}
			}
			assert.True(t, strings.HasPrefix(err.Error(), "transcriptanalytics: "),
				"errors are attributed to this package; got %q", err.Error())
		})
	}

	// The known-positive: the same analyzer, with a VALID combination, returns a report.
	rep, err := svc.RunDetectors(context.Background(), Filters{Scope: ScopeSingle, SessionID: "SA"})
	require.NoError(t, err)
	require.NotNil(t, rep)
	assert.Positive(t, rep.Corpus.RecordCount, "so the error cases above are rejections, not a broken fixture")
}
