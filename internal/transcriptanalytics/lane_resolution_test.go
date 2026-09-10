// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// The cache spells a subagent lane's id as "a" + the spawn name + "-" + 16 hex, so a
// fixture lane id is built the way the cache builds one rather than typed out.
const (
	hexA = "0123456789abcdef"
	hexB = "fedcba9876543210"
)

func laneID(name, hex string) string { return "a" + name + "-" + hex }

// laneCache writes one parquet per entry (the cache's real one-file-per-lane layout, which
// a single-file fixture would not reproduce) and returns an analyzer over it. A nil map is
// the EMPTY cache, which is a distinct input class rather than a degenerate one.
func laneCache(t *testing.T, files map[string][]transcripts.Row) *Service {
	t.Helper()
	root := t.TempDir()
	for name, rows := range files {
		writeSessionFixture(t, root, "claude", name, rows)
	}
	svc, err := NewService(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// TestIsLaneID_DistinguishesTheCacheSpellingFromASpawnName pins the predicate the whole
// name/id fork turns on.
//
// The defect it catches is a predicate loose enough to read a NAME as an id: that lane
// would never be resolved and the caller would get "matched no records" for a lane that is
// sitting in the cache. The spawn-result spelling is in the table because it is the string
// an operator actually pastes, and it must fall to the NAME arm whole — never half-resolve
// on the text before the "@".
func TestIsLaneID_DistinguishesTheCacheSpellingFromASpawnName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{laneID("planner", hexA), true},
		{laneID("fix-link-hub-3", "415e58cc2a3e8556"), true},
		{"planner", false},
		{"fix-link-hub-3", false},
		{"planner@session-7793c99e", false},
		{"agent-a", false},
		{"a-" + hexA, false},
		{"aplanner-0123456789ABCDEF", false},
		{"planner-" + hexA, false},
		{"", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, IsLaneID(tc.in), "IsLaneID(%q)", tc.in)
	}
}

// TestResolveLaneName_ResolvesTheSpawnNameToTheCacheLaneID is requirement 1's analyzer half:
// the name an operator knows becomes the id the cache keys the lane by.
//
// Its known-positive control is the miss on the SAME cache: a name that is not there
// resolves to no candidate while the cache still reports its lanes, so an empty candidate
// list is a measured absence rather than a resolver that returns nothing for everything.
func TestResolveLaneName_ResolvesTheSpawnNameToTheCacheLaneID(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")
	svc := laneCache(t, map[string][]transcripts.Row{
		"agent-" + laneID("planner", hexA): {
			subagentRow("SA", laneID("planner", hexA), base),
			subagentRow("SA", laneID("planner", hexA), base.Add(time.Second)),
		},
	})

	res, err := svc.ResolveLaneName(context.Background(), "planner", "")
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.LaneCount, "the cache holds one lane file")
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, laneID("planner", hexA), res.Candidates[0].AgentID)
	assert.Equal(t, []string{"SA"}, res.Candidates[0].SessionIDs, "the lane's parent session comes from its rows")

	miss, err := svc.ResolveLaneName(context.Background(), "reviewer", "")
	require.NoError(t, err)
	assert.Empty(t, miss.Candidates, "control: an absent name resolves to nothing")
	assert.Equal(t, int64(1), miss.LaneCount, "and the cache is still reported as populated")
}

// TestResolveLaneName_AmbiguityReturnsEveryCandidate is requirement 2's analyzer half. A
// spawn name is REUSED across sessions while the cache id is unique, so the resolver's job
// on more than one match is to hand back every candidate with its session — picking the
// first would silently analyze the wrong lane.
func TestResolveLaneName_AmbiguityReturnsEveryCandidate(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")
	svc := laneCache(t, map[string][]transcripts.Row{
		"agent-" + laneID("planner", hexA): {subagentRow("SA", laneID("planner", hexA), base)},
		"agent-" + laneID("planner", hexB): {subagentRow("SB", laneID("planner", hexB), base.Add(time.Second))},
	})

	res, err := svc.ResolveLaneName(context.Background(), "planner", "")
	require.NoError(t, err)
	assert.Equal(t, int64(2), res.LaneCount)
	require.Len(t, res.Candidates, 2, "both lanes carry the name")
	assert.Equal(t, laneID("planner", hexA), res.Candidates[0].AgentID, "candidates are ordered by id, not by file walk order")
	assert.Equal(t, []string{"SA"}, res.Candidates[0].SessionIDs)
	assert.Equal(t, laneID("planner", hexB), res.Candidates[1].AgentID)
	assert.Equal(t, []string{"SB"}, res.Candidates[1].SessionIDs)
}

// TestResolveLaneName_SessionNarrowsTheResolutionScope is the other half of requirement 2:
// the session argument is what turns an ambiguous name into one lane.
func TestResolveLaneName_SessionNarrowsTheResolutionScope(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")
	svc := laneCache(t, map[string][]transcripts.Row{
		"agent-" + laneID("planner", hexA): {subagentRow("SA", laneID("planner", hexA), base)},
		"agent-" + laneID("planner", hexB): {subagentRow("SB", laneID("planner", hexB), base.Add(time.Second))},
	})

	res, err := svc.ResolveLaneName(context.Background(), "planner", "SB")
	require.NoError(t, err)
	require.Len(t, res.Candidates, 1, "the session admits one of the two lanes")
	assert.Equal(t, laneID("planner", hexB), res.Candidates[0].AgentID)

	// A session that holds no lane of that name resolves to nothing rather than to the
	// lane in the other session.
	none, err := svc.ResolveLaneName(context.Background(), "planner", "SC")
	require.NoError(t, err)
	assert.Empty(t, none.Candidates)
	assert.Equal(t, int64(2), none.LaneCount)
}

