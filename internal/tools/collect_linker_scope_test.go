// SPDX-License-Identifier: Apache-2.0

package tools

// collect_linker_scope_test.go — the post-collect tail's BREADTH at its own call
// site.
//
// WHY IT IS NOT COVERED BY THE LINKER PACKAGE'S SCOPE TEST. That test proves
// LinkDockerfilesInGraph reads one graph and LinkDockerfiles reads them all. It
// says nothing about WHICH of the two runPostCollectLinker reaches for, and that
// is the whole subject of the orchestrator's bound: replacing the scoped call in
// collect_linker.go with the unbounded sweep changes no function that test
// covers, so the linker suite and the tools suite both stay green while every
// code collect on the machine goes back to paying two full keyset drains per code
// graph. AGENTS.md mandates a collect after every commit, so that regression is
// paid on every commit.
//
// THE OBSERVABLE HAS TO BE READS, NOT WIRE ACTIVITY. linkerTriggerCaller's
// wireTouched() is a boolean that is true under BOTH breadths — it was built to
// answer "did the pass run at all", which is a different question. This file's
// caller records reads PER GRAPH and, more importantly, the ORDER of code reads:
// the sweep opens with a code-family enumeration (its first act is resolving
// which graphs exist), while the scoped call opens with a node read of the graph
// it was handed. That ordering is the one observable that separates the two
// calls, and it is the same one the linker package's own scope test uses, so the
// two files read one instrument from either side of the seam.

import (
	"context"
	"encoding/json"
	"maps"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// linkerScopeCaller answers the Dockerfile pass's reads for a set of seeded code
// graphs and records what was read, in order.
//
// IT SERVES REAL CONTENT rather than empty carriers, because an empty answer
// makes both breadths look identical: a sweep over three empty graphs and a
// scoped call into one empty graph both read nothing worth counting. Each seeded
// graph holds a Dockerfile that COPYs one source file, so a pass that reaches a
// graph leaves a per-graph read behind whether or not it emits an edge.
type linkerScopeCaller struct {
	mu sync.Mutex
	// repos are the seeded code graph names.
	repos map[string]bool
	// readsByRepo counts type-browse reads per code graph.
	readsByRepo map[string]int
	// seq records the ORDER of code reads as "enum" and "read:<repo>".
	seq []string
}

func newLinkerScopeCaller(names ...string) *linkerScopeCaller {
	c := &linkerScopeCaller{repos: map[string]bool{}, readsByRepo: map[string]int{}}
	for _, n := range names {
		c.repos[n] = true
	}
	return c
}

func (c *linkerScopeCaller) snapshot() ([]string, map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reads := make(map[string]int, len(c.readsByRepo))
	maps.Copy(reads, c.readsByRepo)
	return append([]string(nil), c.seq...), reads
}

// Call is the legacy gc.Call seam. The Dockerfile pass does not use it; it is
// present because ClientDeps.GraphCaller() is satisfied by an Execute-capable
// type and some sibling paths still reach for Call.
func (c *linkerScopeCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
}

// Stats serves the linkage graph's edge vocabulary so emitLink's edge-type
// declaration resolves. An empty vocabulary is the bootstrap case a write
// admits, which is what every linker fixture in this repository drives.
func (c *linkerScopeCaller) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{GraphStats: &knowledgev1.GraphStats{}}, nil
}

// nodesFor returns the two seeded nodes of one code graph.
func (c *linkerScopeCaller) nodesFor(repo string) []*knowledgev1.Node {
	return []*knowledgev1.Node{
		{
			Id: repo + ":Dockerfile", Type: string(kgtypes.NodeFile),
			FilePath: "Dockerfile", Content: "FROM scratch\nCOPY main.go /\n",
		},
		{Id: repo + ":main.go", Type: string(kgtypes.NodeFile), FilePath: "main.go"},
	}
}

