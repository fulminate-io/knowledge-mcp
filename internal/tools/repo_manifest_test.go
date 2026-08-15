// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

// seedManifest writes a pre-existing manifest straight to disk, the same way
// TestRepoManifest_CorruptFileDegrades seeds its fixture. Deliberately NOT via
// writeAtomicLocked: that method's contract is "callers hold m.mu", and calling
// it unlocked from a test misuses the locking contract for no benefit.
func seedManifest(t *testing.T, m *repoManifest, entries map[string]string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(m.path), 0o750))
	data, err := json.MarshalIndent(entries, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(m.path, data, 0o600))
}

// readManifestKeys decodes the manifest on disk and returns its key set, so the
// prune assertions can compare the WHOLE set rather than probing name by name.
func readManifestKeys(t *testing.T, m *repoManifest) []string {
	t.Helper()
	data, err := os.ReadFile(m.path)
	require.NoError(t, err)
	entries := map[string]string{}
	require.NoError(t, json.Unmarshal(data, &entries))
	keys := make([]string, 0, len(entries))
	for name := range entries {
		keys = append(keys, name)
	}
	return keys
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
	root := t.TempDir()
	// Real directories, so the surviving sibling is a genuine known-positive
	// control: a LIVE entry outliving a rewrite that prunes vanished ones.
	aKnowledge := filepath.Join(root, "a", "knowledge")
	aAgent := filepath.Join(root, "a", "agent")
	bKnowledge := filepath.Join(root, "b", "knowledge")
	for _, dir := range []string{aKnowledge, aAgent, bKnowledge} {
		require.NoError(t, os.MkdirAll(dir, 0o750))
	}

	require.NoError(t, m.Record("knowledge", aKnowledge))
	require.NoError(t, m.Record("agent", aAgent))
	// Overwrite one; the other must survive.
	require.NoError(t, m.Record("knowledge", bKnowledge))

	got, ok := m.Lookup("knowledge")
	require.True(t, ok)
	assert.Equal(t, bKnowledge, got)
	gotAgent, ok := m.Lookup("agent")
	require.True(t, ok)
	assert.Equal(t, aAgent, gotAgent)
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
	// Real directories: a rewrite prunes entries whose recorded directory has
	// vanished, so invented paths would be dropped by the writers themselves.
	root := t.TempDir()
	names := []string{"knowledge", "agent", "infra", "web"}
	for _, name := range names {
		require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0o750))
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := names[n%4]
			// Don't use require.* off the test goroutine (testifylint go-require):
			// funnel errors back and assert on the test goroutine after Wait.
			errs <- m.Record(name, filepath.Join(root, name))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	for _, name := range names {
		got, ok := m.Lookup(name)
		require.True(t, ok, "name %q must be recorded", name)
		assert.Equal(t, filepath.Join(root, name), got)
	}
}

// TestRepoManifest_RecordPrunesVanished seeds a manifest holding dead AND live
// entries and pins that a write drops exactly the dead ones. The assertion is
// full SET EQUALITY on the decoded file: an over-prune that dropped a live entry
// and an under-prune that kept a dead one each break it, whereas three separate
// per-name checks on the dead names would also pass against an implementation
// that deleted everything. The live entries are the known-positive control.
func TestRepoManifest_RecordPrunesVanished(t *testing.T) {
	m := newTestManifest(t)
	root := t.TempDir()
	live := func(name string) string {
		dir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(dir, 0o750))
		return dir
	}
	gone := filepath.Join(root, "never-created")
	seedManifest(t, m, map[string]string{
		"live1": live("live1"),
		"live2": live("live2"),
		"dead1": filepath.Join(gone, "dead1"),
		"dead2": filepath.Join(gone, "dead2"),
		"dead3": filepath.Join(gone, "dead3"),
	})

	require.NoError(t, m.Record("live3", live("live3")))

	keys := readManifestKeys(t, m)
	assert.ElementsMatch(t, []string{"live1", "live2", "live3"}, keys)
	// Cardinality against the FIXTURE-DERIVED constant — three live directories
	// are created above — never against the length of a set read the same way.
	assert.Len(t, keys, 3)
}

