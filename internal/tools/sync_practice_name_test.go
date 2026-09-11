// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// sync_practice_name_test.go — how the sync arms answer a practice NAME.
//
// WHAT MOVED. The four tests that pinned a legacy practice name addressing one of
// the eight pre-singleton graph images went with that path: the family is one
// combined graph, the images are gone from the plane, and a name other than the
// combined graph's own is now REFUSED client-side before either seam. The two
// tests that survive pin the half that did not move — the combined graph still
// moves under an absent name and under "default", and the practice rule reaches
// no other singleton family.

// withFakeSyncTransport points the sync transport at a fake backend playing both
// the agent control plane and GCS.
func withFakeSyncTransport(t *testing.T, backend *fakeSyncBackend) {
	t.Helper()
	withTransport(t, func() (*auth.Transport, error) {
		src := auth.StaticTokenSource{AccessToken: "tok", Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}}}
		return auth.NewSyncTransport(backend.srv.URL, src), nil
	})
}

// TestSyncPush_PracticeWithNoName_AddressesTheSingleton is the same-run control
// for the refusal below: an ABSENT practice name, and the literal "default",
// still mean the combined graph, so the export target carries no instance field
// at all and the family's one live image move is unchanged by this work.
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

// TestSyncPull_PracticeWithNoName_AddressesTheSingleton is the pull half of the
// admitted column. Pull is the direction where an admitted name CREATES the local
// image it was told to write, so the admitted set is pinned on both directions
// rather than inferred from push.
func TestSyncPull_PracticeWithNoName_AddressesTheSingleton(t *testing.T) {
	for _, name := range []string{"", "default"} {
		t.Run("name="+name, func(t *testing.T) {
			want := []byte("KGV4 the cloud image of the combined practice graph")
			backend := newFakeSyncBackend(t)
			backend.pullPlaintext = want
			withFakeSyncTransport(t, backend)

			args := map[string]any{"operation": "pull", "graph": "practice"}
			if name != "" {
				args["name"] = name
			}
			local := &fakeOverwriter{nodes: 4783, edges: 15393}
			handled, out := InterceptSync(opCtx(), pullDeps{local: local}, syncParams(t, args))

			require.True(t, handled)
			require.False(t, out.IsError, "pull of the combined graph must succeed: %q", textOf(out))
			assert.Equal(t, "default", backend.lastPullName, "the account is asked for practice/default")
			require.Equal(t, 1, local.overwriteCalls)
			require.NotNil(t, local.lastReq)
			assert.Equal(t, "default", local.lastReq.GetName(),
				"and the apply lands in practices/default.bin")
			assert.Equal(t, want, local.lastReq.GetGraphBytes())
		})
	}
}

// TestSync_PracticeNameOtherThanDefault_IsRefusedClientSide is the refusal the
// combined graph makes total: `practice` is ONE graph addressed with no name, so
// every name but the combined graph's own is refused before either seam.
//
// TWO OBSERVABLES PER DIRECTION, not one. A test asserting only the message
// passes over the defect this refusal exists to close: without a refusal the name
// lands on no selector field at all and the arm falls through to the combined
// graph, which is what exported practices/default.bin for a push of practice/go.
// So each row also asserts that nothing was exported, offered, pulled or applied.
func TestSync_PracticeNameOtherThanDefault_IsRefusedClientSide(t *testing.T) {
	// A FORMER PRE-SINGLETON NAME, A DISPLAY SPELLING AND A VALUE THAT NEVER
	// NAMED ANYTHING. The first row is what makes the refusal about the FIELD
	// rather than the spelling: "go" was its own canonical slug and is refused
	// anyway.
	for _, name := range []string{"go", "Design Patterns", "not-a-graph"} {
		t.Run("push/"+name, func(t *testing.T) {
			backend := newFakeSyncBackend(t)
			withFakeSyncTransport(t, backend)
			exp := &fakeExporter{bytesOut: []byte("unreachable")}

			handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp},
				syncParams(t, map[string]any{"operation": "push", "graph": "practice", "name": name}))

			require.True(t, handled)
			require.True(t, out.IsError, "a practice name other than the combined graph's must be refused")
			body := textOf(out)
			assert.Contains(t, body, name, "the refusal names the offending spelling")
			assert.Contains(t, body, "ONE combined graph", "and says why")
			assert.Contains(t, body, "no name", "and names the address that works")
			assert.Equal(t, 0, exp.exportCalls, "refused before the local serialize")
			assert.Equal(t, 0, backend.presignCalls, "refused before any control call")
			assert.Empty(t, backend.lastPresignName, "and nothing was offered under any name")
		})

		t.Run("pull/"+name, func(t *testing.T) {
			backend := newFakeSyncBackend(t)
			backend.pullPlaintext = []byte("unreachable")
			withFakeSyncTransport(t, backend)
			local := &fakeOverwriter{}

			handled, out := InterceptSync(opCtx(), pullDeps{local: local},
				syncParams(t, map[string]any{"operation": "pull", "graph": "practice", "name": name}))

			require.True(t, handled)
			require.True(t, out.IsError, "a practice name other than the combined graph's must be refused")
			body := textOf(out)
			assert.Contains(t, body, name)
			assert.Contains(t, body, "ONE combined graph")
			assert.Equal(t, 0, backend.pullCalls, "refused before the control call")
			assert.Equal(t, 0, local.overwriteCalls, "and before any local apply could create the graph")
			assert.Nil(t, local.lastReq, "no apply was composed, under any name")
		})
	}
}

// TestSync_SingletonFamiliesKeepTheirDisposition is the PRACTICE-ONLY leg: checks
// and linkage carry no instance field on a sync either way, and neither family is
// fenced client-side by the practice rule — a name they cannot honor is the
// server's refusal to make, exactly as before.
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
				"the practice name fence must not reach %s: %q", graph, textOf(out))
			require.NotNil(t, exp.lastTarget)
			assert.Empty(t, exp.lastTarget.GetLanguage(),
				"no language selector is ever composed for %s", graph)
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
