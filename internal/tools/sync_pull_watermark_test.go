// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// sync_pull_watermark_test.go gates the client half of the unchanged-graph short-circuit:
// the stored token is transmitted, an unchanged answer skips the download / decrypt /
// apply entirely, a changed answer applies and then stores, and a FAILED apply leaves the
// stored token exactly where it was.

// withTestWatermarkStore swaps the process-wide store for one rooted at a temp path, so
// no test reads or writes the real ~/.knowledge/sync_watermarks.json.
func withTestWatermarkStore(t *testing.T) *syncWatermarkStore {
	t.Helper()
	prev := defaultSyncWatermarkStore
	s := &syncWatermarkStore{path: filepath.Join(t.TempDir(), "sync_watermarks.json")}
	defaultSyncWatermarkStore = s
	t.Cleanup(func() { defaultSyncWatermarkStore = prev })
	return s
}

// wmPullBackend stands up the fake sync backend + transport used by every test here.
func wmPullBackend(t *testing.T) *fakeSyncBackend {
	t.Helper()
	backend := newFakeSyncBackend(t)
	withTransport(t, func() (*auth.Transport, error) {
		src := auth.StaticTokenSource{
			AccessToken: "tok",
			Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}},
		}
		return auth.NewSyncTransport(backend.srv.URL, src), nil
	})
	return backend
}

// TestPullWmUnchangedSkipsApply: an unchanged answer performs NO GCS download and NO
// apply, and reports itself as unchanged in words an operator cannot mistake for an
// applied pull.
func TestPullWmUnchangedSkipsApply(t *testing.T) {
	store := withTestWatermarkStore(t)
	require.NoError(t, store.Save("knowledge", "default", "cg1:7:2:1:0"))

	backend := wmPullBackend(t)
	backend.pullUnchanged = true
	backend.pullWatermark = "cg1:7:2:1:0"
	local := &fakeOverwriter{nodes: 7, edges: 3}

	handled, out := InterceptSync(opCtx(), pullDeps{local: local},
		syncParams(t, map[string]any{"operation": "pull"}))

	require.True(t, handled)
	assert.False(t, out.IsError, "an unchanged pull is a success: %q", textOf(out))
	assert.Equal(t, 1, backend.pullCalls, "the control call still happens — it is what answers the question")

	// The whole point: steps 2-4 do not run. A version that downloaded and then
	// discarded would save nothing.
	assert.Equal(t, 0, local.overwriteCalls, "an unchanged pull must apply NOTHING")
	backend.mu.Lock()
	gets := backend.gcsGets
	backend.mu.Unlock()
	assert.Equal(t, 0, gets, "an unchanged pull must not download the object")

	// The locked operator-facing discriminator.
	assert.Contains(t, textOf(out), "unchanged", "the result must say so")
	assert.NotContains(t, textOf(out), "bytes;",
		"an unchanged pull must NOT render like an applied one — a report that reads as "+
			"success for work that did not happen is the operator-facing twin of silent staleness")
}

// TestPullWmChangedAppliesThenSaves is the positive half, and it is what stops the
// unchanged assertions above being satisfied by a client that never reports bytes at all.
func TestPullWmChangedAppliesThenSaves(t *testing.T) {
	store := withTestWatermarkStore(t)
	require.NoError(t, store.Save("knowledge", "default", "cg1:7:2:1:0"))

	backend := wmPullBackend(t)
	backend.pullPlaintext = []byte("KGV4 the authoritative cloud graph image")
	backend.pullWatermark = "cg1:9:5:4:3"
	local := &fakeOverwriter{nodes: 7, edges: 3}

	handled, out := InterceptSync(opCtx(), pullDeps{local: local},
		syncParams(t, map[string]any{"operation": "pull"}))

	require.True(t, handled)
	assert.False(t, out.IsError, "pull success must not be an error result: %q", textOf(out))
	require.Equal(t, 1, local.overwriteCalls, "a changed pull must apply once")
	assert.Contains(t, textOf(out), "bytes;", "the applied path DOES report bytes")

	assert.Equal(t, "cg1:9:5:4:3", store.Load("knowledge", "default"),
		"the NEW token must be stored after a successful apply")
}

