// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestManifest returns a repoManifest rooted at a fresh temp path so the
// real ~/.knowledge/repos.json is never touched by unit tests. The file is NOT
// pre-created — the missing-file-tolerant paths are part of what's under test.
func newTestManifest(t *testing.T) *repoManifest {
	t.Helper()
	return &repoManifest{path: filepath.Join(t.TempDir(), "repos.json")}
}

func TestRepoManifest_LookupMissingFile(t *testing.T) {
	m := newTestManifest(t)
	// No file on disk: Lookup degrades to ok=false with no error/panic.
	got, ok := m.Lookup("knowledge")
	assert.False(t, ok)
	assert.Empty(t, got)
}

func TestRepoManifest_RecordThenLookup(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Record("knowledge", "/Users/me/code/knowledge"))

	got, ok := m.Lookup("knowledge")
	require.True(t, ok)
	assert.Equal(t, "/Users/me/code/knowledge", got)

	// File was actually created on disk and holds valid JSON.
	data, err := os.ReadFile(m.path)
	require.NoError(t, err)
	var entries map[string]string
	require.NoError(t, json.Unmarshal(data, &entries))
	assert.Equal(t, map[string]string{"knowledge": "/Users/me/code/knowledge"}, entries)
}

func TestRepoManifest_RecordCreatesParentDir(t *testing.T) {
	// The enclosing dir does not exist yet — Record must MkdirAll it (mirrors
	// the first-ever collect into a fresh ~/.knowledge).
	nested := filepath.Join(t.TempDir(), "does", "not", "exist", "repos.json")
	m := &repoManifest{path: nested}
	require.NoError(t, m.Record("agent", "/Users/me/code/agent"))
	got, ok := m.Lookup("agent")
	require.True(t, ok)
	assert.Equal(t, "/Users/me/code/agent", got)
}

func TestRepoManifest_RecordUpsertPreservesOthers(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Record("knowledge", "/a/knowledge"))
	require.NoError(t, m.Record("agent", "/a/agent"))
	// Overwrite one; the other must survive.
	require.NoError(t, m.Record("knowledge", "/b/knowledge"))

	got, ok := m.Lookup("knowledge")
	require.True(t, ok)
	assert.Equal(t, "/b/knowledge", got)
	gotAgent, ok := m.Lookup("agent")
	require.True(t, ok)
	assert.Equal(t, "/a/agent", gotAgent)
}

func TestRepoManifest_RecordRejectsEmpty(t *testing.T) {
	m := newTestManifest(t)
	require.Error(t, m.Record("", "/a/x"))
	require.Error(t, m.Record("x", ""))
}

func TestRepoManifest_LookupEmptyName(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Record("knowledge", "/a/knowledge"))
	got, ok := m.Lookup("")
	assert.False(t, ok)
	assert.Empty(t, got)
}

func TestRepoManifest_CorruptFileDegrades(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(m.path), 0o750))
	require.NoError(t, os.WriteFile(m.path, []byte("{ this is not json"), 0o600))

	// Lookup over a corrupt manifest degrades to not-found, never errors.
	got, ok := m.Lookup("knowledge")
	assert.False(t, ok)
	assert.Empty(t, got)

	// Record self-heals: it replaces the corrupt file with a valid one
	// containing the new mapping.
	require.NoError(t, m.Record("knowledge", "/a/knowledge"))
	healed, ok := m.Lookup("knowledge")
	require.True(t, ok)
	assert.Equal(t, "/a/knowledge", healed)
}

func TestRepoManifest_NilInstance(t *testing.T) {
	// A nil manifest (home dir unresolvable at init) must not panic: Lookup
	// degrades to not-found, Record returns an error the best-effort caller
	// swallows.
	var m *repoManifest
	got, ok := m.Lookup("knowledge")
	assert.False(t, ok)
	assert.Empty(t, got)
	require.Error(t, m.Record("knowledge", "/a/knowledge"))
}

func TestRepoManifest_ConcurrentRecord(t *testing.T) {
	// The mutex must serialize concurrent writers so no rewrite is lost or
	// observed half-written. Run under -race to catch a regression.
	m := newTestManifest(t)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := []string{"knowledge", "agent", "infra", "web"}[n%4]
			// Don't use require.* off the test goroutine (testifylint go-require):
			// funnel errors back and assert on the test goroutine after Wait.
			errs <- m.Record(name, filepath.Join("/code", name))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	for _, name := range []string{"knowledge", "agent", "infra", "web"} {
		got, ok := m.Lookup(name)
		require.True(t, ok, "name %q must be recorded", name)
		assert.Equal(t, filepath.Join("/code", name), got)
	}
}
