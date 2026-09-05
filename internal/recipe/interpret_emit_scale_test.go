// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// scaleSourceView builds a synthetic sourceView of n distinctly-named section
// rows. Distinct names matter: a collision would refuse the run and the
// measurement would be of the refusal path rather than the emit loop.
func scaleSourceView(n int) *sourceView {
	byID := make(map[string]*knowledgev1.Node, n)
	rows := make([]*knowledgev1.Node, 0, n)
	for i := range n {
		id := fmt.Sprintf("s%d", i)
		node := &knowledgev1.Node{Id: id, Type: "section", SymbolName: fmt.Sprintf("Pattern %d", i)}
		byID[id] = node
		rows = append(rows, node)
	}
	return &sourceView{
		byID:     byID,
		byType:   map[string][]*knowledgev1.Node{"section": rows},
		outEdges: map[string][]*knowledgev1.Edge{},
		inEdges:  map[string][]*knowledgev1.Edge{},
	}
}

// medianInterpretNanos runs Interpret over n rows three times and returns the
// MEDIAN elapsed nanoseconds. The median is what keeps scheduling noise on a
// loaded machine out of the verdict; a single run of either size can be an
// outlier by more than the band's whole margin.
func medianInterpretNanos(t *testing.T, recipe *Recipe, n int) int64 {
	t.Helper()
	runs := make([]int64, 0, 3)
	for range 3 {
		sv := scaleSourceView(n)
		start := time.Now()
		res, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{MaxRows: n})
		elapsed := time.Since(start).Nanoseconds()
		require.NoError(t, err)
		require.Len(t, res.Nodes, n, "fixture: every row must emit, or the measurement is of a shorter loop")
		runs = append(runs, elapsed)
	}
	slices.Sort(runs)
	return runs[1]
}

// TestInterpret_EmitDuplicateGuardStaysLinear is the PERFORMANCE gate on the
// duplicate-identity guard.
//
// THE DEFECT CLASS IT ALONE DETECTS: a duplicate guard that is behaviourally
// CORRECT and quadratic. An implementation that scans the accumulated
// result.Nodes for a colliding id on every row refuses exactly the same emission
// sets and names exactly the same rows, so every behavioral criterion in this
// plan passes it. Only the shape of its growth curve tells them apart.
//
// THE BAND IS MEASURED, NOT REASONED. Compliant implementations measured 4.41 to
// 10.54 across three machines and four bases; the quadratic variant measured
// 29.34 to 53.05 across the same. The cap of 20 sits between them, with 1.9x of
// headroom above the worst compliant run ever observed and 1.5x of margin below
// the smallest violating one — the two runs that could actually cross.
//
// THE 8x ROW GROWTH IS ASSERTED HERE rather than left implicit, so a future edit
// to the two sizes cannot silently move the band out from under the constant.
func TestInterpret_EmitDuplicateGuardStaysLinear(t *testing.T) {
	const (
		small    = 2000
		large    = 16000
		ratioCap = 20.0
	)
	require.Equal(t, 8, large/small,
		"the band's separating constant assumes an 8x row growth; changing the sizes invalidates it")

	recipe := parseOrFatal(t, simpleEmitRecipeBody)

	smallNanos := medianInterpretNanos(t, recipe, small)
	largeNanos := medianInterpretNanos(t, recipe, large)
	require.Positive(t, smallNanos, "the small measurement must be nonzero, or the ratio is meaningless")

	ratio := float64(largeNanos) / float64(smallNanos)
	fmt.Printf("emit scaling: %d rows=%dns, %d rows=%dns, ratio=%.2f (cap %.0f)\n",
		small, smallNanos, large, largeNanos, ratio, ratioCap)

	require.Less(t, ratio, ratioCap,
		"the emit loop must stay LINEAR: an 8x row growth costing more than %.0fx means the guard scans the accumulated node list per row instead of reading the run-scoped map",
		ratioCap)
}
