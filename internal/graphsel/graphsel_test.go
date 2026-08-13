// SPDX-License-Identifier: Apache-2.0

package graphsel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestInstanceKeyOf_RoundTripsEveryFamily drives the reverse read against
// selectors built by the PRODUCTION builder rather than by hand, which is what
// makes it pin both directions of one switch instead of comparing a literal
// against itself: a family added to InstanceField and wired into only one
// direction fails here.
func TestInstanceKeyOf_RoundTripsEveryFamily(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		gt   kgtypes.GraphType
		name string
	}{
		{kgtypes.GraphCode, "repoA"},
		{kgtypes.GraphCloud, "acct-1"},
		{kgtypes.GraphCICD, "org-1"},
		{kgtypes.GraphPractice, "go"},
		{kgtypes.GraphKnowledge, "default"},
	} {
		t.Run(string(tc.gt), func(t *testing.T) {
			t.Parallel()
			gotGT, gotName, ok := InstanceKeyOf(GraphSelectorFor(tc.gt, tc.name, false))
			require.True(t, ok)
			assert.Equal(t, tc.gt, gotGT)
			assert.Equal(t, tc.name, gotName)
		})
	}

	t.Run("an empty Graph is the knowledge default", func(t *testing.T) {
		t.Parallel()
		gt, name, ok := InstanceKeyOf(&knowledgev1.GraphSelector{})
		require.True(t, ok)
		assert.Equal(t, kgtypes.GraphKnowledge, gt)
		assert.Empty(t, name, `the ""→"default" collapse belongs to workingset.Normalize, not here`)
	})

	t.Run("a type-only target resolves no instance", func(t *testing.T) {
		t.Parallel()
		// The shape a catalog enumeration compiles to: a graph type and nothing else.
		gt, name, ok := InstanceKeyOf(&knowledgev1.GraphSelector{Graph: string(kgtypes.GraphCode)})
		require.True(t, ok, "the selector still names a graph TYPE")
		assert.Equal(t, kgtypes.GraphCode, gt)
		assert.Empty(t, name, "an enumeration must resolve no instance key")
	})

	t.Run("a nil selector addresses nothing", func(t *testing.T) {
		t.Parallel()
		_, _, ok := InstanceKeyOf(nil)
		assert.False(t, ok)
	})
}
