// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQueryToolDef_NoVectorMode is the GAP-C regression guard (CEO decision:
// DROP): the query tool's mode enum must NOT advertise "vector". It was a dangling
// SEARCH-tool mode with no query compile arm, no intercept, and no server handler —
// post-cutover it denied. The fix drops it from the enum so the LLM is never
// offered a mode the query tool cannot serve. ("text" stays — query does serve a
// text search mode.)
func TestQueryToolDef_NoVectorMode(t *testing.T) {
	def := QueryToolDef()
	modeProp, ok := def.InputSchema.Properties["mode"]
	require.True(t, ok, "the query tool must declare a mode property")
	require.NotEmpty(t, modeProp.Enum, "the mode property must carry an enum")
	assert.NotContains(t, modeProp.Enum, "vector",
		"query(mode) must NOT advertise the dropped SEARCH-tool 'vector' mode (GAP-C)")
	// "stats" and "recent" remain — these are real query modes (GAP-A / GAP-B
	// fixes give them proper homes / validation). A sanity check the enum was not
	// gutted by the vector removal.
	assert.Contains(t, modeProp.Enum, "stats")
	assert.Contains(t, modeProp.Enum, "recent")
}
