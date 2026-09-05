// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunRecipe_RefusesDuplicateEmittedIdentity is the emit-site refusal.
//
// The defect: the emit loop wrote emitted[nodeID] = true and never TESTED it,
// while the line above had already assembled a second node carrying that id. Two
// rows sharing an identity therefore shipped two nodes under one id, and the
// upsert failed in the database with a raw SQLSTATE surfaced verbatim to the
// caller — a recipe-AUTHORING mistake arriving as a storage error.
func TestRunRecipe_RefusesDuplicateEmittedIdentity(t *testing.T) {
	caller := sourceOnlyCaller(
		section("s1", "Message Router"),
		section("s2", "Message Router"), // same name → same identity → same StableID
	)

	res, err := runInline(t, caller, simpleEmitRecipeBody)

	require.Error(t, err, "two rows resolving to one identity must be REFUSED")
	assert.Contains(t, err.Error(), "Message Router", "the error names the colliding identity")
	assert.Contains(t, err.Error(), "pattern", "the error names the emit rule's node type")
	assert.Contains(t, err.Error(), "s2", "the error names the SOURCE ROW that collided")
	// The refusal fires MID-RUN, at the colliding row, so the partial Result
	// carries only the rows emitted before it. That is the observable that
	// distinguishes an emit-site guard from an end-of-run scan.
	require.NotNil(t, res.Extract)
	assert.Len(t, res.Extract.Rows, 1, "only the first row was emitted before the refusal fired")
}

// TestRunRecipe_ShipsDistinctEmittedIdentities is the known-positive control:
// the same recipe and the same row count, with distinct names, must still ship.
//
// Without it, a guard that refused every emission set would satisfy every
// assertion above.
func TestRunRecipe_ShipsDistinctEmittedIdentities(t *testing.T) {
	caller := sourceOnlyCaller(
		section("s1", "Message Router"),
		section("s2", "Message Channel"),
	)

	res, err := runInline(t, caller, simpleEmitRecipeBody)

	require.NoError(t, err, "distinct identities must still run")
	require.NotNil(t, res.Extract)
	assert.Len(t, res.Extract.Rows, 2, "both rows emitted")

	require.Len(t, res.Nodes, 2)
	assert.NotEqual(t, res.Nodes[0].GetId(), res.Nodes[1].GetId(),
		"distinct identities must produce DISTINCT node ids — that is the property the refusal protects")
}

// TestRunRecipe_DuplicateIdentity_ThreeRowsRefusedOnTheSecond is the assertion
// that REJECTS the plausible-but-wrong implementation.
//
// A guard that scans the assembled node list at the END of the run refuses the
// same emission sets, so every other test in this file passes against it. What it
// cannot do is fire at the FIRST collision while the row is in hand — by the end
// of the run the third row has been emitted too, and the error can name nothing
// but an id. Naming the second row AND not the third is what discriminates.
func TestRunRecipe_DuplicateIdentity_ThreeRowsRefusedOnTheSecond(t *testing.T) {
	caller := sourceOnlyCaller(
		section("a-row", "Message Router"),
		section("b-row", "Message Router"),  // collides with a-row → refusal fires HERE
		section("c-row", "Message Channel"), // must never be reached
	)

	_, err := runInline(t, caller, simpleEmitRecipeBody)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "b-row",
		"the refusal must name the row that collided, which only a guard holding the row can do")
	assert.NotContains(t, err.Error(), "c-row",
		"the refusal must fire at the FIRST collision and never reach the third row — an end-of-run scan would have walked past it")
}