// TestRepoManifest_RecordPrunesWhenCurrent is the inert-mechanism gate. Record
// short-circuits when the mapping it was handed is already the one on disk, so a
// prune written AFTER that early return never executes in the dominant real
// case: the repo being collected is normally already recorded at its current
// path, so every collect would return early and every dead sibling would survive
// forever. Do not "simplify" this test away — it is what pins the ordering.
func TestRepoManifest_RecordPrunesWhenCurrent(t *testing.T) {
	m := newTestManifest(t)
	root := t.TempDir()
	liveDir := filepath.Join(root, "knowledge")
	require.NoError(t, os.MkdirAll(liveDir, 0o750))
	seedManifest(t, m, map[string]string{
		"knowledge": liveDir,
		"dead1":     filepath.Join(root, "never-created"),
	})

	// The EXACT mapping already on disk — the currency short-circuit's own case.
	require.NoError(t, m.Record("knowledge", liveDir))

	assert.ElementsMatch(t, []string{"knowledge"}, readManifestKeys(t, m))
}

// TestRepoManifest_RecordKeepsOnStatError proves the predicate is
// fs.ErrNotExist-only rather than any-stat-error. An entry under an unreadable
// parent (permission denied) and an entry whose path exists but is a FILE both
// SURVIVE; only the definitively absent one is dropped. That dropped entry is
// the known-positive control for the two survivals — without it, a prune that
// never ran at all would satisfy everything else here.
func TestRepoManifest_RecordKeepsOnStatError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission bits, so the non-ErrNotExist stat error cannot be produced")
	}
	m := newTestManifest(t)
	root := t.TempDir()

	locked := filepath.Join(root, "locked")
	unreadable := filepath.Join(locked, "repo")
	require.NoError(t, os.MkdirAll(unreadable, 0o750))
	require.NoError(t, os.Chmod(locked, 0o000))
	// Restore permissions on cleanup so t.TempDir's RemoveAll can fire.
	t.Cleanup(func() { _ = os.Chmod(locked, 0o750) }) //nolint:gosec // restoring traverse+write so t.TempDir cleanup can RemoveAll

	// CONTROL FIRST: the filesystem must actually produce a non-ErrNotExist stat
	// error, or the survival assertions below are satisfiable on a filesystem
	// that never produced the error shape at all.
	if _, err := os.Stat(unreadable); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Skipf("filesystem did not produce a permission stat error: %v", err)
	}

	filey := filepath.Join(root, "not-a-dir")
	require.NoError(t, os.WriteFile(filey, []byte("x"), 0o600))
	liveDir := filepath.Join(root, "live")
	require.NoError(t, os.MkdirAll(liveDir, 0o750))

	seedManifest(t, m, map[string]string{
		"unreadable": unreadable,
		"filey":      filey,
		"dead":       filepath.Join(root, "never-created"),
	})

	require.NoError(t, m.Record("live", liveDir))
	assert.ElementsMatch(t, []string{"unreadable", "filey", "live"}, readManifestKeys(t, m))

	// The recorded name is EXEMPT: Record's contract is to make the mapping it
	// was handed true, whether or not that directory exists yet.
	require.NoError(t, m.Record("ghost", filepath.Join(root, "no-such-dir")))
	assert.Contains(t, readManifestKeys(t, m), "ghost")
}

// TestRepoManifest_LookupNeverWrites is a CHARACTERIZATION GUARD: green before
// and after the prune lands, never a reproduction. It fences the constraint that
// a read-only path must not write, against a future change that moves the prune
// into Lookup — over this all-dead manifest such a change would rewrite the file
// and this goes red.
func TestRepoManifest_LookupNeverWrites(t *testing.T) {
	m := newTestManifest(t)
	root := t.TempDir()
	seedManifest(t, m, map[string]string{
		"dead1": filepath.Join(root, "gone1"),
		"dead2": filepath.Join(root, "gone2"),
	})

	before, err := os.ReadFile(m.path)
	require.NoError(t, err)
	statBefore, err := os.Stat(m.path)
	require.NoError(t, err)

	// Past filesystem mtime granularity, so a rewrite would be observable.
	time.Sleep(15 * time.Millisecond)

	for range 3 {
		_, _ = m.Lookup("dead1")
		_, _ = m.Lookup("a-name-the-manifest-does-not-carry")
	}

	after, err := os.ReadFile(m.path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a read path must not rewrite the manifest")
	statAfter, err := os.Stat(m.path)
	require.NoError(t, err)
	assert.True(t, statAfter.ModTime().Equal(statBefore.ModTime()),
		"manifest mtime changed across Lookups: something on the read path wrote")
}
