// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_query_builtin_stats_test.go gates the checks / transformers stats arm
// and the vocabulary refusal that shares its mode.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// statsCorpusKey is how the double keys a graph: EXACTLY the wire spelling a
// selector carries. checks is a singleton whose selector policy admits no set
// name, so its key carries an empty name; transformers carries its real bucket
// name. A caller that canonicalized the wire Target — sending "default" where the
// wire must carry "" — misses the key and reads an EMPTY corpus, which is the
// silent zero this test is built to catch.
type statsCorpusKey struct{ graph, name string }

// statsCorpusFake serves BOTH the Stats RPC and the type-browse Execute from ONE
// seeded corpus, keyed by the wire selector. That is what makes the agreement
// assertion below meaningful rather than circular: the two measurements are
// independent CALLS taking independent code paths through the arm, but they read
// the same store, exactly as they would in production. A wrong selector on either
// path lands on a different key and the two disagree.
type statsCorpusFake struct {
	corpus map[statsCorpusKey]map[string]int64
	// statsTargets records every Stats selector, so the wire spelling itself is
	// observable rather than inferred from the counts.
	statsTargets []*knowledgev1.GraphSelector
}

func (f *statsCorpusFake) Stats(_ context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	sel := req.GetTarget()
	f.statsTargets = append(f.statsTargets, sel)
	byType := f.corpus[statsCorpusKey{graph: sel.GetGraph(), name: sel.GetName()}]
	var nodes int64
	for _, n := range byType {
		nodes += n
	}
	return &knowledgev1.StatsResponse{GraphStats: &knowledgev1.GraphStats{
		NodeCount:   int32(nodes),
		NodesByType: byType,
	}}, nil
}

// Execute answers the per-type browse: the same corpus, counted the other way.
func (f *statsCorpusFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	sel := req.GetTarget()
	q := req.GetQuery()
	byType := f.corpus[statsCorpusKey{graph: sel.GetGraph(), name: sel.GetName()}]
	want := q.GetSelection().GetNodeType()
	nodes := make([]*knowledgev1.Node, 0, byType[want])
	for i := range byType[want] {
		nodes = append(nodes, &knowledgev1.Node{Id: want + "-" + string(rune('a'+i)), Type: want})
	}
	resp := enginetest.ResponseWithNodes(nodes...)
	resp.Total = byType[want]
	return resp, nil
}

// newStatsCorpusFake seeds the two served graphs plus the knowledge default, so
// the both-directions leg has a real corpus to read too.
func newStatsCorpusFake() *statsCorpusFake {
	return &statsCorpusFake{corpus: map[statsCorpusKey]map[string]int64{
		{graph: "checks", name: ""}:              {"finding": 6, "example": 12},
		{graph: "transformers", name: "recipes"}: {"recipe": 10},
		{graph: "", name: ""}:                    {"finding": 3},
	}}
}

// driveBuiltinStats runs one payload through the stats arm.
func driveBuiltinStats(t *testing.T, f *statsCorpusFake, args map[string]any) (bool, kgtools.ToolResult) {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return InterceptQueryBuiltinStats(opCtx(), interceptTestDeps{gc: f},
		kgtools.CallToolParams{Name: "query", Arguments: raw})
}

// browseTotal issues the INDEPENDENT measurement: a type-browse against the same
// graph, read through the same wire selector a caller would send. It is derived
// from the tree rather than pinned, because the corpus changes as checks are
// authored.
func browseTotal(t *testing.T, f *statsCorpusFake, graph, name, nodeType string) int64 {
	t.Helper()
	resp, err := f.Execute(context.Background(), &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{NodeType: nodeType}}},
		Target: &knowledgev1.GraphSelector{Graph: graph, Name: name},
	})
	require.NoError(t, err)
	return resp.GetTotal()
}

// statsJSON parses the json stats envelope the arm renders under format:"json".
func statsJSON(t *testing.T, res kgtools.ToolResult) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(res)), &payload),
		"format:json must emit a parseable stats envelope: %s", textBodyTools(res))
	return payload
}

// nodesByType projects the stats envelope's per-type counts as int64.
func nodesByType(t *testing.T, payload map[string]any) map[string]int64 {
	t.Helper()
	raw, ok := payload["nodes_by_type"].(map[string]any)
	require.True(t, ok, "the stats envelope carries nodes_by_type: %v", payload)
	out := make(map[string]int64, len(raw))
	for k, v := range raw {
		n, isNum := v.(float64)
		require.True(t, isNum, "count for %q is numeric", k)
		out[k] = int64(n)
	}
	return out
}

