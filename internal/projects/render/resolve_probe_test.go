// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// probeGc answers the practice-resolution path and records what it was asked.
// It resolves nodeID in every graph listed in resolvesIn, so a resolver that
// returned whichever probe finished first would be visibly nondeterministic.
type probeGc struct {
	graphs     []string
	nodeID     string
	resolvesIn map[string]bool
	// knowledgeHas makes the knowledge-graph read succeed, which is the common
	// case and must never reach the practice path at all.
	knowledgeHas bool

	mu           sync.Mutex
	executes     int
	listedGraphs int
	probed       []string

	// Concurrency instrumentation: inFlight tracks simultaneous probes and
	// maxInFlight records the high-water mark. A serial loop never exceeds 1.
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	// probeDelay is held by every probe so overlap has a window to occur in.
	probeDelay time.Duration
}

func (p *probeGc) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.TextResult(""), nil
}

func (p *probeGc) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	p.mu.Lock()
	p.executes++
	p.mu.Unlock()

	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		p.mu.Lock()
		p.listedGraphs++
		p.mu.Unlock()
		infos := make([]*knowledgev1.GraphInfo, len(p.graphs))
		for i, g := range p.graphs {
			infos[i] = &knowledgev1.GraphInfo{Name: g}
		}
		return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
	}

	lang := req.GetTarget().GetLanguage()
	if lang == "" {
		// The knowledge-graph read.
		if p.knowledgeHas && q.GetById() == p.nodeID {
			return enginetest.ResponseWithNode(&knowledgev1.Node{
				Id: p.nodeID, SymbolName: "Resolved", Type: string(kgtypes.NodePattern),
			}), nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}

	cur := p.inFlight.Add(1)
	for {
		hi := p.maxInFlight.Load()
		if cur <= hi || p.maxInFlight.CompareAndSwap(hi, cur) {
			break
		}
	}
	time.Sleep(p.probeDelay)
	p.inFlight.Add(-1)

	p.mu.Lock()
	p.probed = append(p.probed, lang)
	p.mu.Unlock()

	if p.resolvesIn[lang] && q.GetById() == p.nodeID {
		return enginetest.ResponseWithNode(&knowledgev1.Node{
			Id: p.nodeID, SymbolName: "In " + lang, Type: string(kgtypes.NodePattern),
		}), nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestResolveAssembleNode_KnowledgeHit_OneExecuteNoPracticeList is the
// characterization guard for the COMMON case, and it is here because a
// concurrent fan-out is easy to start before checking whether it is needed. An
// id that resolves in the knowledge graph must still cost exactly one Execute
// and must never enumerate practice graphs.
func TestResolveAssembleNode_KnowledgeHit_OneExecuteNoPracticeList(t *testing.T) {
	gc := &probeGc{
		graphs:       []string{"go", "python", "rust"},
		nodeID:       "known",
		knowledgeHas: true,
		resolvesIn:   map[string]bool{},
	}
	node, graphType, graphName, err := resolveAssembleNode(context.Background(), gc, "known")
	require.NoError(t, err)
	require.NotNil(t, node)

	assert.Equal(t, "known", node.Id)
	assert.Empty(t, graphType, "a knowledge-graph hit carries no cross-graph routing")
	assert.Empty(t, graphName)
	assert.Equal(t, 1, gc.executes, "exactly one Execute: the knowledge-graph read")
	assert.Zero(t, gc.listedGraphs, "the practice-graph list must not be enumerated on a hit")
	assert.Empty(t, gc.probed, "no practice graph is probed on a hit")
}

// TestResolveAssembleNode_PracticeProbesConcurrentAndIndexDeterministic asserts
// TWO properties in one test, because either alone is satisfiable by a wrong
// implementation: a concurrency-only assertion passes for a resolver that
// returns whichever goroutine won, and a determinism-only assertion passes for
// the unchanged serial loop.
//
// The fixture seeds the SAME id in two practice graphs, so "whichever finished
// first" and "the lowest-indexed graph" are distinguishable answers.
func TestResolveAssembleNode_PracticeProbesConcurrentAndIndexDeterministic(t *testing.T) {
	newGc := func() *probeGc {
		return &probeGc{
			graphs:     []string{"go", "python", "rust", "typescript"},
			nodeID:     "shared",
			resolvesIn: map[string]bool{"python": true, "rust": true},
			probeDelay: 20 * time.Millisecond,
		}
	}

	t.Run("the probes overlap", func(t *testing.T) {
		gc := newGc()
		_, _, _, err := resolveAssembleNode(context.Background(), gc, "shared")
		require.NoError(t, err)

		assert.Equal(t, 1, gc.listedGraphs, "the practice list is read once")
		sort.Strings(gc.probed)
		assert.Equal(t, []string{"go", "python", "rust", "typescript"}, gc.probed,
			"every practice graph is probed — a short-circuit would make the concurrency claim vacuous")
		assert.Greater(t, int(gc.maxInFlight.Load()), 1,
			"probes must overlap; a high-water mark of 1 is a serial loop")
	})

	t.Run("the lowest-indexed resolving graph always wins", func(t *testing.T) {
		// Repeated because a resolver that returned the first goroutine to
		// finish would pass a single run by luck. python is index 1 and rust is
		// index 2; both resolve.
		for range 25 {
			gc := newGc()
			node, graphType, graphName, err := resolveAssembleNode(context.Background(), gc, "shared")
			require.NoError(t, err)
			require.NotNil(t, node)
			assert.Equal(t, "practice", graphType)
			assert.Equal(t, "python", graphName,
				"the lower-indexed graph must win every time, matching the serial loop's first-match order")
			assert.Equal(t, "In python", node.SymbolName)
		}
	})

	t.Run("a failing probe does not cancel its siblings", func(t *testing.T) {
		// Only the LAST graph resolves, so a resolver that let an unresolved or
		// erroring earlier probe cancel the group would report not-found.
		gc := newGc()
		gc.resolvesIn = map[string]bool{"typescript": true}
		node, graphType, graphName, err := resolveAssembleNode(context.Background(), gc, "shared")
		require.NoError(t, err)
		require.NotNil(t, node)
		assert.Equal(t, "practice", graphType)
		assert.Equal(t, "typescript", graphName)
	})

	t.Run("resolving nowhere still reports the documented not-found", func(t *testing.T) {
		gc := newGc()
		gc.resolvesIn = map[string]bool{}
		_, _, _, err := resolveAssembleNode(context.Background(), gc, "shared")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `no node with id "shared" in knowledge or any practice graph`)
	})
}
