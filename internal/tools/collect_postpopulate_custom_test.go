// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// collect_postpopulate_custom_test.go — R8: a collect that dispatches to a
// REGISTRATION runs no compiled-in post-populate hook and writes into no graph
// but its own.
//
// THE HOOK KEY IS THE COLLECTOR TYPE, which is why the dispatch fix alone does
// not discharge this. postPopulateGraphType and postpopulate.Lookup are both
// keyed on a.Type, so a registration shadowing a name with a compiled-in hook
// presents the identical string and the family-broad arm enumerates EVERY graph
// of that family and fires against each. Seven of the eight compiled-in hooks
// declare BreadthFamilyBroad across two graph families, so the
// guard is keyed on the REGISTERED-CUSTOM FACT and on no family.
//
// THE FIXTURE IS A COMPOSITION, because no single existing fake can observe this:
// customDeps.GraphCaller() is nil, so the tail takes its GraphCaller-unavailable
// arm and fires on nothing — a non-reproduction the fixture would manufacture —
// while tailRoutingDeps.GraphTypeCRUD() is nil, so it can resolve no
// registration. customTailDeps embeds the first and overrides the caller with the
// second's recorder.

// customTailDeps is customDeps with the seededBreadthDeps recorder wired into
// both graph-caller accessors. Every other ClientDeps method promotes from the
// embedded fake, WakePipeline included, so R4's instrument still works.
type customTailDeps struct {
	*customDeps
	gc GraphCaller
}

func (d *customTailDeps) GraphCaller() GraphCaller      { return d.gc }
func (d *customTailDeps) LocalGraphCaller() GraphCaller { return d.gc }

// registerCountingHook registers a family-broad post-populate hook under
// collectorType that records the graph names it fired against.
func registerCountingHook(collectorType string) func() []string {
	var mu sync.Mutex
	var fired []string
	postpopulate.Register(collectorType, postpopulate.BreadthFamilyBroad, func(_ context.Context, _ postpopulate.GraphCaller, name string) error {
		mu.Lock()
		fired = append(fired, name)
		mu.Unlock()
		return nil
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), fired...)
	}
}

// customTailFixture wires the composed deps for one arm: the stub collector type
// mapped onto graphType, a counting hook registered under it, a seeded
// enumeration of that family's graphs, and a registration record when registered
// is true.
func customTailFixture(t *testing.T, graphType kgtypes.GraphType, familyWire string, registered bool) (
	deps ClientDeps, firedNames func() []string, recorder *fakeGraphCaller, sink *capturingSink,
) {
	t.Helper()
	registerShadowStub(t)
	mapGraphTypeForTest(t, shadowStubType, graphType)
	firedNames = registerCountingHook(shadowStubType)

	_, recorder = seededBreadthDeps(familyWire, "acct-a", "acct-b", "acct-c")

	var defs []namedEntry
	if registered {
		url := startCustomProvider(t, conformingCustomPayload())
		defs = append(defs, namedCustomDef(shadowStubType, url))
	}
	inner := newCustomDeps(t, defs...)
	return &customTailDeps{customDeps: inner, gc: recorder}, firedNames, recorder, inner.sink
}

// TestPostCollectPostPopulate_RegisteredCustomFiresNoCompiledInHook is R8 ROW 1,
// the negative, asserted on all THREE of the requirement's clauses: the hook
// fires zero times, the family enumeration is never issued, and nothing is
// written against any graph but the collect's own.
//
// IT RUNS ON BOTH GRAPH FAMILIES a compiled-in family-broad hook can be
// registered onto. A guard keyed on the cloud family alone would pass the first
// sub-test and fail the second, which is exactly the mistake the family-agnostic
// fixture exists to catch.
func TestPostCollectPostPopulate_RegisteredCustomFiresNoCompiledInHook(t *testing.T) {
	for _, tc := range []struct {
		name       string
		graphType  kgtypes.GraphType
		familyWire string
	}{
		{"practice family", kgtypes.GraphPractice, "practice"},
		{"practice family", kgtypes.GraphPractice, "practice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, firedNames, recorder, sink := customTailFixture(t, tc.graphType, tc.familyWire, true)

			handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
			require.True(t, handled)
			require.False(t, res.IsError, resultText(res))

			assert.Empty(t, firedNames(),
				"a collect that dispatched to a registration must run NO compiled-in post-populate hook")
			assert.False(t, enumerated(recorder),
				"it must not even enumerate the family: the enumeration is the read that precedes every write")
			for _, r := range sink.results {
				assert.Equal(t, kgtypes.GraphType(shadowStubType), r.GraphType,
					"a registered-custom collect writes into no graph other than its own")
				assert.Equal(t, "own-graph", r.GraphName)
			}
		})
	}
}

