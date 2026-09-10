// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// legacyPracticeGraphNames are the eight pre-singleton practice graphs this
// machine holds, as `query({graph:"practice"})` enumerates them. They are the
// names a sync of a legacy practice graph has to carry through unchanged: the
// combined graph is "default" and these eight are the corpus the migration still
// has to move, so a client that rewrote any of them to "default" would push one
// graph eight times and pull the wrong bytes over seven of them.
var legacyPracticeGraphNames = []string{
	"cloud-design-patterns",
	"design-patterns",
	"enterprise-patterns",
	"go",
	"go-cox-buday-v4",
	"go-idioms",
	"knowledge-architecture",
	"postgres-best-practices",
}

// withFakeSyncTransport points the sync transport at a fake backend playing both
// the agent control plane and GCS.
func withFakeSyncTransport(t *testing.T, backend *fakeSyncBackend) {
	t.Helper()
	withTransport(t, func() (*auth.Transport, error) {
		src := auth.StaticTokenSource{AccessToken: "tok", Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}}}
		return auth.NewSyncTransport(backend.srv.URL, src), nil
	})
}

// TestSyncPush_PracticeLegacyName_AddressesTheLegacyGraph is entry (a): a push of
// practice/go must serialize the LOCAL practices/go.bin and offer it to the cloud
// under practice/go.
//
// The local half is read off the ExportGraph target's LANGUAGE field, which is
// not a spelling choice: the server's practice selector policy consumes
// `language` and REFUSES a set `name`, and resolvePractice opens the graph the
// language names, falling back to the combined "default" when it is empty. A
// target carrying no language is therefore a request for the combined graph, and
// that is exactly what the dev confirmation observed as
// `not_found: practice graph "default" not found`.
func TestSyncPush_PracticeLegacyName_AddressesTheLegacyGraph(t *testing.T) {
	backend := newFakeSyncBackend(t)
	withFakeSyncTransport(t, backend)

	exp := &fakeExporter{bytesOut: []byte("KGV4 practices/go.bin")}
	handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp},
		syncParams(t, map[string]any{"operation": "push", "graph": "practice", "name": "go"}))

	require.True(t, handled)
	require.False(t, out.IsError, "push of a legacy practice graph must succeed: %q", textOf(out))
	require.Equal(t, 1, exp.exportCalls)
	require.NotNil(t, exp.lastTarget)
	assert.Equal(t, "practice", exp.lastTarget.GetGraph())
	assert.Equal(t, "go", exp.lastTarget.GetLanguage(),
		"the legacy name rides the language field — the one instance field the practice selector policy consumes")
	assert.Empty(t, exp.lastTarget.GetName(),
		"a set name on a practice selector is refused by validateGraphSelector")

	assert.Equal(t, "practice", backend.lastPresignGraphType)
	assert.Equal(t, "go", backend.lastPresignName, "the cloud object is offered under practice/go")
	assert.Equal(t, "practice", backend.lastConfirmGraphType)
	assert.Equal(t, "go", backend.lastConfirmName, "confirm ingests it as practice/go")
	assert.Contains(t, textOf(out), "pushed practice/go")
}

// TestSyncPull_PracticeLegacyName_AppliesToTheLegacyGraph is entry (b): a pull of
// practice/go asks the account for practice/go and applies the bytes to the LOCAL
// practices/go.bin. OverwriteGraph takes a flat (graph_type, name), which
// store.OverwriteFromBytes uses verbatim, so the name the arm sends IS the .bin
// it replaces.
func TestSyncPull_PracticeLegacyName_AppliesToTheLegacyGraph(t *testing.T) {
	want := []byte("KGV4 the cloud image of practice/go")
	backend := newFakeSyncBackend(t)
	backend.pullPlaintext = want
	withFakeSyncTransport(t, backend)

	local := &fakeOverwriter{nodes: 173, edges: 206}
	handled, out := InterceptSync(opCtx(), pullDeps{local: local},
		syncParams(t, map[string]any{"operation": "pull", "graph": "practice", "name": "go"}))

	require.True(t, handled)
	require.False(t, out.IsError, "pull of a legacy practice graph must succeed: %q", textOf(out))
	assert.Equal(t, "practice", backend.lastPullGraphType)
	assert.Equal(t, "go", backend.lastPullName, "the account is asked for practice/go")
	require.Equal(t, 1, local.overwriteCalls)
	require.NotNil(t, local.lastReq)
	assert.Equal(t, "practice", local.lastReq.GetGraphType())
	assert.Equal(t, "go", local.lastReq.GetName(), "the apply lands in practices/go.bin, never practices/default.bin")
	assert.Equal(t, want, local.lastReq.GetGraphBytes())
	assert.Contains(t, textOf(out), "pulled practice/go")
}

