// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHelpMutate_DocumentsUpdateBatch pins the help block — the
// `update_batch` subsection in the mutate help must call out the
// all-or-nothing semantics + the backend rejection so the planner
// agent's discoverability survives doc refactors. Relocated client-side
// alongside helpMutate.
//
// The backend assertion reads "backend-tagged" rather than the
// "backend-backed" it once pinned: the check rejects an ITEM carrying a
// backend key, and does not detect a target node that is itself
// tracker-backed, so the older phrase pinned a claim the code does not make.
// The discoverability this test exists to protect is unchanged.
func TestHelpMutate_DocumentsUpdateBatch(t *testing.T) {
	assert.Contains(t, helpMutate, "## operation: update_batch", "helpMutate missing update_batch subsection")
	assert.Contains(t, helpMutate, "all-or-nothing", "helpMutate update_batch should call out all-or-nothing")
	assert.Contains(t, helpMutate, "backend-tagged", "helpMutate update_batch should call out backend-rejection")
}