// TestPostCollectPostPopulate_BuiltinCollectStillFiresTheHook is R8 ROW 2, the
// SAME-RUN KNOWN-POSITIVE. The identical stub type with NO registration takes the
// builtin path, and its compiled-in hook must still fire against every enumerated
// graph of the family. Without this arm, disabling the tail outright would
// satisfy ROW 1.
func TestPostCollectPostPopulate_BuiltinCollectStillFiresTheHook(t *testing.T) {
	for _, tc := range []struct {
		name       string
		graphType  kgtypes.GraphType
		familyWire string
	}{
		{"practice family", kgtypes.GraphPractice, "practice"},
		{"practice family", kgtypes.GraphPractice, "practice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, firedNames, recorder, _ := customTailFixture(t, tc.graphType, tc.familyWire, false)

			handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
			require.True(t, handled)
			require.False(t, res.IsError, resultText(res))

			assert.Equal(t, []string{"acct-a", "acct-b", "acct-c"}, firedNames(),
				"a BUILTIN collect must still fire its family-broad hook against every enumerated graph")
			assert.True(t, enumerated(recorder), "the builtin arm must still issue the family enumeration")
		})
	}
}

// TestPostCollectPostPopulate_RegisteredCustomIsCapabilityAbsenceNotWorkFailure
// is R8 ROW 3, the error class, asserted at the TOOL BOUNDARY rather than on the
// orchestrator's return value: a skipped hook is CAPABILITY ABSENCE, so the
// collect result must be a success. Reading the ToolResult keeps this a behavior
// gate rather than a compile-time tautology about a signature.
func TestPostCollectPostPopulate_RegisteredCustomIsCapabilityAbsenceNotWorkFailure(t *testing.T) {
	deps, _, _, _ := customTailFixture(t, kgtypes.GraphPractice, "practice", true)

	handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
	require.True(t, handled)
	assert.False(t, res.IsError,
		"skipping a compiled-in hook for a registered-custom collect is capability absence, never work failure: %s",
		resultText(res))
	assert.Contains(t, resultText(res), "Collected "+shadowStubType+" own-graph")
}

// TestPostCollectPostPopulate_DirectDriveGuard drives runPostCollectPostPopulate
// directly on both values of the registered-custom fact, so the guard is
// observable without a whole collect and the two directions sit side by side.
func TestPostCollectPostPopulate_DirectDriveGuard(t *testing.T) {
	const stubType = "t18-direct-drive-stub"
	mapGraphTypeForTest(t, stubType, kgtypes.GraphPractice)
	firedNames := registerCountingHook(stubType)

	t.Run("registered custom: no hook, no enumeration, no error", func(t *testing.T) {
		deps, recorder := seededBreadthDeps("practice", "acct-a", "acct-b", "acct-c")
		err := runPostCollectPostPopulate(context.Background(), deps, stubType, "own-graph", true)
		require.NoError(t, err)
		assert.Empty(t, firedNames())
		assert.False(t, enumerated(recorder))
	})

	t.Run("builtin: the hook fires against the whole family", func(t *testing.T) {
		deps, recorder := seededBreadthDeps("practice", "acct-a", "acct-b", "acct-c")
		err := runPostCollectPostPopulate(context.Background(), deps, stubType, "own-graph", false)
		require.NoError(t, err)
		assert.Equal(t, []string{"acct-a", "acct-b", "acct-c"}, firedNames())
		assert.True(t, enumerated(recorder))
	})
}
