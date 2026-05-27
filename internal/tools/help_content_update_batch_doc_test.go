// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHelpMutate_DocumentsUpdateBatch pins the help block — the
// `update_batch` subsection in the mutate help must call out the
// all-or-nothing semantics + backend-backed rejection so the planner
// agent's discoverability survives doc refactors. Relocated client-side
// alongside helpMutate per FUL-251.
func TestHelpMutate_DocumentsUpdateBatch(t *testing.T) {
	assert.Contains(t, helpMutate, "## operation: update_batch", "helpMutate missing update_batch subsection")
	assert.Contains(t, helpMutate, "all-or-nothing", "helpMutate update_batch should call out all-or-nothing")
	assert.Contains(t, helpMutate, "backend-backed", "helpMutate update_batch should call out backend-rejection")
}
