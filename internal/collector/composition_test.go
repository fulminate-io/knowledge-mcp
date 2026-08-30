// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// nodesOfType builds n nodes carrying the given Type, so a census fixture reads
// as the counts it is asserting rather than as a wall of literals.
func nodesOfType(typ string, n int) []*knowledgev1.Node {
	out := make([]*knowledgev1.Node, 0, n)
	for range n {
		out = append(out, &knowledgev1.Node{Type: typ})
	}
	return out
}

func TestNewCollectComposition_CountsAndRenderOrdering(t *testing.T) {
	// The measured CWE shape, which is the harvest this whole mechanism exists
	// for: many list_item / link / table nodes and zero substantive content.
	var nodes []*knowledgev1.Node
	nodes = append(nodes, nodesOfType("list_item", 24)...)
	nodes = append(nodes, nodesOfType("link", 16)...)
	nodes = append(nodes, nodesOfType("table", 12)...)
	nodes = append(nodes, nodesOfType("list", 4)...)
	nodes = append(nodes, nodesOfType("page", 4)...)
	nodes = append(nodes, nodesOfType("section", 4)...)

	edges := make([]kgwire.BatchEdge, 60)

	comp := NewCollectComposition(&collectorwire.CollectResult{
		GraphType: kgtypes.GraphWebRaw,
		GraphName: "cwe",
		Nodes:     nodes,
		Edges:     edges,
	})

	assert.Equal(t, kgtypes.GraphWebRaw, comp.GraphType)
	assert.Equal(t, "cwe", comp.GraphName)
	assert.Equal(t, 64, comp.TotalNodes)
	assert.Equal(t, 60, comp.TotalEdges)
	assert.Equal(t, map[string]int{
		"list_item": 24,
		"link":      16,
		"table":     12,
		"list":      4,
		"page":      4,
		"section":   4,
	}, comp.NodesByType)

	// Count descending, then name ascending: list / page / section all carry 4
	// and are ordered alphabetically among themselves, which is what pins the
	// tiebreak half of the ordering rule.
	assert.Equal(t,
		"nodes 64 (list_item 24, link 16, table 12, list 4, page 4, section 4), edges 60",
		comp.Render())

	// The empty result renders without an empty parenthetical.
	empty := NewCollectComposition(&collectorwire.CollectResult{})
	assert.Equal(t, 0, empty.TotalNodes)
	assert.Empty(t, empty.NodesByType)
	assert.Equal(t, "nodes 0, edges 0", empty.Render())
}

// TestCollectComposition_RenderCeilingEngages is the known-positive fixture for
// the truncation bound: a composition carrying MORE than compositionRenderTopN
// distinct types must render exactly the top N and name how many it omitted.
// Without a fixture that exceeds the bound, a Render that never truncated would
// read identically to one that does.
func TestCollectComposition_RenderCeilingEngages(t *testing.T) {
	const distinct = compositionRenderTopN + 5

	// Descending counts by construction, so the expected render is the first N
	// of a known sequence rather than something derived from Render's own sort.
	var nodes []*knowledgev1.Node
	total := 0
	for i := range distinct {
		count := distinct - i
		nodes = append(nodes, nodesOfType(fmt.Sprintf("type_%02d", i), count)...)
		total += count
	}

	comp := NewCollectComposition(&collectorwire.CollectResult{Nodes: nodes})
	require.Len(t, comp.NodesByType, distinct)
	require.Equal(t, total, comp.TotalNodes)

	got := comp.Render()

	// Exactly compositionRenderTopN type entries survive, in count-descending
	// order, and the omitted count is the remainder.
	var want strings.Builder
	fmt.Fprintf(&want, "nodes %d (", total)
	for i := range compositionRenderTopN {
		if i > 0 {
			want.WriteString(", ")
		}
		fmt.Fprintf(&want, "type_%02d %d", i, distinct-i)
	}
	fmt.Fprintf(&want, ", +%d more types), edges 0", distinct-compositionRenderTopN)
	assert.Equal(t, want.String(), got)

	// The types past the ceiling are named nowhere in the output — the
	// truncation is real, not cosmetic.
	for i := compositionRenderTopN; i < distinct; i++ {
		assert.NotContains(t, got, fmt.Sprintf("type_%02d", i))
	}
}

// --- CompositionAsserter capability dispatch ---------------------------------

// assertingCollector declares a composition invariant that ALWAYS errors, so any
// call reaching it is observable.
type assertingCollector struct {
	name   string
	fired  *int
	result *collectorwire.CollectResult
}

func (c *assertingCollector) Name() string { return c.name }

func (c *assertingCollector) Collect(_ context.Context, _ string, _ CollectOptions) (*collectorwire.CollectResult, error) {
	if c.result != nil {
		return c.result, nil
	}
	return &collectorwire.CollectResult{}, nil
}

func (c *assertingCollector) AssertComposition(CollectComposition) error {
	*c.fired++
	return errors.New("asserting collector always refuses")
}

var _ CompositionAsserter = (*assertingCollector)(nil)

// TestCheckComposition_SilentForTypeWithoutAsserter covers the degrade-to-nothing
// half of the capability idiom: a registered collector that declares no invariant
// gets no gate, and neither does a type that is not registered at all.
func TestCheckComposition_SilentForTypeWithoutAsserter(t *testing.T) {
	resetRegistry(t)
	Register(&fakeCollector{name: "no-invariant"})

	comp := NewCollectComposition(&collectorwire.CollectResult{GraphName: "anything"})

	assert.NoError(t, CheckComposition("no-invariant", comp),
		"a registered collector declaring no invariant must be silent")
	assert.NoError(t, CheckComposition("never-registered", comp),
		"an unregistered collector type must be silent, not an error")

	// KNOWN POSITIVE, same call and same composition: a collector that DOES
	// declare an invariant is reached and its verdict is returned. Without this
	// leg, a CheckComposition that returned nil unconditionally would satisfy
	// both assertions above.
	fired := 0
	Register(&assertingCollector{name: "has-invariant", fired: &fired})
	require.Error(t, CheckComposition("has-invariant", comp))
	assert.Equal(t, 1, fired, "the declared invariant was actually invoked")
}

// TestCollect_DoesNotRunCompositionCheck proves the assertion is NOT inside
// Collect. That placement is load-bearing: the four cloud collectors call
// collector.Collect recursively per cascade target, so an assertion inside
// Collect would fire once per cascade sub-collect and let a sub-collect's
// composition fail the parent.
func TestCollect_DoesNotRunCompositionCheck(t *testing.T) {
	installCaptureSink(t)
	resetRegistry(t)

	fired := 0
	Register(&assertingCollector{
		name:  "always-refuses",
		fired: &fired,
		result: &collectorwire.CollectResult{
			GraphName: "refused-graph",
			Nodes:     []*knowledgev1.Node{{Type: "page"}},
		},
	})

	comp, err := Collect(context.Background(), "always-refuses", "id", CollectOptions{})
	require.NoError(t, err, "Collect must not run the composition assertion")
	assert.Equal(t, 0, fired, "AssertComposition must not be reached from inside Collect")
	assert.Equal(t, map[string]int{"page": 1}, comp.NodesByType,
		"Collect still censuses the result — it just does not assert on it")

	// KNOWN POSITIVE: the SAME registered collector DOES refuse when the check is
	// dispatched at the top-level site, so the zero above is the placement rather
	// than an asserter that never fires.
	require.Error(t, CheckComposition("always-refuses", comp))
	assert.Equal(t, 1, fired)
}
