// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collect_linker_custom_gate_test.go — the post-collect cross-graph linker does
// not run for a collect that dispatched to a REGISTRATION, whatever the
// registration is named.
//
// THE COLLECTOR-TYPE CHECK IS NOT A SUFFICIENT GATE, which is why this file
// exists beside collect_linker_trigger_test.go. postCollectLinkerType is a type
// STRING, and a registration presenting that string would be admitted by the
// type check alone — the linker would then read and write a built-in code graph
// on behalf of a provider whose data none of it is. The gate is keyed on the
// REGISTERED-CUSTOM FACT, the same value and the same resolution the
// post-populate sibling reads.
//
// THE TRIGGER NAME CANNOT CARRY A REGISTRATION TODAY, and that is a reason to
// keep the guard rather than to drop it. `code` is a builtin GRAPH TYPE, so
// graphtypecrud refuses a registration under it and the collision this gate
// answers is unreachable from production right now. It was reachable one release
// ago, when the trigger set was seven provider names any of which could carry a
// registration, and it becomes reachable again the moment a trigger is added for
// a name that is a collector rather than a graph type. A guard whose collision is
// currently unreachable is not a guard that is wrong.
//
// THE DISCRIMINATING PAIR IS THE DIRECT DRIVE at the bottom of this file: the
// same collector type, the same deps, the same wire instrument, differing in the
// REGISTERED-CUSTOM FACT ALONE. The whole-collect arms above it observe the other
// half — that a registered-custom collect reaches the tail and leaves the wire
// untouched — with the pipeline wake as their same-run proof that the tail ran at
// all.

// t19LinkerStubType is the test-only collector name the known-positive arm
// registers a compiled-in collector under. It is deliberately NOT shadowStubType:
// that name has a process-global post-populate hook registered against it by the
// post-populate tests, whose graph-type mapping is installed per-test, so a
// builtin collect under it would fail or pass depending on test order.
const t19LinkerStubType = "t19-linker-gate-stub"

var t19LinkerStubOnce sync.Once

// t19LinkerStubCollector is the compiled-in half of the pair: a collector that
// ships one node through the injected sink, exactly as shadowStubCollector does,
// so a collect under this name completes and reaches the post-collect tail.
type t19LinkerStubCollector struct{}

func (t19LinkerStubCollector) Name() string { return t19LinkerStubType }

