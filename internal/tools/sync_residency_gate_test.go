// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestSyncPush_RefusesSyncIneligibleBuiltins pins the residency gate the builtin
// arm never had: syncableGateRejection now refuses a builtin that
// kgtypes.SyncEligible does not admit, so "raw graphs never sync" is enforced
// rather than merely documented.
//
// THE NAME SAYS PUSH AND THE GATE IS WIDER THAN THAT. syncableGateRejection is
// called ONCE, from InterceptSync, for push AND pull alike — so this change
// refuses BOTH operations for these three graphs. That is intended: a pull of a
// never-pushed raw graph would otherwise reach handlePull and could overwrite the
// local copy with nothing.
//
// BOTH DIRECTIONS ARE ASSERTED, and the admit side is not optional. A gate that
// refused EVERY builtin would satisfy every refusal assertion below while breaking
// every real push, so the empty-rejection cases are what give the refusals their
// meaning.
//
// NIL deps IS SUFFICIENT: the builtin branch returns before it touches
// GraphTypeCRUD, so this test needs no harness setup.
func TestSyncPush_RefusesSyncIneligibleBuiltins(t *testing.T) {
	ctx := context.Background()

	// REFUSED. Each is PAIRED with a check that the predicate really reports the
	// graph sync-ineligible — otherwise a regressed SyncEligible and a correct gate
	// would be indistinguishable from a correct SyncEligible and a gate that refuses
	// everything, and this half would pass either way.
	for _, gt := range []kgtypes.GraphType{kgtypes.GraphWebRaw, kgtypes.GraphPDFRaw, kgtypes.GraphLogs} {
		assert.False(t, kgtypes.SyncEligible(gt),
			"fixture: %q must be sync-ineligible, or the refusal asserted below proves nothing about the gate", gt)

		msg := syncableGateRejection(ctx, nil, string(gt))
		assert.NotEmpty(t, msg,
			"sync push must REFUSE %q — it is sync-ineligible and its bytes must stay on this machine", gt)
		assert.Contains(t, msg, string(gt),
			"the refusal must NAME the graph so an operator can tell which of several arguments was rejected")
	}

	// ADMITTED — the control. These four are sync-eligible builtins and must still
	// pass the gate untouched; note that checks and practice are embed-enrolled just
	// like the raw graphs are, which is what makes them the right control: they
	// prove the gate keys on SYNC residency, not on whether a graph is indexed.
	for _, gt := range []kgtypes.GraphType{
		kgtypes.GraphKnowledge, kgtypes.GraphCode, kgtypes.GraphPractice, kgtypes.GraphChecks,
	} {
		assert.True(t, kgtypes.SyncEligible(gt), "fixture: %q must be sync-eligible", gt)
		assert.Empty(t, syncableGateRejection(ctx, nil, string(gt)),
			"sync must still ADMIT %q — a gate that refused every builtin would break every real push", gt)
	}
}