// Execute answers the three read shapes the pass and its edge emitter issue, and
// records exactly TWO of them.
//
// THE BY-ID PROBE IS DELIBERATELY NOT COUNTED, and the exclusion is what makes
// this instrument measure breadth rather than noise. emitLink resolves each
// endpoint through crossgraph, which enumerates the foreign families and probes
// them BY ID — reads issued per EDGE, by a different mechanism, after the pass
// has already chosen its graphs. Counting them would make a one-graph pass that
// emits one edge look like a sweep. What the pass's own breadth is spelled as is
// the TYPE BROWSE (its NodeFile and NodePackage drains) and the graph-name
// enumeration, so those two are what this records. The linker package's own
// scope fake draws the same line for the same reason.
func (c *linkerScopeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	tgt := req.GetTarget()
	if tgt.GetGraph() != string(kgtypes.GraphCode) {
		return &knowledgev1.ExecuteResponse{}, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// By-id endpoint probe — served so the edges really emit, never recorded.
	if id := q.GetById(); id != "" {
		for repo := range c.repos {
			for _, n := range c.nodesFor(repo) {
				if n.GetId() == id {
					return enginetest.ResponseWithNodes(n), nil
				}
			}
		}
		return enginetest.ResponseWithNodes(), nil
	}

	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		c.seq = append(c.seq, "enum")
		infos := make([]*knowledgev1.GraphInfo, 0, len(c.repos))
		for n := range c.repos {
			infos = append(infos, &knowledgev1.GraphInfo{Name: n})
		}
		return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
	}

	nodeType := q.GetSelection().GetNodeType()
	if nodeType == "" {
		return enginetest.ResponseWithNodes(), nil
	}
	repo := tgt.GetRepo()
	c.readsByRepo[repo]++
	c.seq = append(c.seq, "read:"+repo)
	if !c.repos[repo] || nodeType != string(kgtypes.NodeFile) {
		return enginetest.ResponseWithNodes(), nil
	}
	return enginetest.ResponseWithNodes(c.nodesFor(repo)...), nil
}

// TestRunPostCollectLinker_ReadsOnlyTheCollectedGraph is ARM 1's breadth AT THE
// CALL, which is the row the what-to-test list named and the one the unit test
// does not reach.
//
// TWO-SIDED, and the second side is the bound. (i) the collected graph IS read,
// so the tail ran; (ii) the other two code graphs are read ZERO times and the
// first code read is a NODE read rather than a family enumeration — the tail
// already knows its graph's name, so the enumeration leaves the collect path
// entirely. An assertion that only checked (i) passes identically under the
// unbounded sweep, which is exactly how the regression stayed invisible.
func TestRunPostCollectLinker_ReadsOnlyTheCollectedGraph(t *testing.T) {
	gc := newLinkerScopeCaller("repo-a", "repo-b", "repo-c")

	runPostCollectLinker(context.Background(), newLinkerTriggerDeps(gc), postCollectLinkerType, "repo-a", false)

	seq, reads := gc.snapshot()
	require.NotEmpty(t, seq, "control: the tail issued code reads at all — a tail that never ran bounds nothing")
	assert.Equal(t, "read:repo-a", seq[0],
		"the post-collect tail must reach the SCOPED entry point, which opens with a node read of the "+
			"graph it was handed; the sweep opens with a code-family enumeration, and this is the read "+
			"that tells the two calls apart")
	assert.Positive(t, reads["repo-a"], "the collected graph is read")
	assert.Zero(t, reads["repo-b"], "no read may reach a graph the collect did not touch")
	assert.Zero(t, reads["repo-c"], "no read may reach a graph the collect did not touch")
	assert.NotContains(t, seq[:firstEnum(seq)], "enum",
		"and no code-family enumeration precedes the first read: the tail already knows its graph's "+
			"name, so the pass never asks which graphs exist. (An enumeration LATER in the sequence is "+
			"crossgraph's per-edge endpoint locate, a different mechanism that runs only once an edge "+
			"has been found — which is why the assertion is on the ORDER rather than on absence.)")
}

// TestRunPostCollectLinker_ManualLinkKeepsTheSweep is the SIBLING CELL, driven
// through the same instrument so the two calls are compared rather than each
// asserted alone.
//
// IT EXISTS SO THE BOUND CANNOT BE APPLIED TO BOTH CALLERS BY ACCIDENT. Scoping
// manage(operation:"link") to one graph loses the manual operation's whole value
// and would pass every row of the test above; only this row fails on it. It
// drives handleClientLinker — the manage tool's own handler and the production
// call site — rather than the linker package's unit, because the claim is about
// which entry point the manual path reaches.
func TestManageLink_KeepsTheAllGraphsSweep(t *testing.T) {
	gc := newLinkerScopeCaller("repo-a", "repo-b", "repo-c")

	res := handleClientLinker(context.Background(), newLinkerTriggerDeps(gc), manageArgs{Operation: "link"})
	require.False(t, res.IsError, toolResultText(res))

	seq, reads := gc.snapshot()
	require.NotEmpty(t, seq, "control: the manual operation issued code reads at all")
	assert.Equal(t, "enum", seq[0],
		"the manual operation must reach the SWEEP, which opens with the code-family enumeration")
	for _, name := range []string{"repo-a", "repo-b", "repo-c"} {
		assert.Positivef(t, reads[name], "the manual operation reads every code graph; %q was not read", name)
	}
	assert.Contains(t, toolResultText(res), "dockerfile=3",
		"and it links in all three, which a scoped call could not do")
}

// firstEnum returns the index of the first recorded enumeration, or len(seq)
// when there is none, so a prefix slice is always valid.
func firstEnum(seq []string) int {
	for i, s := range seq {
		if s == "enum" {
			return i
		}
	}
	return len(seq)
}
