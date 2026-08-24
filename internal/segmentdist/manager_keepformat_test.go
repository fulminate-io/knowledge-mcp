// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestFormatFamiliesAreDisjointUnderKeepFormat asserts the ping-pong break AT THE
// MECHANISM rather than arguing it in prose.
//
// THE LOOP THIS PREVENTS: two clients of one account at different versions
// sharing a single format name. Each rejects the other's blobs as unreadable and
// rebuilds them in its own layout, and each rebuild is the other's next
// rejection — forever. A version-carrying name makes the two layouts disjoint
// families, so neither client ever SEES the other's metas and each rebuilds once.
//
// BOTH DIRECTIONS ARE ASSERTED, and that is the point. Showing only that the new
// client filters out old metas would leave the loop half-open: it is equally
// required that an OLD client does not pick up NEW segments it cannot read.
func TestFormatFamiliesAreDisjointUnderKeepFormat(t *testing.T) {
	t.Parallel()

	const retiredName = "hnsw"
	current := hnsw.New().Name()
	require.Equal(t, "hnswv3", current, "the shipped format name must be version-carrying")
	require.NotEqual(t, retiredName, current,
		"the families are only disjoint if the names actually differ — this is the whole mechanism")

	metas := []searchengine.SegmentMeta{
		{ID: "old-family", Format: retiredName},
		{ID: "new-family", Format: current},
	}
	keptBy := func(format string) []searchengine.SegmentID {
		dm, _ := newReclaimManager(t, t.TempDir())
		dm.format = format
		var kept []searchengine.SegmentID
		for _, meta := range metas {
			if dm.keepFormat(meta.Format) {
				kept = append(kept, meta.ID)
			}
		}
		return kept
	}

	require.Equal(t, []searchengine.SegmentID{"new-family"}, keptBy(current),
		"a client on the current format must not see the retired family's segments")
	require.Equal(t, []searchengine.SegmentID{"old-family"}, keptBy(retiredName),
		"a client on the retired format must not see the current family's segments either — "+
			"the other half of the ping-pong")

	// KNOWN-POSITIVE: an unpinned manager keeps BOTH. Without it, a keepFormat that
	// rejected everything unfamiliar — or one wired to return false — would satisfy
	// both assertions above while filtering for the wrong reason.
	require.Len(t, keptBy(""), 2, "an unpinned manager filters nothing")
}
