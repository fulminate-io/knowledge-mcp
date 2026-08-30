// SPDX-License-Identifier: Apache-2.0

package codexcli

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak gate.
//
// runCLI spawns the goroutine that pumps the subprocess's output (subprocess.go:59).
// It exits when the pipe closes, so a test whose fake CLI never exits leaves that
// pump blocked on a read and the child process parented to the test binary.
//
// THE PER-TEST SYMPTOM IS NOTHING AT ALL, which is exactly why this has to be a
// package-level gate rather than an assertion someone remembers to write: a leaked
// goroutine does not fail the test that leaked it, it fails whatever runs after it,
// or nothing at all until the leak becomes a resource exhaustion in production.
//
// The allowlist is deliberately EMPTY. An entry added here later must name the
// goroutine and say why its lifetime legitimately exceeds the test that started it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
