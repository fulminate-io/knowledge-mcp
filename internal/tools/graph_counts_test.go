// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// perNameStats answers with counts for the names it knows and an error for every
// other name, so ONE run renders a readable graph, an empty graph and an
// unreadable one through the same instrument.
//
// That matters: a fake that only ever fails cannot show that a real empty still
// renders as a measurement, and a fake that only ever succeeds cannot show the
// failure at all. The discrimination this file exists to prove needs both in one
// run.
type perNameStats struct {
	counts map[string][2]int32
	err    error
}

func (p *perNameStats) Stats(
	_ context.Context, req *knowledgev1.StatsRequest,
) (*knowledgev1.StatsResponse, error) {
	t := req.GetTarget()
	name := t.GetName()
	if name == "" {
		name = t.GetLanguage()
	}
	if name == "" {
		name = t.GetAccount()
	}
	c, ok := p.counts[name]
	if !ok {
		return nil, p.err
	}
	return &knowledgev1.StatsResponse{
		GraphStats: &knowledgev1.GraphStats{NodeCount: c[0], EdgeCount: c[1]},
	}, nil
}

func (p *perNameStats) Execute(
	context.Context, *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestGraphCountRow_ReportsTheReadFailureRatherThanEmptiness is the row-renderer
// gate.
//
// THE DEFECT: graphCounts returned (0, 0) on ANY error, and the row rendered that
// as "0 nodes, 0 edges" — byte-identical to a graph that really is empty. A graph
// the client could not READ was reported as a graph with nothing IN it, which is
// precisely how a live unreachable-graph incident came to be diagnosed as data
// loss.
//
// THE EMPTY-GRAPH PAIR IS THE DISCRIMINATING CONTROL. Without it, a renderer that
// reported a read failure for every zero count would satisfy every other
// assertion here while hiding every genuinely empty graph.
func TestGraphCountRow_ReportsTheReadFailureRatherThanEmptiness(t *testing.T) {
	fake := &perNameStats{
		counts: map[string][2]int32{
			"readable": {12, 34},
			"empty":    {0, 0},
		},
		err: errors.New("not_found: graph does not exist"),
	}
	ctx := context.Background()

	// UNREADABLE: names the graph, carries the error, and reports NO count.
	unreadable := graphCountRow(ctx, fake, "practice", "phantom")
	assert.Contains(t, unreadable, "phantom", "the row names the graph")
	assert.Contains(t, unreadable, "not_found", "the row carries the error text a reader must act on")
	assert.NotContains(t, unreadable, "0 nodes",
		"an unmeasured value must NEVER be rendered as a measurement — this exact row inverted a live diagnosis")

	// READABLE: still reports its counts.
	readable := graphCountRow(ctx, fake, "practice", "readable")
	assert.Contains(t, readable, "12 nodes", "a readable graph still reports its node count")
	assert.Contains(t, readable, "34 edges", "a readable graph still reports its edge count")

	// EMPTY: still renders as the MEASUREMENT it is...
	empty := graphCountRow(ctx, fake, "practice", "empty")
	assert.Contains(t, empty, "0 nodes",
		"a genuinely empty graph is a measured zero and must still render as one")
	assert.NotContains(t, empty, "not_found",
		"an empty graph is not a read failure")

	// ...and renders DIFFERENTLY from the unreadable one. This is the pair that
	// makes the whole distinction observable rather than asserted.
	assert.NotEqual(t, empty, unreadable,
		"an empty graph and an unreadable one must not render identically — that identity IS the defect")
}

// TestListLinkageGraphs_FailedCountIsNotAnAbsentGraph drives the real linkage
// listing, which is where conflating the two did the most damage: a swallowed
// error became (0, 0), fell into the zero check, and reported the graph as NOT
// FOUND.
func TestListLinkageGraphs_FailedCountIsNotAnAbsentGraph(t *testing.T) {
	ctx := context.Background()

	// FAILED READ: an error result naming what happened, never "not found".
	failing := &perNameStats{counts: map[string][2]int32{}, err: errors.New("unavailable: backend unreachable")}
	res := listLinkageGraphs(ctx, failing)
	require.NotEmpty(t, res.Content)
	body := res.Content[0].Text
	assert.True(t, res.IsError, "a read failure is an error result, not a cheerful absence report")
	assert.Contains(t, body, "unavailable", "the result names what actually happened")
	assert.NotContains(t, body, "No linkage graph found",
		"a graph that could not be READ must never be reported as a graph that does not EXIST")

	// CONTROL 1 — a genuinely ABSENT linkage graph still reports absence.
	absent := &perNameStats{counts: map[string][2]int32{"": {0, 0}}}
	resAbsent := listLinkageGraphs(ctx, absent)
	require.NotEmpty(t, resAbsent.Content)
	assert.Contains(t, resAbsent.Content[0].Text, "No linkage graph found",
		"real absence must still be reported as absence")

	// CONTROL 2 — a POPULATED linkage graph still reports its counts.
	populated := &perNameStats{counts: map[string][2]int32{"": {7, 9}}}
	resPop := listLinkageGraphs(ctx, populated)
	require.NotEmpty(t, resPop.Content)
	assert.False(t, resPop.IsError)
	assert.Contains(t, resPop.Content[0].Text, "7 nodes", "a populated graph still reports its counts")
	assert.Contains(t, resPop.Content[0].Text, "9 edges")
}
