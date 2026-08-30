// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"fmt"
	"os"
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak gate AND redirects the
// parquet cache root away from the user's real ~/.knowledge/transcripts-cache.
//
// Run fans out one goroutine per transcript file (run.go:195) plus a collector for
// their results (:204), and ship.go detaches its own upload. A sync abandoned
// partway — a cancelled context, an early error return — leaves file workers reading
// transcripts and an uploader holding a cloud transport.
//
// THE PER-TEST SYMPTOM IS NOTHING AT ALL, which is exactly why this has to be a
// package-level gate rather than an assertion someone remembers to write: a leaked
// goroutine does not fail the test that leaked it, it fails whatever runs after it,
// or nothing at all until the leak becomes a resource exhaustion in production.
//
// The allowlist is deliberately EMPTY. An entry added here later must name the
// goroutine and say why its lifetime legitimately exceeds the test that started it.
//
// THE CACHE REDIRECT IS THE SAME KIND OF GATE, for the same reason. A test that sets no
// cacheRootDir override makes cacheSessionParquet fall through its empty-root branch to
// os.UserHomeDir() and write a lane into the user's real cache — which the analyzer then
// counts as real usage. Like a leaked goroutine that costs nothing in the test that leaked
// it, a leaked lane fails nothing here and shows up as corpus pollution somewhere else, so
// the redirect belongs at the package level rather than in each test that happens to
// remember it. Any test overriding cacheRootDir must RESTORE the previous value rather
// than clearing it, or it disables this gate for every test that runs after it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "transcriptsync-cache-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "transcriptsync: create test cache root: %v\n", err)
		os.Exit(1)
	}
	cacheRootDir = dir
	// No cleanup, deliberately: goleak.VerifyTestMain calls os.Exit internally, so nothing
	// registered around it runs. The directory is left for the OS to reap — an unreachable
	// removal would read as a guarantee this cannot make.
	goleak.VerifyTestMain(m)
}
