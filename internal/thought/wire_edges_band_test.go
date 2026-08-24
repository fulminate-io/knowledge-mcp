// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// bandPlanCaller answers a banded match-all read from a fixture corpus, honoring the
// requested from_id band, and records every plan it was sent so the WIRE SHAPE can be
// asserted rather than inferred from the returned rows.
type bandPlanCaller struct {
	corpus []*knowledgev1.Edge
	plans  []*knowledgev1.QueryPlan
}

func (c *bandPlanCaller) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	c.plans = append(c.plans, q)
	// Honors the band through the package's single band predicate, exactly as every
	// other fake here does.
	return &knowledgev1.ExecuteResponse{Edges: bandNarrow(c.corpus, q)}, nil
}

func TestFetchAllEdgesBanded(t *testing.T) {
	ctx := context.Background()
	ids := make([]string, 0, 64)
	corpus := make([]*knowledgev1.Edge, 0, 64)
	for i := range 64 {
		id := fmt.Sprintf("th-%04d", i)
		ids = append(ids, id)
		corpus = append(corpus, &knowledgev1.Edge{FromId: id, ToId: "t", Type: "charged-by"})
	}

	t.Run("empty inputs make no call at all", func(t *testing.T) {
		c := &bandPlanCaller{corpus: corpus}
		got, err := fetchAllEdgesBanded(ctx, c, nil, nil)
		require.NoError(t, err)
		assert.Nil(t, got)
		assert.Empty(t, c.plans, "an empty id set must not reach the wire")

		gotNil, errNil := fetchAllEdgesBanded(ctx, nil, ids, nil)
		require.NoError(t, errNil)
		assert.Nil(t, gotNil, "a nil caller matches the pivot reader's empty-graph shape")
	})

	t.Run("the plan carries a band and NO pivots", func(t *testing.T) {
		c := &bandPlanCaller{corpus: corpus}
		got, err := fetchAllEdgesBanded(ctx, c, ids, []kgtypes.EdgeType{kgtypes.EdgeChargedBy})
		require.NoError(t, err)

		require.Len(t, c.plans, paging.EdgeBandCount, "one Execute per band, no halving on this fixture")
		for i, p := range c.plans {
			// THE LOAD-BEARING ASSERTION: the ids derive the boundaries, they are
			// never SENT. A regression that passed them as pivots would still return
			// the right rows on this fixture and would be invisible without this.
			assert.Empty(t, p.GetIds(), "band %d must carry no pivot set", i)
			assert.Empty(t, p.GetById(), "band %d must carry no by_id pivot", i)
			assert.Empty(t, p.GetSelection().GetFromId(), "band %d must carry no from_id pivot set", i)

			assert.NotNil(t, p.GetEdgeFromBand(), "band %d must carry the range", i)
			assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_EDGES, p.GetReturnMode())
			assert.True(t, p.GetIncludeTombstones(), "band %d must include tombstones", i)
			assert.Equal(t, int32(engine.CorrelationsEdgeScanCap), p.GetLimit(),
				"the plan Limit is the server's own ceiling, so the drain's cap can notice it")
			assert.Equal(t, []string{string(kgtypes.EdgeChargedBy)}, p.GetSelection().GetEdgeTypes())
		}

		// The tiling is walked end to end, open at both ends.
		assert.Empty(t, c.plans[0].GetEdgeFromBand().GetFromIdGte(), "the first band is open below")
		assert.Empty(t, c.plans[len(c.plans)-1].GetEdgeFromBand().GetFromIdLt(), "the last band is open above")

		// Against a fixture-derived constant: the union is the whole corpus.
		assert.Len(t, got, 64, "every seeded edge survives the banded drain")
	})

	t.Run("an empty type filter sends no Selection", func(t *testing.T) {
		c := &bandPlanCaller{corpus: corpus}
		_, err := fetchAllEdgesBanded(ctx, c, ids, nil)
		require.NoError(t, err)
		require.NotEmpty(t, c.plans)
		assert.Nil(t, c.plans[0].GetSelection(),
			"a nil type filter means every type at the wire, matching the pivot reader")
	})
}