// TestSyncPush_PracticeWithNoName_AddressesTheSingleton is entry (c), the
// same-run control for the two above: an ABSENT practice name still means the
// combined graph, so the export target carries no instance field at all and the
// singleton disposition is unchanged by this work.
func TestSyncPush_PracticeWithNoName_AddressesTheSingleton(t *testing.T) {
	for _, name := range []string{"", "default"} {
		t.Run("name="+name, func(t *testing.T) {
			backend := newFakeSyncBackend(t)
			withFakeSyncTransport(t, backend)

			args := map[string]any{"operation": "push", "graph": "practice"}
			if name != "" {
				args["name"] = name
			}
			exp := &fakeExporter{bytesOut: []byte("KGV4 practices/default.bin")}
			handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp}, syncParams(t, args))

			require.True(t, handled)
			require.False(t, out.IsError, "push of the combined graph must succeed: %q", textOf(out))
			require.NotNil(t, exp.lastTarget)
			assert.Empty(t, exp.lastTarget.GetLanguage(),
				"an absent or default name is the combined graph — no legacy selector is sent")
			assert.Empty(t, exp.lastTarget.GetName())
			assert.Equal(t, "default", backend.lastPresignName)
			assert.Contains(t, textOf(out), "pushed practice/default")
		})
	}
}

// TestSync_PracticeNonCanonicalName_IsRefusedClientSide is entry (d): a display
// spelling is REFUSED naming the canonical slug, never quietly slugified. Both
// directions are fenced, and pull is the direction that needs it most: its apply
// creates the named .bin, so a coerced or admitted "Design Patterns" would leave
// a local graph no read of the family can open.
//
// The refusal lands BEFORE any RPC or control call, which the call counters
// assert: a name this server will never accept should not cost a whole-graph
// serialize or an upload first.
func TestSync_PracticeNonCanonicalName_IsRefusedClientSide(t *testing.T) {
	const hostile = "Design Patterns"

	t.Run("push", func(t *testing.T) {
		backend := newFakeSyncBackend(t)
		withFakeSyncTransport(t, backend)
		exp := &fakeExporter{bytesOut: []byte("unreachable")}

		handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp},
			syncParams(t, map[string]any{"operation": "push", "graph": "practice", "name": hostile}))

		require.True(t, handled)
		require.True(t, out.IsError, "a non-canonical practice name must be refused")
		assert.Contains(t, textOf(out), hostile, "the refusal names the offending spelling")
		assert.Contains(t, textOf(out), "design-patterns", "and the canonical spelling that would have worked")
		assert.Equal(t, 0, exp.exportCalls, "refused before the local serialize")
		assert.Equal(t, 0, backend.presignCalls, "refused before any control call")
	})

	t.Run("pull", func(t *testing.T) {
		backend := newFakeSyncBackend(t)
		backend.pullPlaintext = []byte("unreachable")
		withFakeSyncTransport(t, backend)
		local := &fakeOverwriter{}

		handled, out := InterceptSync(opCtx(), pullDeps{local: local},
			syncParams(t, map[string]any{"operation": "pull", "graph": "practice", "name": hostile}))

		require.True(t, handled)
		require.True(t, out.IsError, "a non-canonical practice name must be refused")
		assert.Contains(t, textOf(out), hostile)
		assert.Contains(t, textOf(out), "design-patterns")
		assert.Equal(t, 0, backend.pullCalls, "refused before the control call")
		assert.Equal(t, 0, local.overwriteCalls, "and before any local apply could create the graph")
	})
}