func (t19LinkerStubCollector) Collect(_ context.Context, id string, opts collector.CollectOptions) (*collectorwire.CollectResult, error) {
	t19LinkerStubCalls.Add(1)
	res := &collectorwire.CollectResult{
		GraphType:    kgtypes.GraphPractice,
		GraphName:    "internal-" + id,
		Nodes:        []*knowledgev1.Node{{Id: "INTERNAL-1", Type: "resource"}},
		WalkComplete: true,
	}
	if opts.Sink != nil {
		if err := opts.Sink.WriteResult(context.Background(), t19LinkerStubType, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// t19LinkerStubCalls counts the compiled-in collector's runs. Package-level
// because the collector registry is a process global and the stub is registered
// once per test binary.
var t19LinkerStubCalls atomic.Int32

// registerT19LinkerStub registers the compiled-in collector once and resets its
// counter for the calling test. collector.Register panics on a duplicate and has
// no Unregister, hence the sync.Once — the same idiom registerShadowStub uses.
func registerT19LinkerStub(t *testing.T) {
	t.Helper()
	t19LinkerStubOnce.Do(func() { collector.Register(t19LinkerStubCollector{}) })
	t19LinkerStubCalls.Store(0)
}

// customLinkerFixture wires a whole-collect fixture whose GraphCaller is the
// wire-counting linker instrument. registered=true stands a conforming custom
// provider up and registers it under family, so the collect dispatches to the
// REGISTRATION; registered=false leaves the catalog empty, so the collect takes
// the builtin path.
func customLinkerFixture(t *testing.T, family string, registered bool) (ClientDeps, *linkerTriggerCaller, *customDeps) {
	t.Helper()
	var defs []namedEntry
	if registered {
		url := startCustomProvider(t, conformingCustomPayload())
		defs = append(defs, namedCustomDef(family, url))
	}
	inner := newCustomDeps(t, defs...)
	gc := &linkerTriggerCaller{}
	return &customTailDeps{customDeps: inner, gc: gc}, gc, inner
}

// TestRunPostCollectLinker_RegisteredCustomCollectRunsNoLinker is R1's whole-
// collect observable, driven through the tool boundary rather than the helper.
//
// THREE ARMS, ONE INSTRUMENT. The wire count is linkerTriggerCaller's Execute
// counter, which is what "the pass ran" looks like from outside: the Dockerfile
// pass's first act is a node read over the Execute seam.
//
//	(a) REGISTERED CUSTOM under a test-only name that a compiled-in collector
//	    ALSO holds: the shadowing case, where the type string alone cannot tell
//	    the two paths apart.
//	(b) REGISTERED CUSTOM under the literal name "gcp": the same case under a
//	    name an operator's config is likely to carry.
//	(c) REGISTERED CUSTOM under a name nothing else claims: the same-run control
//	    that attributes (a) and (b)'s zeros to the gate rather than to the custom
//	    collect path touching no wire in general.
//
// EACH ARM CARRIES ITS OWN SAME-RUN KNOWN-POSITIVE: the pipeline wake count. A
// zero wire count from a tail that never ran would prove nothing; wake==1 says
// the tail ran to its end and the linker declined inside it.
func TestRunPostCollectLinker_RegisteredCustomCollectRunsNoLinker(t *testing.T) {
	t.Run("registered custom collect of a shadowed collector name runs no linker", func(t *testing.T) {
		registerT19LinkerStub(t)

		deps, gc, inner := customLinkerFixture(t, t19LinkerStubType, true)
		handled, res := callCollect(deps, `{"type":"`+t19LinkerStubType+`","id":"acct-a"}`)
		require.True(t, handled)
		require.False(t, res.IsError, resultText(res))

		require.Equal(t, int32(0), t19LinkerStubCalls.Load(),
			"the registration must have won the dispatch over the compiled-in collector")
		assert.Zero(t, gc.execs.Load(),
			"a collect that dispatched to a registration must run NO post-collect linker: it walks graphs whose provider it is not")
		assert.False(t, gc.wireTouched(), "the whole post-collect tail must touch the wire zero times")
		assert.Equal(t, 1, inner.wakeCount(),
			"R4: the pipeline wake still follows the gated tail — the nodes the collect uploaded still need summarizing")
	})

	t.Run("registered custom collect named gcp runs no linker", func(t *testing.T) {
		deps, gc, inner := customLinkerFixture(t, "gcp", true)
		handled, res := callCollect(deps, `{"type":"gcp","id":"proj-a"}`)
		require.True(t, handled)
		require.False(t, res.IsError, resultText(res))

		assert.Zero(t, gc.execs.Load(),
			"a registration named gcp must run no post-collect linker, whatever the trigger set holds")
		assert.Equal(t, 1, inner.wakeCount(), "R4: the pipeline wake is still reached")
	})

	t.Run("registered custom collect of a family nothing else claims is the zero control", func(t *testing.T) {
		deps, gc, _ := customLinkerFixture(t, "t19-not-allowlisted", true)
		handled, res := callCollect(deps, `{"type":"t19-not-allowlisted","id":"inst-a"}`)
		require.True(t, handled)
		require.False(t, res.IsError, resultText(res))

		assert.Zero(t, gc.execs.Load(),
			"a family nothing else claims touched the wire zero times before this change too — it attributes the zeros above to the linker")
	})
}

// TestRunPostCollectLinker_DirectDriveRegisteredCustomGuard drives the helper
// directly on BOTH values of the registered-custom fact for the ONE trigger
// type, so the guard is observable without a whole collect and the two
// directions sit side by side. It is the shape
// TestPostCollectPostPopulate_DirectDriveGuard uses for the sibling gate.
//
// THE BUILTIN ROW IS THE REGRESSION FENCE. The gate must skip the pass for a
// REGISTRATION, never for the family: with the false row absent, deleting the
// registered-custom check entirely would still satisfy the true row.
func TestRunPostCollectLinker_DirectDriveRegisteredCustomGuard(t *testing.T) {
	const collectedGraph = "some-repo"

	t.Run("registered custom", func(t *testing.T) {
		gc := &linkerTriggerCaller{}
		runPostCollectLinker(context.Background(), newLinkerTriggerDeps(gc), postCollectLinkerType, collectedGraph, true)
		assert.False(t, gc.wireTouched(),
			"a collect that resolved to a REGISTRATION must run no linker, whatever the family is named")
	})
	t.Run("builtin", func(t *testing.T) {
		gc := &linkerTriggerCaller{}
		runPostCollectLinker(context.Background(), newLinkerTriggerDeps(gc), postCollectLinkerType, collectedGraph, false)
		assert.True(t, gc.wireTouched(),
			"a collect that resolved to a COMPILED-IN collector must still run the linker")
	})
}
