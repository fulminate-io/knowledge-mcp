// SPDX-License-Identifier: Apache-2.0

// collect_recipe_land_version_test.go — requirement 4's version counter, driven
// across THREE real landings.
//
// It is its own file because it is the only cell in this family that needs the
// fake to behave like a STORE rather than a recorder: run N reads what run N-1
// wrote. The fake and its helpers are in collect_recipe_land_harness_test.go.

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// TestInterceptCollect_Landing_VersionCounterWalksAcrossThreeRealRuns is
// requirement 4's counter, driven the only way that can observe it: THREE real
// landings against one fake that applies each run's plan back the way a store
// does, so run N reads what run N-1 actually wrote.
//
// WHY A SEEDED RESIDENT CANNOT OBSERVE THIS. Production never writes a version
// key onto a BASE resident — only a twin carries one, under a DERIVED id. A test
// that hand-seeds version=2 onto the base id therefore reaches v3 by a path
// production does not use, and passes against an implementation whose counter
// pins at 2 forever. That is the class this test exists to replace.
//
// A DUPLICATE-ID WRITE IS ITSELF A RED, asserted at the end. create_batch is an
// ADD: applyCreate probes only edge endpoints, and store.Graph.AddNode finishes
// with g.nodes.Store(live.Id, live), so a repeated id REPLACES the resident row
// with no refusal anywhere on the path. A landing that re-minted the same twin id
// would look successful and destroy the twin before it.
func TestInterceptCollect_Landing_VersionCounterWalksAcrossThreeRealRuns(t *testing.T) {
	routerBase := recipe.StableID(landingTargetKey(), "hohpe-eip", "pattern", "Message Router")
	twin2 := recipe.VersionedTwinID(landingTargetKey(), "hohpe-eip", "pattern", routerBase, 2)
	twin3 := recipe.VersionedTwinID(landingTargetKey(), "hohpe-eip", "pattern", routerBase, 3)

	c := newLandingCaller()

	// RUN 1 — nothing resident: the emitted id lands as a BASE node with no
	// version key at all, because absent means 1 and the corpus is not rewritten
	// to carry a value meaning "original".
	mark := len(c.reads)
	_, plan1 := runLand(t, c, nil)
	base1 := bodyByID(plan1, routerBase)
	require.NotNil(t, base1, "run 1 lands the base node under the emitted id")
	assert.Empty(t, base1.GetMetadata()[landingVersionMetaKey], "a first landing writes no version key")
	assert.Equal(t, 1, practiceByIDReads(c.reads[mark:]),
		"run 1 costs ONE bulk read: nothing is resident, so no chain level is probed, and the hub the batch "+
			"CREATES is admitted from the payload rather than read")

	// RUN 2 — the base id is now resident: a twin at v2 under a derived id.
	mark = len(c.reads)
	_, plan2 := runLand(t, c, nil)
	b2 := bodyByID(plan2, twin2)
	require.NotNil(t, b2, "run 2 mints v2 under a DISTINCT derived id")
	assert.Equal(t, "2", b2.GetMetadata()[landingVersionMetaKey])
	assert.Equal(t, 3, practiceByIDReads(c.reads[mark:]),
		"run 2 costs THREE bulk reads: the base level, one probe of the v2 ids that finds nothing, and the "+
			"write path's own resolution of the RESIDENT hub — a landing under a hub it did not create holds "+
			"that hub to the hub contract before writing under it")

	// RUN 3 — THE ASSERTION THE OLD TEST COULD NOT MAKE. The v2 twin is resident
	// under a derived id the base-id read never asks about, so a landing that
	// reads only the emitted ids re-mints twin2 here.
	mark = len(c.reads)
	_, plan3 := runLand(t, c, nil)
	b3 := bodyByID(plan3, twin3)
	require.NotNil(t, b3,
		"THE COUNTER WALKS: run 3 must find v2 through the chain and mint v3, not a second v2")
	assert.Equal(t, "3", b3.GetMetadata()[landingVersionMetaKey])
	assert.Nil(t, bodyByID(plan3, twin2), "and it must not write the v2 id a second time")
	assert.Equal(t, 4, practiceByIDReads(c.reads[mark:]),
		"run 3 costs FOUR bulk reads: the base level, the v2 level that hits, the v3 level that ends the walk, "+
			"and the write path's resolution of the resident hub")

	// RUN 4 — THE ASSERTION RUN 3 CANNOT MAKE. Every twin id is derived from the
	// BASE id and a level, never from the previous twin. A walk that derived each
	// probe from the chain's latest id instead would agree with this one through
	// run 3 (base → v2 → v3 happen to coincide) and diverge here: it would probe
	// an id nobody minted, read v3 as absent, and re-mint the v3 ids. A fourth
	// landing is the shortest run that tells the two derivations apart.
	twin4 := recipe.VersionedTwinID(landingTargetKey(), "hohpe-eip", "pattern", routerBase, 4)
	mark = len(c.reads)
	_, plan4 := runLand(t, c, nil)
	b4 := bodyByID(plan4, twin4)
	require.NotNil(t, b4,
		"run 4 must find v3 through the chain and mint v4: a probe derived from the latest twin instead of the base re-mints v3 here")
	assert.Equal(t, "4", b4.GetMetadata()[landingVersionMetaKey])
	assert.Nil(t, bodyByID(plan4, twin3), "and it must not write the v3 id a second time")
	assert.Equal(t, 5, practiceByIDReads(c.reads[mark:]),
		"run 4 costs FIVE bulk reads: the base level, v2, v3, the v4 level that ends the walk, and the write "+
			"path's resolution of the resident hub")

	// FOUR DISTINCT IDS, versions 1 through 4, all four retained side by side.
	ids := map[string]struct{}{routerBase: {}, twin2: {}, twin3: {}, twin4: {}}
	assert.Len(t, ids, 4, "every version carries its own id")
	require.Contains(t, c.practiceByID, routerBase)
	require.Contains(t, c.practiceByID, twin2)
	require.Contains(t, c.practiceByID, twin3)
	require.Contains(t, c.practiceByID, twin4)
	assert.Empty(t, c.practiceByID[routerBase].GetMetadata()[landingVersionMetaKey], "v1 is the absent key")
	assert.Equal(t, "2", c.practiceByID[twin2].GetMetadata()[landingVersionMetaKey])
	assert.Equal(t, "3", c.practiceByID[twin3].GetMetadata()[landingVersionMetaKey])
	assert.Equal(t, "4", c.practiceByID[twin4].GetMetadata()[landingVersionMetaKey])

	// THREE next-version EDGES, CHAINED old → new rather than fanned out of the base.
	assert.Equal(t, routerBase, nextVersionEdgeInto(t, plan2, twin2),
		"run 2's edge runs from the base to v2")
	assert.Equal(t, twin2, nextVersionEdgeInto(t, plan3, twin3),
		"run 3's edge runs from V2 to v3 — the chain's latest, not the base")
	assert.Equal(t, twin3, nextVersionEdgeInto(t, plan4, twin4),
		"run 4's edge runs from V3 to v4")

	assert.Empty(t, c.duplicateWrites,
		"no landing may write an id the graph already holds: create_batch is an add, so that is a silent overwrite")
}

// nextVersionEdgeInto returns the id of the OLD end of the single next-version
// edge whose new end is the batch slot holding wantID.
func nextVersionEdgeInto(t *testing.T, plan *knowledgev1.MutationPlan, wantID string) string {
	t.Helper()
	var from []string
	for _, e := range plan.GetEdges() {
		if e.GetType() != string(kgtypes.EdgeNextVersion) {
			continue
		}
		idx := int(e.GetToIdx())
		bodies := plan.GetNodeBodies()
		require.GreaterOrEqual(t, idx, 0, "the new end is a slot in this batch")
		require.Less(t, idx, len(bodies))
		if bodies[idx].GetId() != wantID {
			continue
		}
		require.Equal(t, int32(-1), e.GetFromIdx(), "the old end is an existing node, addressed by id")
		from = append(from, e.GetFromId())
	}
	require.Len(t, from, 1, "exactly one next-version edge lands on a twin")
	return from[0]
}
