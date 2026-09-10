// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// The three answers an analyze_usage run can give when it folds no record, and the axis the
// selector work runs inside. A report, a refusal naming what was searched, and the
// cold-cache hint have to be told apart by their TEXT, and an operation outside the
// vocabulary has to be refused as one whatever selector accompanies it. Every test here
// drives a real transcriptanalytics.Service over a real parquet cache in t.TempDir(); the
// fixture helpers live in intercept_analyze_usage_lane_test.go.
// TestInterceptAnalyzeUsage_EmptyPopulationRefusesNamingWhatItSearched is requirement 3's
// middle case, over every scope: the cache HOLDS lanes and the selected population is empty.
//
// Before this, every one of these returned the cold-cache "--seed" hint, so a wrong selector
// and an empty cache were the same response and an operator who mistyped a lane id was told
// to re-seed a cache that was already full. Each row asserts the scope, the value searched
// and both corpus numbers, and asserts the hint is ABSENT — that absence is what makes the
// three responses distinguishable rather than merely different.
func TestInterceptAnalyzeUsage_EmptyPopulationRefusesNamingWhatItSearched(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	id := laneCacheID(laneHexA)
	populated := laneCacheDeps(t, map[string][]transcripts.Row{
		"SA":          {laneMainRow("SA", base)},
		"agent-" + id: {laneRow("SA", id, base.Add(time.Second))},
	})

	cases := []struct {
		name  string
		args  string
		scope string
		deps  ClientDeps
		want  []string
		lanes string
	}{
		{
			name: "single with an unknown name", scope: "single", deps: populated,
			args: `{"operation":"run-detectors","scope":"single","agent":"reviewer"}`,
			want: []string{"reviewer"}, lanes: "lane_count=2",
		},
		{
			name: "single with an unknown cache lane id", scope: "single", deps: populated,
			args: `{"operation":"run-detectors","scope":"single","agent":"` + laneCacheID(laneHexB) + `"}`,
			want: []string{laneCacheID(laneHexB)}, lanes: "lane_count=2",
		},
		{
			name: "single with the spawn-result spelling", scope: "single", deps: populated,
			args: `{"operation":"run-detectors","scope":"single","agent":"planner@session-7793c99e"}`,
			want: []string{"planner@session-7793c99e"}, lanes: "lane_count=2",
		},
		{
			name: "session-tree with a session that matches no row", scope: "session-tree", deps: populated,
			args: `{"operation":"run-detectors","scope":"session-tree","session":"SZ"}`,
			want: []string{"SZ"}, lanes: "lane_count=2",
		},
		{
			name: "single with a session that matches no row", scope: "single", deps: populated,
			args: `{"operation":"run-detectors","scope":"single","session":"SZ"}`,
			want: []string{"SZ"}, lanes: "lane_count=2",
		},
		{
			name: "time-range whose bounds hold no record", scope: "time-range", deps: populated,
			args: `{"operation":"run-detectors","scope":"time-range","since":"2020-01-01T00:00:00Z","until":"2020-01-02T00:00:00Z"}`,
			want: []string{"since=2020-01-01T00:00:00Z", "until=2020-01-02T00:00:00Z"}, lanes: "lane_count=2",
		},
		{
			name:  "all over a cache whose rows are all non-analyzable",
			scope: "all",
			deps: laneCacheDeps(t, map[string][]transcripts.Row{
				"SM": {
					{Model: "m", SessionID: "SM", RecordTS: base, IsMeta: true, InputTokens: 9},
					{Model: "<synthetic>", SessionID: "SM", RecordTS: base.Add(time.Second), InputTokens: 9},
				},
			}),
			args: `{"operation":"run-detectors","scope":"all"}`,
			want: []string{"takes no selector"}, lanes: "lane_count=1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handled, body, isErr := callAnalyzeUsage(t, tc.deps, tc.args)
			require.True(t, handled)
			require.True(t, isErr, "an empty population on a populated cache is a refusal; got %s", body)
			assert.Contains(t, body, `"`+tc.scope+`"`, "the refusal names the scope it ran")
			for _, want := range tc.want {
				assert.Contains(t, body, want, "the refusal names the selector or bounds it searched")
			}
			assert.Contains(t, body, tc.lanes, "and the corpus lane count, which says the cache is not empty")
			assert.Contains(t, body, "record_count=0", "and the record count the selector matched")
			assert.NotContains(t, body, "--seed", "the cold-cache hint belongs to an EMPTY cache only")
		})
	}

	// The known-positive on the same instrument: a selector that DOES match returns a report
	// over the very same cache, so the refusals above are about the selectors rather than a
	// broken fixture or a branch that refuses everything.
	handled, body, isErr := callAnalyzeUsage(t, populated, `{"operation":"run-detectors","scope":"single","agent":"planner"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.Contains(t, body, `"record_count":1`)
}

// TestInterceptAnalyzeUsage_EmptyCacheRendersTheHintOnEveryScope is requirement 3's third
// response, and the ordering assertion the whole design turns on.
//
// An empty cache resolves no name, so a resolver that refused an unknown name BEFORE reading
// the corpus block would answer "no lane named planner" where the requirement demands the
// --seed hint. The by-name row is what pins resolution behind the lane_count check; the
// other five rows are the same rule on the scopes that need no resolution.
func TestInterceptAnalyzeUsage_EmptyCacheRendersTheHintOnEveryScope(t *testing.T) {
	deps := laneCacheDeps(t, nil)

	for _, tc := range []struct{ name, args string }{
		{"all", `{"operation":"run-detectors","scope":"all"}`},
		{"session-tree", `{"operation":"run-detectors","scope":"session-tree","session":"SA"}`},
		{"single by session", `{"operation":"run-detectors","scope":"single","session":"SA"}`},
		{"single by cache lane id", `{"operation":"run-detectors","scope":"single","agent":"` + laneCacheID(laneHexA) + `"}`},
		{"single by name", `{"operation":"run-detectors","scope":"single","agent":"planner"}`},
		{"time-range", `{"operation":"run-detectors","scope":"time-range","since":"2026-06-01T00:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handled, body, isErr := callAnalyzeUsage(t, deps, tc.args)
			require.True(t, handled)
			require.False(t, isErr, "an empty cache is a state to report, not a caller error")
			assert.Contains(t, body, "--seed", "an empty cache renders the cold-cache hint on every scope")
		})
	}
}
