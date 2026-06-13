// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSearchToolDef_SimilarModeDocumented is Phase 4's criterion: SearchToolDef's
// schema carries a node_id string Property (non-empty Description) and the mode
// Property Description names 'similar' with the stored-vector-space semantics,
// self-exclusion, and the reciprocal-rank-fusion (not raw cosine) score disclosure.
// It must NOT carry a drain/seconds-on-large-graphs cost caution (the superseded
// design). Fails-when-absent: a missing node_id property or a mode Description that
// never names 'similar' fails the assertion.
func TestSearchToolDef_SimilarModeDocumented(t *testing.T) {
	def := SearchToolDef()
	props := def.InputSchema.Properties

	nodeID, ok := props["node_id"]
	require.True(t, ok, "SearchToolDef must carry a node_id property for mode:similar")
	assert.Equal(t, "string", nodeID.Type)
	assert.NotEmpty(t, nodeID.Description, "node_id Description must be non-empty (catalog guard)")

	mode, ok := props["mode"]
	require.True(t, ok, "SearchToolDef must carry a mode property")
	d := mode.Description
	assert.Contains(t, d, "similar", "mode Description names the similar mode")
	assert.Contains(t, strings.ToLower(d), "stored vector", "mode Description discloses the stored-vector space")
	assert.Contains(t, strings.ToLower(d), "exclude", "mode Description discloses self-exclusion")
	assert.Contains(t, strings.ToLower(d), "fusion", "mode Description discloses the rank-fusion scoring")
	assert.Contains(t, strings.ToLower(d), "cosine", "mode Description clarifies the score is NOT raw cosine")
}
