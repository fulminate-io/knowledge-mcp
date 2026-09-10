// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// collect_custom_shadow_test.go — R1 and R2: a registered custom family WINS the
// collect dispatch over a compiled-in collector of the same name, and a name with
// no registration still runs the builtin path byte-for-byte.
//
// THE COLLISION IS BUILT, NOT BORROWED. The four cloud collector packages are not
// linked into this test binary (`go list -deps -test ./internal/tools/` lists
// collector/cloud, the SHARED package, but none of cloud/{aws,azure,gcp,k8s}),
// so a test naming "gcp" here observes no collision at all and would pass
// vacuously. The colliding name is a fake collector registered under a test-only
// name through this package's sync.Once idiom — collector.Register panics on a
// duplicate and has no Unregister.

const shadowStubType = "t18-shadow-collision-stub"

var shadowStubOnce sync.Once

// shadowStubCollector is the "compiled-in internal collector" half of the
// collision: a collector registered under shadowStubType whose Collect records
// that it ran. R1's zero is this counter.
type shadowStubCollector struct{}

func (shadowStubCollector) Name() string { return shadowStubType }

func (shadowStubCollector) Collect(_ context.Context, id string, opts collector.CollectOptions) (*collectorwire.CollectResult, error) {
	shadowStubCalls.Add(1)
	res := &collectorwire.CollectResult{
		GraphType:    kgtypes.GraphPractice,
		GraphName:    "internal-" + id,
		Nodes:        []*knowledgev1.Node{{Id: "INTERNAL-1", Type: "resource"}},
		WalkComplete: true,
	}
	if opts.Sink != nil {
		if err := opts.Sink.WriteResult(context.Background(), shadowStubType, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// shadowStubCalls counts the internal collector's runs. Package-level because the
// collector registry is a process global and the stub is registered once.
var shadowStubCalls atomic.Int32

// registerShadowStub registers the fake internal collector exactly once per test
// binary and resets its counter for the calling test.
func registerShadowStub(t *testing.T) {
	t.Helper()
	shadowStubOnce.Do(func() { collector.Register(shadowStubCollector{}) })
	shadowStubCalls.Store(0)
}

// countingCustomProvider stands a conforming provider up and returns its URL plus
// a reader for how many times its tool was called.
func countingCustomProvider(t *testing.T) (url string, calls func() int32) {
	t.Helper()
	var n atomic.Int32
	u := startCustomProvider(t, func(*mcp.CallToolRequest) any {
		n.Add(1)
		return conformingCustomPayload()
	})
	return u, n.Load
}

// TestCustomCollect_RegistrationWinsOverACompiledInCollectorOfTheSameName is R1.
// THREE ARMS IN ONE RUN against one instrument:
//
//	(a) COLLIDING: the fake internal collector IS registered under the name AND a
//	    registration record exists for it. The provider must be reached, its tool
//	    called, the result shipped under the registration family, and the internal
//	    collector must not run.
//	(b) THE ZERO'S CONTROL (and R2): the same name with the fake registered and NO
//	    record. The internal collector runs and the provider does not, which is
//	    what makes (a)'s zero a real zero rather than a broken fixture.
//	(c) A NON-COLLIDING KNOWN-POSITIVE: a registered name with no internal
//	    collector at all still reaches its provider.
func TestCustomCollect_RegistrationWinsOverACompiledInCollectorOfTheSameName(t *testing.T) {
	registerShadowStub(t)
	collidingURL, collidingCalls := countingCustomProvider(t)
	plainURL, plainCalls := countingCustomProvider(t)

	// (a) COLLIDING — the registration must win.
	depsA := newCustomDeps(t, namedCustomDef(shadowStubType, collidingURL))
	handled, res := callCollect(depsA, `{"type":"`+shadowStubType+`","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	assert.Equal(t, int32(1), collidingCalls(),
		"the registration's provider must be reached even though a collector of the same name is compiled in")
	assert.Equal(t, int32(0), shadowStubCalls.Load(),
		"the compiled-in collector of the same name must not run at all")

	// THE SHIPPED IDENTITY, not only the counts: the pre-fix run shipped under an
	// empty graphType and graphName while reporting isError=false, so counts alone
	// would not have caught it.
	got := depsA.sink.last()
	require.NotNil(t, got, "the collect must have shipped a result")
	assert.Equal(t, kgtypes.GraphType(shadowStubType), got.GraphType,
		"the result must land under the REGISTRATION's family")
	assert.Equal(t, "board", got.GraphName, "the collect id is the instance inside that family")
	require.Len(t, got.Nodes, 1)
	assert.Equal(t, "ISSUE-1", got.Nodes[0].GetId(), "the nodes must be the PROVIDER's, not the internal collector's")

	// (b) THE CONTROL — same name, no registration: today's builtin path.
	depsB := newCustomDeps(t)
	handled, res = callCollect(depsB, `{"type":"`+shadowStubType+`","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	assert.Equal(t, int32(1), shadowStubCalls.Load(),
		"with no registration the compiled-in collector of that name must run, which is what makes arm (a)'s zero a real zero")
	assert.Equal(t, int32(1), collidingCalls(), "the provider must not be called a second time on the unregistered arm")

	// (c) THE NON-COLLIDING KNOWN-POSITIVE — a registered name no collector holds.
	depsC := newCustomDeps(t, namedCustomDef(customStubFamily, plainURL))
	handled, res = callCollect(depsC, `{"type":"`+customStubFamily+`","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	assert.Equal(t, int32(1), plainCalls(), "a registered family with no compiled-in collector must still reach its provider")
}

// failingGraphTypeCRUD errors on every read and records that it was consulted. A
// walk that short-circuits before the catalog never touches it, which is what
// makes "the stub was never called" a POSITIVE assertion rather than an absence.
type failingGraphTypeCRUD struct{ calls atomic.Int32 }

func (f *failingGraphTypeCRUD) List(context.Context) ([]*knowledgev1.GraphTypeDef, error) {
	f.calls.Add(1)
	return nil, errors.New("catalog read must not happen for a builtin graph type")
}

func (f *failingGraphTypeCRUD) ByName(context.Context, string) (*knowledgev1.GraphTypeDef, bool, error) {
	f.calls.Add(1)
	return nil, false, errors.New("catalog read must not happen for a builtin graph type")
}

func (f *failingGraphTypeCRUD) Update(context.Context, *knowledgev1.GraphTypeDef) error { return nil }

// TestLookupCustomCollector_BuiltinGraphTypeNamesSpendNoCatalogRead is R2's
// short-circuit arm. code, web and pdf are the three collect types that are also
// BUILTIN GRAPH TYPES, and a name IsBuiltinGraphType matches can never hold a
// registration (graphtypecrud.validateRegistration refuses it on the same
// predicate), so its catalog read is provably useless and must not be issued.
//
// IT DRIVES lookupCustomCollector DIRECTLY rather than InterceptCollect: a
// collect(type:"code") that got past the lookup would run the real code
// collector against this machine, and the observation — whether the catalog was
// consulted — belongs to this function anyway.
func TestLookupCustomCollector_BuiltinGraphTypeNamesSpendNoCatalogRead(t *testing.T) {
	for _, name := range []string{"code", "web", "pdf"} {
		crud := &failingGraphTypeCRUD{}
		deps := &customDeps{sink: &capturingSink{}, crud: crud}

		def, err := lookupCustomCollector(context.Background(), deps, name)

		require.NoError(t, err, "a builtin graph type must resolve without consulting the catalog: %s", name)
		assert.Nil(t, def, "a builtin graph type resolves to no registration: %s", name)
		assert.Equal(t, int32(0), crud.calls.Load(),
			"%q is a builtin graph type, so the catalog read is provably useless and must be short-circuited", name)
	}
}

// TestLookupCustomCollector_InputClasses covers the classes the dispatch's
// specification names, each with the answer the caller depends on.
func TestLookupCustomCollector_InputClasses(t *testing.T) {
	registerShadowStub(t)
	ctx := context.Background()

	t.Run("a builtin COLLECTOR name that is not a graph type, REGISTERED, resolves to the record", func(t *testing.T) {
		deps := newCustomDeps(t, namedCustomDef(shadowStubType, "http://example.invalid"))
		def, err := lookupCustomCollector(ctx, deps, shadowStubType)
		require.NoError(t, err)
		require.NotNil(t, def)
		assert.Equal(t, shadowStubType, def.Def.GetName())
	})

	t.Run("the same name UNREGISTERED resolves to the builtin path", func(t *testing.T) {
		deps := newCustomDeps(t)
		def, err := lookupCustomCollector(ctx, deps, shadowStubType)
		require.NoError(t, err)
		assert.Nil(t, def)
	})

	t.Run("a name in neither registry, on a session that read NO project file, says the project scope went unread", func(t *testing.T) {
		// THE ANSWER CHANGED WITH THE WHICH-REPOSITORY RULING'S SECOND CLAUSE, and
		// the change is deliberate: from a session carrying no working directory
		// the daemon CANNOT distinguish a typo from a family registered in a
		// repository's project file it never opened. It says which of those it
		// knows — that it read no project file — rather than asserting the
		// collector does not exist.
		deps := newCustomDeps(t)
		def, err := lookupCustomCollector(ctx, deps, "t18-name-in-neither-registry")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "t18-name-in-neither-registry")
		assert.Contains(t, err.Error(), "NO project-scope file was read")
		assert.Nil(t, def)
	})

	t.Run("the same name on a session that DID read a project file falls through to the builtin refusal", func(t *testing.T) {
		// THE CONTROL FOR THE ROW ABOVE. Here a project file was read and the name
		// is genuinely absent from it, so the generic unknown-type refusal is the
		// correct answer and the lookup must stay silent.
		root := t.TempDir()
		require.NoError(t, collectorconfig.Write(
			filepath.Join(root, collectorconfig.ConfigDirName, collectorconfig.FileName),
			map[string]collectorconfig.Entry{"something-else": namedCustomDef("something-else", "https://p.example/mcp").entry}))
		deps := newCustomDeps(t)
		inRepo := session.ContextWithWorkspaceCwd(ctx, root)
		def, err := lookupCustomCollector(inRepo, deps, "t18-name-in-neither-registry")
		require.NoError(t, err, "a project file WAS read; the name is simply not in it")
		assert.Nil(t, def)
	})

	t.Run("a nil GraphTypeCRUD is the degraded client: whatever the refusal says, the catalog is not its cause", func(t *testing.T) {
		// The nil catalog must not change the ANSWER. With a project file read,
		// this name falls through exactly as it does on a wired client; the
		// degraded client is capability absence, never a refusal of its own.
		root := t.TempDir()
		require.NoError(t, collectorconfig.Write(
			filepath.Join(root, collectorconfig.ConfigDirName, collectorconfig.FileName),
			map[string]collectorconfig.Entry{"other": namedCustomDef("other", "https://p.example/mcp").entry}))
		deps := &customDeps{sink: &capturingSink{}}
		inRepo := session.ContextWithWorkspaceCwd(ctx, root)
		def, err := lookupCustomCollector(inRepo, deps, "t18-degraded-client")
		require.NoError(t, err)
		assert.Nil(t, def)
	})

	t.Run("a file entry RESOLVES on a degraded client, which is what makes the behavior upsert's nil arm load-bearing", func(t *testing.T) {
		// The entry is the registration record, so a client with no catalog wired
		// still dispatches it: the behavior half simply cannot be forwarded. An
		// implementation that ERRORED on the missing catalog would make a degraded
		// client unable to collect at all, and nothing observed that arm.
		scope := useTempCollectorScope(t, namedCustomDef("degraded-but-registered", "https://p.example/mcp"))
		deps := &customDeps{sink: &capturingSink{}, scope: scope}
		def, err := lookupCustomCollector(ctx, deps, "degraded-but-registered")
		require.NoError(t, err, "a nil catalog is capability absence, never a reason to refuse a registered family")
		require.NotNil(t, def)
		assert.Equal(t, "degraded-but-registered", def.Def.GetName())
	})

	t.Run("a ByName error is RETURNED, never reported as not-registered", func(t *testing.T) {
		deps := &customDeps{sink: &capturingSink{}, crud: &failingGraphTypeCRUD{}}
		def, err := lookupCustomCollector(ctx, deps, "t18-wire-failure")
		require.Error(t, err, "'not registered' and 'could not tell' are different answers with different consequences")
		assert.Contains(t, err.Error(), "cannot tell whether a custom collector is registered")
		assert.Nil(t, def)
	})

	t.Run("the empty string is not a graph type and is looked up like any other name", func(t *testing.T) {
		crud := &failingGraphTypeCRUD{}
		deps := &customDeps{sink: &capturingSink{}, crud: crud}
		_, err := lookupCustomCollector(ctx, deps, "")
		require.Error(t, err)
		assert.Equal(t, int32(1), crud.calls.Load())
	})
}

// TestCustomCollect_ShadowingGateIdentityIsTheRegistrationsAndTheWakeFires is
// R4 on the SHADOWING case: the gate family is the registration name and the
// gate instance is the collect id even though a collector of that name is
// compiled in, and the pipeline wake still fires.
func TestCustomCollect_ShadowingGateIdentityIsTheRegistrationsAndTheWakeFires(t *testing.T) {
	registerShadowStub(t)
	url, calls := countingCustomProvider(t)

	deps := newCustomDeps(t, namedCustomDef(shadowStubType, url))
	handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	require.Equal(t, int32(1), calls(), "the collect must have gone to the registration, or the identity below is the builtin's")

	assert.Equal(t, 1, deps.wakeCount(),
		"a shadowing custom collect wakes the LLM pipeline exactly as any other collect does")

	family, gateName, err := collectGateGraphIdentity(shadowStubType, "board", nil, true)
	require.NoError(t, err)
	assert.Equal(t, kgtypes.GraphType(shadowStubType), family, "the gate family is the REGISTRATION name")
	assert.Equal(t, "board", gateName, "the gate instance is the collect id")

	// THE CONTROL: the same type WITHOUT the registered-custom flag is the builtin
	// arm, which derives no name for a non-code collector.
	builtinName, err := CollectGateGraphName(shadowStubType, "board", nil, false)
	require.NoError(t, err)
	assert.Empty(t, builtinName, "the builtin arm's identity derivation is unchanged")
}

// TestCustomCollect_ShadowingGateIdentityIsRecordedByTheRealCollect closes the
// gap the sibling above leaves. That test re-derives the identity by calling
// collectGateGraphIdentity with the registered-custom flag SUPPLIED BY THE TEST,
// so it proves the derivation and not that the real collect used it: the value
// routeCollect computes is handed to collectWaitOrDetach and surfaces through no
// dep the test can read.
//
// THIS ONE READS WHAT THE RUNNING COLLECT RECORDED. The runtime stores the
// (family, instance) pair of every in-flight run and answers
// CollectInFlightForGraph from it — that is the production consumer of the gate
// identity — so holding a shadowing collect inside its provider and querying the
// runtime observes the value the real dispatch produced.
//
// THE SAME-RUN NEGATIVE is the whole point: the compiled-in collector of this
// name ships under GraphPractice, so a dispatch that recorded the BUILTIN identity
// would answer true for cloud and false for the registration name. Both are
// asserted.
func TestCustomCollect_ShadowingGateIdentityIsRecordedByTheRealCollect(t *testing.T) {
	registerShadowStub(t)
	url, started, release := blockingCustomProvider(t)

	deps := newCustomDeps(t, namedCustomDef(shadowStubType, url))
	deps.rt = NewCollectRuntime()
	deps.rt.detachAfter = 50 * time.Millisecond // detach fast so the call returns while the run holds
	t.Cleanup(func() { deps.rt.Stop(10 * time.Second) })
	t.Cleanup(release)

	handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	<-started // the run really is inside the provider, so it is genuinely in flight

	assert.True(t, deps.rt.CollectInFlightForGraph(kgtypes.GraphType(shadowStubType), "board"),
		"the runtime must have recorded the in-flight run under the REGISTRATION's family and the collect id — this is the identity the real dispatch computed, not one the test supplied")
	assert.False(t, deps.rt.CollectInFlightForGraph(kgtypes.GraphPractice, "board"),
		"and NOT under the compiled-in collector's family, which is what a dispatch that recorded the builtin identity would have written")
	assert.False(t, deps.rt.CollectInFlightForGraph(kgtypes.GraphType(shadowStubType), "some-other-id"),
		"the instance half is the collect id, so a different id is not this run")
}
