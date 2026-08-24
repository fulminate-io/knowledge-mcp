// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFetchEdgesForNodeSet_PagesAtReflectionPageSize pins the PAGE SEQUENCE the
// reflection node-set edge read produces, at the call site rather than in the drain.
//
// THE REPRODUCTION AND THE REGRESSION ARE ONE TEST, and its red is BEHAVIORAL
// rather than structural: against a call site still passing the shared
// paging.EdgePivotPageSize, these same 2,501 pivots split into SIX pages
// (500,500,500,500,500,1); against reflectionEdgePivotPageSize they split into TWO
// (2500,1). So the expected failure is an assertion diff on the page slice — if
// this test fails to COMPILE or errors before the assertion, that is a setup
// problem, not the red this test exists to show.
//
// THE ASSERTION IS A WHOLE-SLICE EQUALITY, not a call count: a drain that issued
// two calls of the wrong sizes would satisfy a count and must not satisfy this.
//
// THE GENERIC DRAIN BOUND IS NOT WHAT IS UNDER TEST HERE. paging's own drain test
// already owns "a page never exceeds the bound"; the property this phase changes is
// WHICH bound this call site passes, which is only observable in this package.
func TestFetchEdgesForNodeSet_PagesAtReflectionPageSize(t *testing.T) {
	ctx := context.Background()
	gc := &edgeTypeRecorder{reflectEquivFake: newReflectEquivFake(defaultEquivSpec())}

	// The ids are synthetic and deliberately OUTSIDE the fake's seeded corpus: the
	// page sizes are a property of the drain's chunking, not of what comes back, and
	// the fake answers an edges request for unknown ids with an empty edge set rather
	// than an error.
	const pivots = 2501
	ids := make([]string, 0, pivots)
	for i := range pivots {
		ids = append(ids, fmt.Sprintf("t-pivot-%04d", i))
	}

	_, err := fetchEdgesForNodeSet(ctx, gc, ids, nil)
	require.NoError(t, err)

	require.Equal(t, []int{2500, 1}, gc.takePivotCounts(),
		"the reflection node-set read must page at reflectionEdgePivotPageSize — %d pivots "+
			"are one full page plus a remainder, not six pages of the shared default", pivots)
}
