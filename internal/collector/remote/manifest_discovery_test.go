// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// manifest_discovery_test.go — the discovery store's four states, which have
// OPPOSITE dispositions, plus the propagation property that ties a store failure
// to the collect.
//
// ONE TOP-LEVEL TEST PER STATE, deliberately. Absence and failure used to return
// the same boolean; folding them back into one test would let a passing state
// mask a failing one, which is exactly the collapse this file exists to prevent.
//
// EVERY TEST BUILDS ITS OWN STORE UNDER t.TempDir(). Nothing here may touch the
// real ~/.knowledge/collect-discovery.json — defaultDiscoveryStore is a package
// var, and a test writing through it corrupts the developer's own collect state.

// tempDiscoveryStore returns a store rooted in a fresh temp dir, with the file
// itself absent.
func tempDiscoveryStore(t *testing.T) *discoveryStore {
	t.Helper()
	return &discoveryStore{path: filepath.Join(t.TempDir(), "collect-discovery.json")}
}

// TestDiscoveryStore_MissingFileIsNotAnError is state (b): NO PRIOR RECORD. It is
// the bootstrap path every machine takes exactly once, so it must stay a
// legitimate first collect. An implementation that makes "the store returned
// nothing" an error without separating absence from failure aborts the first
// collect everywhere, and this is the test that catches it.
func TestDiscoveryStore_MissingFileIsNotAnError(t *testing.T) {
	d := tempDiscoveryStore(t)
	_, err := os.Stat(d.path)
	require.ErrorIs(t, err, os.ErrNotExist, "control: the fixture store must genuinely not exist")

	changed, err := d.changed("code/repo@main", "sig-1")
	require.NoError(t, err, "a first collect is ABSENCE, never failure")
	require.True(t, changed, "with no baseline the collect takes the rebuild lane")

	// THE COMPARE RECORDS NOTHING. Asking twice without a record in between must
	// still answer CHANGED — this is the leg that fails against a changed() which
	// kept the old write, and it is what makes the commit point observable at all.
	stillChanged, err := d.changed("code/repo@main", "sig-1")
	require.NoError(t, err)
	require.True(t, stillChanged, "changed() must not advance the baseline it is comparing against")

	// KNOWN POSITIVE for the write half: once record() commits, the same question
	// answers UNCHANGED — so the no-error answers above come from a working store
	// rather than from one that silently wrote nothing.
	require.NoError(t, d.record(baselineCommit{key: "code/repo@main", sig: "sig-1"}))
	again, err := d.changed("code/repo@main", "sig-1")
	require.NoError(t, err)
	require.False(t, again, "the second collect of an unchanged configuration sees its own baseline")
}

// TestDiscoveryStore_CorruptFileAborts is state (c), malformed. Reading it as
// empty looked like tolerance and was a permanent silent degrade: the file never
// heals, so every collect on that machine paid a full upload.
func TestDiscoveryStore_CorruptFileAborts(t *testing.T) {
	d := tempDiscoveryStore(t)
	require.NoError(t, os.WriteFile(d.path, []byte("{not json"), 0o600))

	changed, err := d.changed("code/repo@main", "sig-1")
	require.Error(t, err, "a lost record is a FAILURE, not an absence")
	require.Contains(t, err.Error(), d.path, "the error must name the store it could not parse")
	require.False(t, changed, "the error path returns no meaningful answer")

	// BOTH HALVES CARRY IT. record() re-reads before merging, so a corrupt store
	// must abort the commit too rather than overwrite the file with a single key
	// and silently discard every other graph's baseline.
	require.Error(t, d.record(baselineCommit{key: "code/repo@main", sig: "sig-1"}),
		"the commit half must refuse a store it cannot parse")
}

// TestDiscoveryStore_UnwritableStoreAborts is state (c), unwritable.
//
// THE FIXTURE IS A PARENT THAT IS A REGULAR FILE, NOT chmod. A chmod-000
// directory is still writable BY ROOT and CI may run as root, so that fixture
// would pass for the wrong reason on exactly the machine that matters. MkdirAll
// over a regular file fails for everyone, root included.
func TestDiscoveryStore_UnwritableStoreAborts(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o600))
	d := &discoveryStore{path: filepath.Join(blocker, "collect-discovery.json")}

	// THE WRITE HALF IS THE ONE THIS STATE IS ABOUT, and it is now a separate
	// method: a store that cannot be written keeps no baseline, so record() must
	// report it rather than let the collect look healthy and re-fire its trigger
	// forever.
	require.Error(t, d.record(baselineCommit{key: "code/repo@main", sig: "sig-1"}),
		"a store that cannot be written keeps no baseline")

	_, err := d.changed("code/repo@main", "sig-1")
	require.Error(t, err, "and the read half cannot reach it either")
	require.Contains(t, err.Error(), "collect discovery store")
}

// TestDiscoveryStore_NilStoreAborts is state (d): the home directory could not be
// resolved, so this machine holds none of the client's state.
func TestDiscoveryStore_NilStoreAborts(t *testing.T) {
	var d *discoveryStore

	changed, err := d.changed("code/repo@main", "sig-1")
	require.Error(t, err, "no store at all is a client-environment failure, not a first collect")
	require.False(t, changed)

	require.Error(t, d.record(baselineCommit{key: "code/repo@main", sig: "sig-1"}),
		"the commit half must refuse a machine that holds none of this client's state")
}

// TestWriteResult_DiscoveryStoreFailureAbortsCollect is NOT a fifth state — it is
// the PROPAGATION property, and the only catcher for the way this change can be
// implemented perfectly and still do nothing: rewriting all three store functions
// exactly as specified and then writing `if err != nil { changed = true }` at the
// call site, which restores the retired lane while satisfying every store-level
// test and every signature gate.
func TestWriteResult_DiscoveryStoreFailureAbortsCollect(t *testing.T) {
	prev := defaultDiscoveryStore
	corrupt := &discoveryStore{path: filepath.Join(t.TempDir(), "collect-discovery.json")}
	require.NoError(t, os.WriteFile(corrupt.path, []byte("{not json"), 0o600))
	defaultDiscoveryStore = corrupt
	t.Cleanup(func() { defaultDiscoveryStore = prev })

	t.Setenv(collectDiffEnv, "on")
	client, rec := startRecordingIngest(t)
	result := twoFileResult()
	rec.manifest = manifestMatching(result)

	err := NewUploadSink(client).WriteResult(context.Background(), "", result)
	require.Error(t, err, "a store failure must abort the COLLECT, not degrade it")
	require.Contains(t, err.Error(), corrupt.path, "and the message must name the path")
	// THE DISCRIMINATING LEG: an abort raised after the upload satisfies an
	// error-only assertion while costing exactly what the ruling forbids.
	require.Zero(t, chunkCount(rec), "no chunk may be sent once the store has failed")
}
