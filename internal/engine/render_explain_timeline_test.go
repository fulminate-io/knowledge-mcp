// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// TestRenderExplainCorrelations_CandidateGroups pins the group collapse on the
// two arms the surface census found: explain (a single-node edge listing) and
// correlations (a ranked whole-graph edge table). Neither is a walk, so neither
// emits the frontier line.
func TestRenderExplainCorrelations_CandidateGroups(t *testing.T) {
	const src = "a/x.go:Caller"
	const key = "a/x.go:1042:CALLS:Run"

	groupMember := func(to string) knowledgev1.Edge {
		return knowledgev1.Edge{
			FromId: src, ToId: to, Type: "CALLS",
			Method: kgtypes.EdgeMethodAmbiguousName, Evidence: key, Confidence: 1.0 / 3.0,
		}
	}
	// The real shape a cloud/linkage edge has: a genuine human-readable citation
	// that must keep rendering.
	cloudEdge := func() knowledgev1.Edge {
		return knowledgev1.Edge{
			FromId: src, ToId: "img/base:latest", Type: "BUILDS",
			Method: "tier1-dockerfile", Evidence: "Dockerfile:14 COPY src", Confidence: 0.9,
		}
	}
	names := map[string]*knowledgev1.Node{
		src:               {Id: src, SymbolName: "Caller"},
		"p/a.go:Run":      {Id: "p/a.go:Run", SymbolName: "Run", FilePath: "p/a.go", StartLine: 10, Signature: "func Run() error"},
		"p/b.go:Run":      {Id: "p/b.go:Run", SymbolName: "Run", FilePath: "p/b.go", StartLine: 20, Signature: "func Run(n int)"},
		"p/c.go:Run":      {Id: "p/c.go:Run", SymbolName: "Run", FilePath: "p/c.go", StartLine: 30, Signature: "func Run(s string)"},
		"img/base:latest": {Id: "img/base:latest", SymbolName: "base"},
	}

	allEdges := func() []knowledgev1.Edge {
		return []knowledgev1.Edge{
			groupMember("p/a.go:Run"), groupMember("p/b.go:Run"), groupMember("p/c.go:Run"), cloudEdge(),
		}
	}

	t.Run("explain_group_collapses", func(t *testing.T) {
		groups, ungrouped := GroupCandidateEdges(allEdges())
		require.Len(t, groups, 1)
		out := RenderExplainEdges("code", ungrouped, names, groups)

		assert.Contains(t, out, "one of 3 candidates")
		assert.Contains(t, out, "exactly one is the real target")
		// The bound edge still gets its own numbered block; no member does.
		assert.Equal(t, 1, strings.Count(out, "### Edge #"), "only the ungrouped edge is listed per-edge")
		for _, m := range []string{"p/a.go:Run", "p/b.go:Run", "p/c.go:Run"} {
			assert.NotContains(t, out, "### Edge #1 — Caller -> Run", "no member gets a per-edge block")
			assert.Contains(t, out, m, "every candidate is still named, inside the group block")
		}
		// Not a walk: nothing to short-circuit.
		assert.NotContains(t, out, "traversal stops at this candidate group")
	})

	t.Run("explain_suppresses_raw_key", func(t *testing.T) {
		// The group's own key and method never print raw; the cloud edge keeps
		// BOTH, which is the half that stops a blanket deletion from passing.
		groups, ungrouped := GroupCandidateEdges(allEdges())
		out := RenderExplainEdges("code", ungrouped, names, groups)
		assert.NotContains(t, out, "Evidence (raw): "+key)
		assert.Contains(t, out, "- Evidence (raw): Dockerfile:14 COPY src")
		assert.Contains(t, out, "- Method: tier1-dockerfile")
	})

	t.Run("correlations_group_collapses", func(t *testing.T) {
		edges := allEdges()
		groups, _ := GroupCandidateEdges(edges)
		require.Len(t, groups, 1)
		// Only the ungrouped row reaches the table.
		rows := []CorrelationEdgeRow{{
			Edge: copyGroupEdge(&edges[3]), FromName: "Caller", ToName: "base",
			FromType: "function", ToType: "image",
		}}
		out := RenderCorrelations("code", rows, 4, false, groups)

		assert.Contains(t, out, "one of 3 candidates")
		assert.Equal(t, 1, strings.Count(out, "| `Caller` [function] |"), "one table row: the bound edge")
		assert.NotContains(t, out, "traversal stops at this candidate group")
	})

	t.Run("correlations_total_line_unchanged", func(t *testing.T) {
		// THE CATCHER for an implementation that "helpfully" subtracts collapsed
		// rows from a number three other behaviors read.
		edges := allEdges()
		groups, _ := GroupCandidateEdges(edges)
		rows := []CorrelationEdgeRow{{
			Edge: copyGroupEdge(&edges[3]), FromName: "Caller", ToName: "base",
			FromType: "function", ToType: "image",
		}}
		out := RenderCorrelations("code", rows, 4, false, groups)
		assert.Contains(t, out, "4 edge(s), sorted by confidence desc.",
			"total still counts EDGES, group members included")
	})
}

// TestNewTimelineTopK_RowCapBounds pins the constructor's own bounds on rowCap.
//
// NewTimelineTopK is EXPORTED and sizes its retention buffer from the rowCap it
// is handed, so the bound has to live here rather than only at the one call site
// that happens to clamp today: an enormous rowCap made the capacity hint exceed
// what a slice allocation accepts and panicked the constructor outright.
//
// The three cases are one guard plus its two known-positive controls — an
// in-range value must survive unchanged and a non-positive one must still fall
// back to the default, so a clamp that swallowed every caller's limit would fail
// here rather than pass as "bounded".
func TestNewTimelineTopK_RowCapBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		given int
		want  int
	}{
		{"MaxInt is clamped to the ceiling", math.MaxInt, TimelineRowCapMax},
		{"above the ceiling is clamped", TimelineRowCapMax + 1, TimelineRowCapMax},
		{"an in-range limit passes through", 42, 42},
		{"non-positive falls back to the default", 0, TimelineRowCapDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := NewTimelineTopK("CreatedAt", tc.given)
			if k.rowCap != tc.want {
				t.Fatalf("rowCap = %d, want %d", k.rowCap, tc.want)
			}
			if c := cap(k.entries); c > TimelineRowCapMax+paging.BrowsePageSize {
				t.Fatalf("retention buffer capacity %d exceeds the bound %d",
					c, TimelineRowCapMax+paging.BrowsePageSize)
			}
		})
	}
}
