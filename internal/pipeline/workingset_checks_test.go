// SPDX-License-Identifier: Apache-2.0

package pipeline

// workingset_checks_test.go drives the WHOLE chain the checks graph depends on,
// end to end: a user interaction admits it, and the catalog loop registers its
// collector.
//
// IT EXISTS BECAUSE THE TWO HALVES WERE FIXED SEPARATELY AND EITHER ALONE IS
// INERT. The eligible-type filter had to learn about checks, and the working-set
// normalizer had to stop refusing a family that carries no instance field; with
// only the filter fixed nothing is ever admitted to filter, and with only the
// normalizer fixed the member is admitted and then dropped. Two tests flanking
// the seam would each have stayed green through the outage, so this one drives
// the value across it.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// TestChecksAdmissionRegistersItsCollector is the end-to-end reproduction of the
// live symptom: 18 check nodes at embedded=0 through repeated full drains,
// because no collector was ever registered for the graph holding them.
//
// THE ADMISSION IS THE ONLY SIGNAL DELIVERED. The catalog wake channel is never
// touched, and the never-assertion below establishes the baseline, so a
// registration appearing afterwards can only have come from the admission itself.
func TestChecksAdmissionRegistersItsCollector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake := newFakeWireClient()
	p := New(Config{}, fake, nil, nil)
	ws := workingset.New()
	p.AttachWorkingSet(ws)

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.RefreshLoadedGraphs(ctx)
	}()

	require.Never(t, func() bool { return len(registeredKeys(p)) > 0 }, 200*time.Millisecond, 20*time.Millisecond,
		"an empty working set registers nothing, so a registration below cannot be startup residue")

	// THE INTERACTION, SPELLED THE WAY THE CORPUS READER SPELLS IT. Every checks
	// read sends an EMPTY instance name because the selector policy rejects a set
	// one, so admitting under "" is the real shape — passing "default" here would
	// test a call no production site makes.
	require.True(t, ws.Admit(kgtypes.GraphChecks, "", "manage_checks"),
		"a manage_checks interaction must admit the checks graph")

	wantKey := graphKey{GraphType: kgtypes.GraphChecks, GraphName: "default"}
	require.Eventually(t, func() bool {
		_, ok := registeredKeys(p)[wantKey]
		return ok
	}, 2*time.Second, 10*time.Millisecond,
		"the admission alone must wake the catalog loop and register the checks collector — "+
			"without it the graph's nodes sit at embedded=0 through every drain with nothing reporting the gap")

	assert.Equal(t, map[graphKey]struct{}{wantKey: {}}, registeredKeys(p),
		"exactly the admitted graph — the admission registers its own graph and nothing else")
	assert.Zero(t, executeCallCount(fake), "waking and registering still costs no catalog RPC")

	cancel()
	<-done
	require.NoError(t, p.Stop(context.Background()))
}

// TestChecksStaysUnregisteredWithoutAnInteraction is the other half of the rule,
// and it is what stops the fix above from becoming "checks is always on".
//
// The working set exists to deny exactly this: a graph nobody touched gets no
// collector, however eligible its type is. A checks graph that registered without
// an interaction would be a seeded exception, which is the class the package's
// own doc says it has none of.
func TestChecksStaysUnregisteredWithoutAnInteraction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake := newFakeWireClient()
	p := New(Config{}, fake, nil, nil)
	ws := workingset.New()
	p.AttachWorkingSet(ws)

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.RefreshLoadedGraphs(ctx)
	}()

	// A DIFFERENT graph is admitted, so the loop is demonstrably awake and
	// registering. Without this the assertion below would hold just as well for a
	// loop that had died on entry.
	require.True(t, ws.Admit(kgtypes.GraphCode, "repoA", "search"))
	require.Eventually(t, func() bool {
		_, ok := registeredKeys(p)[graphKey{GraphType: kgtypes.GraphCode, GraphName: "repoA"}]
		return ok
	}, 2*time.Second, 10*time.Millisecond, "control: the loop must be awake and registering")

	_, registered := registeredKeys(p)[graphKey{GraphType: kgtypes.GraphChecks, GraphName: "default"}]
	assert.False(t, registered,
		"checks must earn its collector by interaction like every other graph — eligibility alone is not membership")

	cancel()
	<-done
	require.NoError(t, p.Stop(context.Background()))
}
