// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTestManifest swaps the package-level defaultRepoManifest for one rooted at
// a temp path for the duration of the test, restoring the original after. This
// keeps the real ~/.knowledge/repos.json untouched while exercising the
// package-level recordRepoDir / lookupRepoDir helpers the consumers call.
func withTestManifest(t *testing.T) *repoManifest {
	t.Helper()
	prev := defaultRepoManifest
	m := &repoManifest{path: filepath.Join(t.TempDir(), "repos.json")}
	defaultRepoManifest = m
	t.Cleanup(func() { defaultRepoManifest = prev })
	return m
}

func TestRecordCollectedRepo_CodeRecords(t *testing.T) {
	m := withTestManifest(t)
	// A code collect records filepath.Base(id) → id (id is already absolute).
	recordCollectedRepo("code", "/Users/me/code/knowledge")
	got, ok := m.Lookup("knowledge")
	require.True(t, ok, "a successful code collect must record the repo→path mapping")
	assert.Equal(t, "/Users/me/code/knowledge", got)
}

func TestRecordCollectedRepo_NonCodeNoOp(t *testing.T) {
	m := withTestManifest(t)
	// Non-code collectors (web/pdf/aws/...) are addressed by URL/account, not a
	// repo name — they must NOT write a manifest entry.
	for _, typ := range []string{"web", "pdf", "aws", "gcp", "k8s", "logs", "github"} {
		recordCollectedRepo(typ, "/Users/me/code/knowledge")
	}
	_, ok := m.Lookup("knowledge")
	assert.False(t, ok, "non-code collects must not populate the repo manifest")
}

func TestRecordCollectedRepo_KeyIsBasename(t *testing.T) {
	m := withTestManifest(t)
	// The manifest key is the path basename — the same name `collect` keys the
	// code graph under — not the full path.
	recordCollectedRepo("code", "/Users/me/work/checkouts/agent")
	got, ok := m.Lookup("agent")
	require.True(t, ok)
	assert.Equal(t, "/Users/me/work/checkouts/agent", got)
	_, fullPathLookup := m.Lookup("/Users/me/work/checkouts/agent")
	assert.False(t, fullPathLookup, "manifest must be keyed by basename, not the full path")
}