// TestSync_SingletonFamiliesKeepTheirDisposition is entry (e): the legacy arm is
// PRACTICE-ONLY. checks carries no instance field on a sync either way, and
// neither family is fenced client-side by the practice canonical rule — a name
// they cannot honor is the server's refusal to make, exactly as before.
func TestSync_SingletonFamiliesKeepTheirDisposition(t *testing.T) {
	for _, graph := range []string{"checks", "linkage"} {
		t.Run(graph, func(t *testing.T) {
			backend := newFakeSyncBackend(t)
			withFakeSyncTransport(t, backend)

			exp := &fakeExporter{bytesOut: []byte("KGV4 singleton")}
			handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp},
				syncParams(t, map[string]any{"operation": "push", "graph": graph, "name": "Design Patterns"}))

			require.True(t, handled)
			assert.False(t, out.IsError,
				"the practice canonical fence must not reach %s: %q", graph, textOf(out))
			require.NotNil(t, exp.lastTarget)
			assert.Empty(t, exp.lastTarget.GetLanguage(),
				"no legacy language selector is ever composed for %s", graph)
		})
	}

	t.Run("checks carries no instance field", func(t *testing.T) {
		backend := newFakeSyncBackend(t)
		withFakeSyncTransport(t, backend)
		exp := &fakeExporter{bytesOut: []byte("KGV4 checks")}
		handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp},
			syncParams(t, map[string]any{"operation": "push", "graph": "checks", "name": "go"}))
		require.True(t, handled)
		require.False(t, out.IsError, "%q", textOf(out))
		require.NotNil(t, exp.lastTarget)
		assert.Empty(t, exp.lastTarget.GetName(), "checks is a singleton: it consumes no instance field")
		assert.Empty(t, exp.lastTarget.GetLanguage())
	})
}

// TestSync_EightLegacyPracticeNamesRoundTripAsThemselves is entry (f): every one
// of the eight pre-singleton graphs addresses ITSELF on both directions. It is
// the entry that catches a rule which happens to work for one name — a fold to
// "default", or a slug transform applied to an already-canonical name — because
// every one of the eight is its own canonical spelling and none of them is
// "default".
func TestSync_EightLegacyPracticeNamesRoundTripAsThemselves(t *testing.T) {
	for _, name := range legacyPracticeGraphNames {
		t.Run(name, func(t *testing.T) {
			backend := newFakeSyncBackend(t)
			backend.pullPlaintext = []byte("KGV4 " + name)
			withFakeSyncTransport(t, backend)

			exp := &fakeExporter{bytesOut: []byte("KGV4 " + name)}
			handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp},
				syncParams(t, map[string]any{"operation": "push", "graph": "practice", "name": name}))
			require.True(t, handled)
			require.False(t, out.IsError, "push %s: %q", name, textOf(out))
			require.NotNil(t, exp.lastTarget)
			assert.Equal(t, name, exp.lastTarget.GetLanguage(), "push exports practices/%s.bin", name)
			assert.Equal(t, name, backend.lastPresignName, "push offers it as practice/%s", name)

			local := &fakeOverwriter{}
			handled, out = InterceptSync(opCtx(), pullDeps{local: local},
				syncParams(t, map[string]any{"operation": "pull", "graph": "practice", "name": name}))
			require.True(t, handled)
			require.False(t, out.IsError, "pull %s: %q", name, textOf(out))
			assert.Equal(t, name, backend.lastPullName, "pull asks the account for practice/%s", name)
			require.NotNil(t, local.lastReq)
			assert.Equal(t, name, local.lastReq.GetName(), "pull applies to practices/%s.bin", name)
		})
	}
}