// TestPullWmApplyFailureKeepsToken is the NAMED CATCHER for the defer-wrapped /
// advance-before-persist anti-pattern. A token stored for bytes that were never applied
// makes every later pull answer "unchanged" for a graph this machine does not hold —
// silent, permanent staleness.
func TestPullWmApplyFailureKeepsToken(t *testing.T) {
	store := withTestWatermarkStore(t)
	require.NoError(t, store.Save("knowledge", "default", "cg1:7:2:1:0"))

	backend := wmPullBackend(t)
	backend.pullPlaintext = []byte("KGV4 bytes that will fail to apply")
	backend.pullWatermark = "cg1:9:5:4:3"
	local := &fakeOverwriter{overwriteErr: errors.New("injected: apply failed")}

	handled, out := InterceptSync(opCtx(), pullDeps{local: local},
		syncParams(t, map[string]any{"operation": "pull"}))

	require.True(t, handled)
	assert.True(t, out.IsError, "a failed apply must surface as an error")
	require.Equal(t, 1, local.overwriteCalls, "precondition: the apply was actually attempted")

	assert.Equal(t, "cg1:7:2:1:0", store.Load("knowledge", "default"),
		"a FAILED apply must leave the stored watermark exactly as it was — saving the new "+
			"token here would make this machine claim bytes it never applied")
}

// TestPullWmEmptyTokenNotStored: an empty watermark is the server's "cannot answer"
// signal. It must not be stored; the prior key is removed so the next pull sends nothing
// and receives a full export.
func TestPullWmEmptyTokenNotStored(t *testing.T) {
	store := withTestWatermarkStore(t)
	require.NoError(t, store.Save("knowledge", "default", "cg1:7:2:1:0"))

	backend := wmPullBackend(t)
	backend.pullPlaintext = []byte("KGV4 image from a server that cannot answer")
	backend.pullWatermark = "" // the cannot-answer signal
	local := &fakeOverwriter{nodes: 1, edges: 0}

	handled, out := InterceptSync(opCtx(), pullDeps{local: local},
		syncParams(t, map[string]any{"operation": "pull"}))

	require.True(t, handled)
	assert.False(t, out.IsError, "a full export with no token is an ordinary success: %q", textOf(out))
	require.Equal(t, 1, local.overwriteCalls, "the bytes are still applied")

	assert.Empty(t, store.Load("knowledge", "default"),
		"an empty token must clear the stored one, never be stored as a value")
	entries, err := readLocalJSONMap(store.path)
	require.NoError(t, err)
	_, present := entries[syncWatermarkKey("knowledge", "default")]
	assert.False(t, present, "the key must be gone from the file, not present and empty")
}

// TestPullWmSendsStoredToken is the NAMED CATCHER for a Load that is wired but whose
// value never reaches the wire — a defect under which every pull silently degrades to a
// full export and nothing else in this suite fails.
func TestPullWmSendsStoredToken(t *testing.T) {
	store := withTestWatermarkStore(t)
	require.NoError(t, store.Save("knowledge", "default", "cg1:42:1:2:3"))

	backend := wmPullBackend(t)
	backend.pullPlaintext = []byte("KGV4 image")
	backend.pullWatermark = "cg1:43:1:2:3"
	local := &fakeOverwriter{nodes: 1, edges: 1}

	handled, _ := InterceptSync(opCtx(), pullDeps{local: local},
		syncParams(t, map[string]any{"operation": "pull"}))
	require.True(t, handled)

	backend.mu.Lock()
	sent := backend.lastPullWatermark
	backend.mu.Unlock()
	assert.Equal(t, "cg1:42:1:2:3", sent,
		"the STORED token must actually be transmitted — a Load whose value never reaches "+
			"the request degrades every pull to a full export with nothing else failing")
}

// TestPullWmNoStoredTokenSendsEmpty is the known-positive counterpart to the test above:
// with nothing stored the client sends an empty watermark, which is what makes a
// first-ever pull a full export. Without it, the assertion above is equally satisfied by
// a client that hardcodes a token.
func TestPullWmNoStoredTokenSendsEmpty(t *testing.T) {
	withTestWatermarkStore(t) // empty store — nothing saved

	backend := wmPullBackend(t)
	backend.pullPlaintext = []byte("KGV4 image")
	backend.pullWatermark = "cg1:1:1:1:1"
	local := &fakeOverwriter{nodes: 1, edges: 1}

	handled, _ := InterceptSync(opCtx(), pullDeps{local: local},
		syncParams(t, map[string]any{"operation": "pull"}))
	require.True(t, handled)

	backend.mu.Lock()
	sent := backend.lastPullWatermark
	backend.mu.Unlock()
	assert.Empty(t, sent, "with nothing stored the client must send no watermark")
}
