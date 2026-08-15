// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// graphNameCallCount reports how many RETURN_MODE_GRAPH_NAMES enumerations the
// fixture backend served. Zero is the whole point of the working-set walk, so the
// counter exists to make that zero observable rather than inferred.
func (e *reconcileEngine) graphNameCallCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.graphNameCalls
}

// TestSegmentBearingGraphs_IsTheWorkingSet pins that the reconcile's graph set IS
// the working set, read locally, at zero enumeration cost.
//
// The two halves are one test on purpose: the empty half asserts a zero-length walk
// AND zero enumeration RPCs, and a zero-length walk is exactly what a fixture that
// was never wired would also produce. The admitted half is its known-positive
// control — the same client, the same backend, one admission, and now the walk
// yields exactly that graph while the enumeration counter stays at zero.
//
// The working set is set EXPLICITLY here rather than inherited from the fixture
// constructor: the reconcile fixtures declare their own working set, so a test whose
// subject is the emptiness of that set has to own it, or it would be asserting the
// constructor's default instead of the function under test.
func TestSegmentBearingGraphs_IsTheWorkingSet(t *testing.T) {
	c, eng := buildReconcileClient(t)

	// This test's subject is the working set, not local presence: it asks which
	// graphs an INTERACTION earns, so the second membership condition is stubbed
	// present rather than left to depend on whether this machine happens to have a
	// checkout named admittedRepo. The presence condition has its own test.
	c.localPresence = func(kgtypes.GraphType, string) bool { return true }

	// Register instances the RETIRED per-type enumeration would have discovered. A
	// walk that still enumerated would find these and return them, so their absence
	// below is a claim about the mechanism, not about an empty backend.
	eng.namesByType[string(kgtypes.GraphCode)] = []string{"enumeratedRepo"}
	eng.namesByType[string(kgtypes.GraphCloud)] = []string{"enumeratedAcct"}

	c.workingSet = workingset.New() // interacted with nothing yet.

	require.Empty(t, c.segmentBearingGraphs(),
		"an empty working set walks NOTHING — not even the graphs the backend would enumerate")
	require.Equal(t, 0, eng.graphNameCallCount(),
		"the walk issues no enumeration RPC: the ENUMERATION is scoped, not merely the walk that follows it")

	// KNOWN-POSITIVE CONTROL: one admission, and the same call now yields exactly
	// that graph — still without asking the backend to enumerate anything.
	c.AdmitGraph(kgtypes.GraphCode, "admittedRepo", "search")

	require.Equal(t,
		[]segmentGraphRef{{gt: kgtypes.GraphCode, name: "admittedRepo"}},
		c.segmentBearingGraphs(),
		"one admission walks exactly that graph — and none of the enumerable ones")
	require.Equal(t, 0, eng.graphNameCallCount(),
		"admission changes the membership, never the cost: still zero enumeration RPCs")
}