// TestStatsServedOnChecksAndTransformers (FAILS-WHEN-ABSENT) gates the SERVE path
// and the refusal path together.
//
// ITS CENTRAL LEG IS AN AGREEMENT, NOT A SUCCESS. A stats arm that keyed its wire
// selector wrong returns a WELL-FORMED render reporting zero — the silent-zero
// class a success-only assertion is green against. So the counts stats reports are
// compared against the totals an independently-issued type-browse reports in the
// SAME RUN, and both are read from the tree rather than pinned to a literal.
func TestStatsServedOnChecksAndTransformers(t *testing.T) {
	t.Run("checks stats agrees with an independent type browse", func(t *testing.T) {
		f := newStatsCorpusFake()
		handled, res := driveBuiltinStats(t, f, map[string]any{
			"graph": "checks", "mode": "stats", "format": "json",
		})
		require.True(t, handled, "checks stats is claimed")
		require.False(t, res.IsError, "and served: %s", textBodyTools(res))

		got := nodesByType(t, statsJSON(t, res))
		for _, nodeType := range []string{"finding", "example"} {
			want := browseTotal(t, f, "checks", "", nodeType)
			require.Positive(t, want, "the fixture seeds a NON-ZERO %s corpus — a zero here would make the "+
				"agreement below vacuous", nodeType)
			assert.Equal(t, want, got[nodeType],
				"stats and an independent type browse must agree on %q; a disagreement means the stats "+
					"selector reached a different graph than the browse", nodeType)
		}

		// THE WIRE SPELLING ITSELF. checks is a singleton whose selector policy
		// rejects a set name, so the Target must carry an EMPTY name — never the
		// internal default-instance key, which is the mistake that produces a
		// well-formed zero.
		require.Len(t, f.statsTargets, 1)
		assert.Equal(t, "checks", f.statsTargets[0].GetGraph())
		assert.Empty(t, f.statsTargets[0].GetName(),
			"the checks wire Target carries NO name — CanonicalInstanceName is an internal key, not a wire value")
	})

	t.Run("transformers stats agrees with an independent type browse", func(t *testing.T) {
		f := newStatsCorpusFake()
		handled, res := driveBuiltinStats(t, f, map[string]any{
			"graph": "transformers", "name": "recipes", "mode": "stats", "format": "json",
		})
		require.True(t, handled)
		require.False(t, res.IsError, "%s", textBodyTools(res))

		want := browseTotal(t, f, "transformers", "recipes", "recipe")
		require.Positive(t, want, "the fixture seeds a non-zero recipe corpus")
		assert.Equal(t, want, nodesByType(t, statsJSON(t, res))["recipe"])
		require.Len(t, f.statsTargets, 1)
		assert.Equal(t, "recipes", f.statsTargets[0].GetName(), "the transformers Target carries its bucket name")
	})

	t.Run("the name rule runs both directions", func(t *testing.T) {
		// Asserting BOTH directions in one place is what stops an implementer
		// applying one graph's name policy to the other.
		f := newStatsCorpusFake()
		_, nameless := driveBuiltinStats(t, f, map[string]any{"graph": "transformers", "mode": "stats"})
		require.True(t, nameless.IsError, "a nameless transformers stats is refused")
		assert.Contains(t, textBodyTools(nameless), `mode:"modules"`, "and names the enumeration")

		_, checks := driveBuiltinStats(t, newStatsCorpusFake(), map[string]any{"graph": "checks", "mode": "stats"})
		assert.False(t, checks.IsError,
			"checks is a SINGLETON whose selector policy rejects a set name, so a nameless call SUCCEEDS: %s",
			textBodyTools(checks))
	})

	t.Run("an unknown graph value is refused naming the value and the vocabulary", func(t *testing.T) {
		// TWO VALUES, and they are not interchangeable: `all` is a plausible word a
		// caller reasonably tries, the other is a typo stand-in, and an
		// implementation could special-case one without the other.
		for _, graph := range []string{"all", "nonsense-graph-xyz"} {
			handled, res := driveBuiltinStats(t, newStatsCorpusFake(), map[string]any{
				"graph": graph, "mode": "stats",
			})
			require.Truef(t, handled, "%q is CLAIMED and refused, never left to the generic deny", graph)
			require.True(t, res.IsError)
			body := textBodyTools(res)
			assert.Containsf(t, body, graph, "the refusal names the offending value")
			assert.Contains(t, body, "valid graphs are the built-ins", "and the accepted vocabulary")
		}
	})

	t.Run("no leg is answered by the generic deny", func(t *testing.T) {
		// Asserted separately from the success and refusal legs: a refusal that
		// merely reworded while still routing to the deny would satisfy those.
		for _, args := range []map[string]any{
			{"graph": "checks", "mode": "stats"},
			{"graph": "transformers", "name": "recipes", "mode": "stats"},
			{"graph": "all", "mode": "stats"},
			{"graph": "nonsense-graph-xyz", "mode": "stats"},
		} {
			_, res := driveBuiltinStats(t, newStatsCorpusFake(), args)
			assert.NotContains(t, textBodyTools(res), "not a recognized engine-reducible shape")
		}
	})

	t.Run("the knowledge stats arm is untouched", func(t *testing.T) {
		// BOTH DIRECTIONS beyond the name pair: an over-broad arm that swallowed
		// the graphs its siblings own reddens here rather than in a distant suite.
		f := newStatsCorpusFake()
		handled, _ := driveBuiltinStats(t, f, map[string]any{"graph": "knowledge", "mode": "stats"})
		assert.False(t, handled, "the builtin-stats arm DECLINES knowledge — its own arm owns it")

		raw, err := json.Marshal(map[string]any{"graph": "knowledge", "mode": "stats"})
		require.NoError(t, err)
		kHandled, kRes := InterceptQueryStats(opCtx(), interceptTestDeps{gc: f},
			kgtools.CallToolParams{Name: "query", Arguments: raw})
		require.True(t, kHandled, "and the knowledge stats arm still claims it")
		assert.False(t, kRes.IsError, "%s", textBodyTools(kRes))
		assert.Contains(t, textBodyTools(kRes), "Knowledge Graph", "answering with its own stats render")
	})
}
