// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHelpMutate_CensusRemainderPinned pins the outcome of the claim census
// over the remainder of this file — the update_batch internals, the delete
// block, and the create/update claims the first pass enumerated but did not
// clear. Most of that remainder verified clean; the assertions below cover the
// four that did not, plus the surviving accurate statements the repairs must
// not take down with them.
//
// It is deliberately a SECOND test rather than more assertions on the
// unbacked-claims guard: a gate pointed at that test would go green the moment
// the first repair landed, whether this census cleared its rows or none of them.
func TestHelpMutate_CensusRemainderPinned(t *testing.T) {
	// The retrieval guidance named the first sentence of the description as
	// what BM25 matches. Five weighted fields are indexed and the whole
	// description is among them.
	assert.NotContains(t, helpMutate, "first sentence of",
		"helpMutate must not claim BM25 matches only the first sentence of description")
	assert.Contains(t, helpMutate, "five weighted fields",
		"helpMutate must describe the real BM25 field set")

	// keywords named a display facet that exists nowhere in the client. The
	// double-weighted BM25 field it really is must stay documented.
	assert.NotContains(t, helpMutate, "keywords\n                    facet",
		"helpMutate must not advertise a keywords display facet")
	assert.Contains(t, helpMutate, "double-weighted BM25 field",
		"helpMutate must keep the real keywords behavior")

	// The batch backend check reads the ITEM's metadata, not the target node,
	// so a tracker-backed node is not detected and its edit does not sync. The
	// help said such nodes are rejected.
	assert.NotContains(t, helpMutate, "backend-backed nodes are rejected",
		"helpMutate must not claim the batch path rejects tracker-backed targets")
	assert.Contains(t, helpMutate, "ITEM's own metadata",
		"helpMutate must state what the batch backend check actually reads")

	// Prune-by-age selects on node type and no type is retention-eligible, so
	// the two worked prune examples could never run. Both shipped surfaces —
	// the help block and the wire schema — must say so.
	assert.NotContains(t, helpDelete, `delete({ "older_than": "7d" })`,
		"helpDelete must not show a prune example that cannot run")
	assert.Contains(t, helpDelete, "NOT CURRENTLY AVAILABLE",
		"helpDelete must tell the reader prune-by-age is unavailable")
	assert.Contains(t, DeleteToolDef().Description, "NOT CURRENTLY AVAILABLE",
		"the delete wire schema must tell the reader prune-by-age is unavailable")

	// A soft delete leaves edges; a hard delete sweeps them. The gotcha stated
	// the soft behavior unconditionally, one section after offering hard:true.
	assert.Contains(t, helpDelete, "A HARD delete sweeps every incident edge",
		"helpDelete must distinguish soft-delete edge retention from hard-delete edge sweep")

	// Claims the census checked and found accurate — here so a later rewrite
	// cannot satisfy the negatives above by deleting the blocks around them.
	assert.Contains(t, helpDelete, "A malformed hard value DENIES the delete",
		"helpDelete must keep the verified malformed-hard-flag contract")
	assert.Contains(t, helpMutate, "single store transaction with",
		"helpMutate must keep the verified all-or-nothing update_batch contract")
	assert.Contains(t, helpMutate, "decoded length must equal 32 bytes",
		"helpMutate must keep the verified binary_vector length contract")
}
