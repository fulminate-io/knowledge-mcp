// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPackageGate_CacheRootRedirectedAwayFromHome proves TestMain's redirect is ACTIVE at
// run time, not merely declared. The structural gate on TestMain's body proves the seam is
// named; only this proves cacheSessionParquet would resolve somewhere other than the user's
// real cache when a test forgets to override it.
//
// Both clauses are load-bearing. Non-empty catches the redirect being wiped by a cleanup
// that assigns "" — the exact defect that made an earlier fix inert, and the reason
// cache_test.go's cleanups save and restore. Not-under-home catches a redirect that points
// at a directory that is still inside the home tree, which would leave the pollution in
// place while reading as fixed.
func TestPackageGate_CacheRootRedirectedAwayFromHome(t *testing.T) {
	require.NotEmpty(t, cacheRootDir,
		"TestMain must redirect the cache root; an empty root falls through to ~/.knowledge/transcripts-cache")

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.NotEmpty(t, home)

	root, err := filepath.Abs(cacheRootDir)
	require.NoError(t, err)
	homeAbs, err := filepath.Abs(home)
	require.NoError(t, err)

	assert.False(t, strings.HasPrefix(root+string(filepath.Separator), homeAbs+string(filepath.Separator)),
		"the redirected cache root %q must not resolve under the home dir %q", root, homeAbs)
}

// TestCacheSessionParquet_DefaultRootIsNotWrittenByThisSuite is the known-positive for the
// gate above: it drives cacheSessionParquet through the SAME call the polluting tests make
// and asserts the lane landed under the redirected root. Without it, the gate above proves
// only that a variable holds a path — not that the writer reads that variable.
func TestCacheSessionParquet_DefaultRootIsNotWrittenByThisSuite(t *testing.T) {
	require.NotEmpty(t, cacheRootDir, "TestMain's redirect is the subject of this test")

	src := filepath.Join(t.TempDir(), "src.parquet")
	require.NoError(t, os.WriteFile(src, []byte("PAR1-not-really"), 0o600))

	require.NoError(t, cacheSessionParquet("claude", "gate-probe", src))

	assert.FileExists(t, filepath.Join(cacheRootDir, "claude", "gate-probe.parquet"),
		"the lane lands under the redirected root")

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(home, ".knowledge", "transcripts-cache", "claude", "gate-probe.parquet"),
		"and never under the real cache")
}