// TestResolveLaneName_OneLaneAcrossTwoSessionsStaysOneCandidate covers the shape a
// per-(id, session) candidate list would get wrong: one lane whose rows carry two parent
// sessions is still ONE lane, so it resolves rather than reading as ambiguous, and the
// refusal it would appear in names both sessions rather than picking one.
func TestResolveLaneName_OneLaneAcrossTwoSessionsStaysOneCandidate(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")
	id := laneID("planner", hexA)
	svc := laneCache(t, map[string][]transcripts.Row{
		"agent-" + id: {subagentRow("SB", id, base), subagentRow("SA", id, base.Add(time.Second))},
	})

	res, err := svc.ResolveLaneName(context.Background(), "planner", "")
	require.NoError(t, err)
	require.Len(t, res.Candidates, 1, "one agent id is one lane however many sessions its rows name")
	assert.Equal(t, []string{"SA", "SB"}, res.Candidates[0].SessionIDs, "every session it carries, sorted")
}

// TestResolveLaneName_EmptyCacheReportsZeroLanesAndResolvesNothing is the ORDERING signal
// requirement 3 rests on: an empty cache must be distinguishable from a name that matched
// nothing, so the resolver reports the lane count from the glob before it can refuse a
// name. The caller renders the cold-cache hint on this, never an unknown-name refusal.
func TestResolveLaneName_EmptyCacheReportsZeroLanesAndResolvesNothing(t *testing.T) {
	svc := laneCache(t, nil)

	res, err := svc.ResolveLaneName(context.Background(), "planner", "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), res.LaneCount, "an empty cache reports no lanes")
	assert.Empty(t, res.Candidates)
}

// TestResolveLaneName_RefusesAnEmptyName keeps the resolver from answering a question no
// caller asked: an empty name would build the prefix "a-" and match any lane whose id
// begins with it.
func TestResolveLaneName_RefusesAnEmptyName(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")
	svc := laneCache(t, map[string][]transcripts.Row{
		"agent-" + laneID("planner", hexA): {subagentRow("SA", laneID("planner", hexA), base)},
	})

	_, err := svc.ResolveLaneName(context.Background(), "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transcriptanalytics: ", "errors are attributed to this package")
}

// TestScope_SingleAdmitsASessionScopedLaneName is requirement 2's validator half.
//
// single refuses session and agent together for the cache ID form, because an id is unique
// and a session alongside it selects nothing extra. For a NAME the session is the
// RESOLUTION SCOPE, so the pair describes a population and is admitted. Both arms are here
// because the difference between them is the whole rule.
func TestScope_SingleAdmitsASessionScopedLaneName(t *testing.T) {
	t.Run("a name plus a session is admitted", func(t *testing.T) {
		err := Filters{Scope: ScopeSingle, SessionID: "SA", AgentID: "planner"}.Validate()
		assert.NoError(t, err, "the session is a name's resolution scope, not an unusable combination")
	})

	t.Run("the cache id form still refuses the pair", func(t *testing.T) {
		err := Filters{Scope: ScopeSingle, SessionID: "SA", AgentID: laneID("planner", hexA)}.Validate()
		require.Error(t, err, "an id is unique; a session alongside it narrows nothing")
		assert.Contains(t, err.Error(), "both were given")
	})

	// The known-positive on the same instrument: each selector ALONE is still admitted, so
	// the refusal above is about the pair rather than about Validate refusing everything.
	assert.NoError(t, Filters{Scope: ScopeSingle, SessionID: "SA"}.Validate())
	assert.NoError(t, Filters{Scope: ScopeSingle, AgentID: laneID("planner", hexA)}.Validate())
}

// TestResolveLaneName_AnUnreadableCacheFileIsAnError drives the resolver's one error return.
//
// The failure it forbids is silent: a cache file the decoder cannot read must surface as an
// error, never as a shorter candidate list, because a swallowed read turns "this file could
// not be read" into "no lane by that name" — and the caller acts on the second by fixing a
// selector that was right. The control runs first, over the same cache without the bad file,
// so the error below is attributable to the file rather than to a resolver that errors on
// everything.
func TestResolveLaneName_AnUnreadableCacheFileIsAnError(t *testing.T) {
	base := mustTS(t, "2026-06-01T10:00:00Z")
	id := laneID("planner", hexA)
	root := t.TempDir()
	writeSessionFixture(t, root, "claude", "agent-"+id, []transcripts.Row{subagentRow("SA", id, base)})
	svc, err := NewService(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	control, err := svc.ResolveLaneName(context.Background(), "planner", "")
	require.NoError(t, err)
	require.Len(t, control.Candidates, 1, "control: this cache resolves the name before the bad file lands")

	bad := filepath.Join(root, "claude", "agent-abroken-0123456789abcdef.parquet")
	require.NoError(t, os.WriteFile(bad, []byte("not a parquet file"), 0o600))

	_, err = svc.ResolveLaneName(context.Background(), "planner", "")
	require.Error(t, err, "an unreadable cache file must surface, never shorten the candidate list")
	assert.Contains(t, err.Error(), "transcriptanalytics: ",
		"and be attributed to this package, as every other error it returns is")
}
