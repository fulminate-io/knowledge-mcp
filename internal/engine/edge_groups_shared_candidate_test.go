// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestGroupCandidateEdges_SharedCandidateKeepsBothMemberships is the END of the
// chain the four-part edge identity exists to keep intact, and it is the gate
// that states the actual requirement: no link is lost.
//
// THE SHAPE IT PROTECTS. Two reference sites in one declaration — say `sc.Drop`
// and `ec.Drop` — resolve to the SAME candidate set. Resolution forms one group
// per SITE, so each candidate receives one edge per group, and the two edges for
// a SHARED candidate carry the same (FromId, ToId, Type) with DIFFERENT group
// keys in Evidence. Under the old three-column edge identity those two were one
// row: one membership was silently dropped, so one of the two groups arrived
// here INCOMPLETE — missing exactly its shared candidate — and a reader could
// not tell a two-candidate group from a three-candidate one that lost a member.
//
// WHY THIS TEST IS AT THE RENDERER RATHER THAN AT THE TABLE. The table can only
// show that two rows exist; it cannot show that the two GROUPS a reader
// reconstructs from them are each complete. Grouping is keyed on
// (FromID, Evidence), so the reconstruction is where a dropped membership
// actually becomes a missing candidate — which is the harm, and the thing the
// schema change was spent to prevent.
func TestGroupCandidateEdges_SharedCandidateKeepsBothMemberships(t *testing.T) {
	const (
		from = "pkg/a.go:Caller"
		keyA = "pkg/a.go:8403:CALLS:scCache.Drop"
		keyB = "pkg/a.go:8407:CALLS:ecCache.Drop"
		// shared is the candidate BOTH sites resolve to — the row that used to
		// collapse. The other two are unique to their own group, so a group that
		// lost the shared member is still non-empty and would pass any
		// "the group exists" check; only its MEMBER SET shows the loss.
		shared  = "pkg/b.go:Cache.Drop"
		onlyToA = "pkg/c.go:SummaryCache.Drop"
		onlyToB = "pkg/d.go:EmbedCache.Drop"
	)
	edge := func(to, evidence string) knowledgev1.Edge {
		return knowledgev1.Edge{
			FromId: from, ToId: to, Type: "CALLS",
			Method:     kgtypes.EdgeMethodAmbiguousName,
			Evidence:   evidence,
			Confidence: 0.5,
		}
	}
	// Interleaved on purpose: the two memberships of the shared candidate are NOT
	// adjacent, so a grouping that keyed on anything positional would be caught.
	edges := []knowledgev1.Edge{
		edge(shared, keyA),
		edge(onlyToA, keyA),
		edge(shared, keyB),
		edge(onlyToB, keyB),
	}

	groups, passthrough := GroupCandidateEdges(edges)

	require.Empty(t, passthrough,
		"every edge here is a candidate edge, so none should fall through to the ungrouped remainder")
	require.Len(t, groups, 2,
		"two reference sites means two groups; got %d — the two group keys were merged", len(groups))

	// Ranged BY INDEX, not by value: knowledgev1.Edge embeds a protobuf
	// MessageState carrying a sync.Mutex, so a value range copies a lock and
	// go vet rejects it.
	membersByKey := map[string][]string{}
	for i := range groups {
		g := &groups[i]
		for j := range g.Members {
			membersByKey[g.Key] = append(membersByKey[g.Key], g.Members[j].ToId)
		}
	}

	// THE LOAD-BEARING ASSERTION: the shared candidate appears in BOTH groups.
	// Under the old identity exactly one of these two was missing it.
	assert.ElementsMatch(t, []string{onlyToA, shared}, membersByKey[keyA],
		"group %q lost a member: a shared candidate must appear in EVERY group that resolved to it, "+
			"not only in whichever group's row happened to survive", keyA)
	assert.ElementsMatch(t, []string{onlyToB, shared}, membersByKey[keyB],
		"group %q lost a member: a shared candidate must appear in EVERY group that resolved to it, "+
			"not only in whichever group's row happened to survive", keyB)

	// KNOWN-POSITIVE CONTROL FOR THE TWO ASSERTIONS ABOVE. ElementsMatch against
	// an empty actual and an empty expected passes; both expectations here are
	// non-empty, and this makes the shared candidate's DOUBLE presence explicit as
	// a count rather than leaving it implied by two separate set comparisons.
	shares := 0
	for _, members := range membersByKey {
		for _, to := range members {
			if to == shared {
				shares++
			}
		}
	}
	assert.Equal(t, 2, shares,
		"the shared candidate must be a member TWICE — once per group — but appeared %d time(s); "+
			"one occurrence means the two memberships collapsed into a single edge somewhere upstream", shares)
}
