// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInterceptManage_RegisterRepo_Valid pins the happy path: a name + existing
// absolute-dir root is recorded in the machine-local manifest, the op reports
// success echoing name → path, and a subsequent Lookup returns the recorded dir.
func TestInterceptManage_RegisterRepo_Valid(t *testing.T) {
	m := withTestManifest(t)
	ix := &fakeIndexer{}
	dir := t.TempDir()
	handled, res := manageCall(t, ix,
		`{"operation":"register_repo","name":"myrepo","root":"`+dir+`"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "register_repo: %s", toolResultText(res))

	body := toolResultText(res)
	assert.Contains(t, body, "myrepo", "success output must echo the repo name")
	assert.Contains(t, body, dir, "success output must echo the recorded path")

	got, ok := m.Lookup("myrepo")
	require.True(t, ok, "a successful register_repo must record the name → path mapping")
	assert.Equal(t, dir, got)
}

// TestInterceptManage_RegisterRepo_JSON pins the format:json branch: it echoes
// name → path as a structured object.
func TestInterceptManage_RegisterRepo_JSON(t *testing.T) {
	withTestManifest(t)
	ix := &fakeIndexer{}
	dir := t.TempDir()
	handled, res := manageCall(t, ix,
		`{"operation":"register_repo","name":"myrepo","root":"`+dir+`","format":"json"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "register_repo json: %s", toolResultText(res))

	var decoded struct {
		Name string `json:"name"`
		Root string `json:"root"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &decoded))
	assert.Equal(t, "myrepo", decoded.Name)
	assert.Equal(t, dir, decoded.Root)
}

// TestInterceptManage_RegisterRepo_EmptyName rejects an empty name and writes
// nothing to the manifest.
func TestInterceptManage_RegisterRepo_EmptyName(t *testing.T) {
	m := withTestManifest(t)
	ix := &fakeIndexer{}
	dir := t.TempDir()
	handled, res := manageCall(t, ix,
		`{"operation":"register_repo","name":"","root":"`+dir+`"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "empty name must be rejected")
	_, ok := m.Lookup("")
	assert.False(t, ok, "a rejected register_repo must not write to the manifest")
}

// TestInterceptManage_RegisterRepo_NonAbsoluteRoot rejects a relative root and
// records nothing (validation gates the write).
func TestInterceptManage_RegisterRepo_NonAbsoluteRoot(t *testing.T) {
	m := withTestManifest(t)
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix,
		`{"operation":"register_repo","name":"myrepo","root":"relative/path"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "a non-absolute root must be rejected")
	_, ok := m.Lookup("myrepo")
	assert.False(t, ok, "a rejected register_repo must not write to the manifest")
}

// TestInterceptManage_RegisterRepo_NonDirectoryRoot rejects a root that exists
// but is a FILE (not a directory), and records nothing.
func TestInterceptManage_RegisterRepo_NonDirectoryRoot(t *testing.T) {
	m := withTestManifest(t)
	ix := &fakeIndexer{}
	file := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	handled, res := manageCall(t, ix,
		`{"operation":"register_repo","name":"myrepo","root":"`+file+`"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "a root that is a file (not a dir) must be rejected")
	_, ok := m.Lookup("myrepo")
	assert.False(t, ok, "a rejected register_repo must not write to the manifest")
}

// TestInterceptManage_RegisterRepo_NonExistentRoot rejects a root that does not
// exist on disk.
func TestInterceptManage_RegisterRepo_NonExistentRoot(t *testing.T) {
	m := withTestManifest(t)
	ix := &fakeIndexer{}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	handled, res := manageCall(t, ix,
		`{"operation":"register_repo","name":"myrepo","root":"`+missing+`"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "a non-existent root must be rejected")
	_, ok := m.Lookup("myrepo")
	assert.False(t, ok, "a rejected register_repo must not write to the manifest")
}

// TestInterceptManage_RegisterRepo_Overwrite pins idempotent re-registration:
// re-registering an existing name to a new dir overwrites the recorded path.
func TestInterceptManage_RegisterRepo_Overwrite(t *testing.T) {
	m := withTestManifest(t)
	ix := &fakeIndexer{}
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	_, res1 := manageCall(t, ix,
		`{"operation":"register_repo","name":"myrepo","root":"`+dir1+`"}`)
	require.False(t, res1.IsError, "first register: %s", toolResultText(res1))
	got1, ok := m.Lookup("myrepo")
	require.True(t, ok)
	assert.Equal(t, dir1, got1)

	_, res2 := manageCall(t, ix,
		`{"operation":"register_repo","name":"myrepo","root":"`+dir2+`"}`)
	require.False(t, res2.IsError, "re-register: %s", toolResultText(res2))
	got2, ok := m.Lookup("myrepo")
	require.True(t, ok)
	assert.Equal(t, dir2, got2, "re-registering a name must overwrite its recorded path")
}

// TestInterceptManage_RegisterRepo_NoServerCall is the core scope guard:
// register_repo is PURELY CLIENT-SIDE. It must report handled==true and never
// forward any request to the server (no Index RPC fired), because the recorded
// path is machine-local and must never leave this host.
func TestInterceptManage_RegisterRepo_NoServerCall(t *testing.T) {
	withTestManifest(t)
	ix := &fakeIndexer{}
	dir := t.TempDir()
	handled, res := manageCall(t, ix,
		`{"operation":"register_repo","name":"myrepo","root":"`+dir+`"}`)
	require.True(t, handled, "register_repo must be handled client-side")
	require.False(t, res.IsError, "register_repo: %s", toolResultText(res))
	assert.Empty(t, ix.requests(), "register_repo must NOT forward any request to the server")
	assert.Equal(t, int64(0), ix.indexCalls.Load(), "register_repo must fire zero Index RPCs")
}

// TestRegisterRepo_EndToEndResolveRepoDir proves the full write → read loop:
// register a repo via InterceptManage register_repo, then resolveRepoDir for a
// DIFFERENT current tree resolves that bare cross-repo name to the registered
// directory — through the same machine-local manifest.
func TestRegisterRepo_EndToEndResolveRepoDir(t *testing.T) {
	withTestManifest(t)
	ix := &fakeIndexer{}
	parent := t.TempDir()
	target := filepath.Join(parent, "otherrepo")
	require.NoError(t, os.MkdirAll(target, 0o750))
	cwdTree := filepath.Join(parent, "knowledge") // the current tree — a different dir
	require.NoError(t, os.MkdirAll(cwdTree, 0o750))

	handled, res := manageCall(t, ix,
		`{"operation":"register_repo","name":"otherrepo","root":"`+target+`"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "register_repo: %s", toolResultText(res))

	deps := astTestDeps{rootDir: cwdTree}
	got, err := resolveRepoDir(context.Background(), deps, "otherrepo")
	require.NoError(t, err, "a just-registered cross-repo name must resolve via the manifest")
	assert.Equal(t, target, got, "register_repo → resolveRepoDir must round-trip through the same manifest")
}
