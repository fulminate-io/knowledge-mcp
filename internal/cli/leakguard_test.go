// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak gate.
//
// proxyOverWS spawns both directions of the tunnel pump at once —
// pumpStdinToWS and pumpWSToStdout (tunnel_proxy.go:152-153). They exit only when
// the connection closes, so a test that opens a tunnel against a stub and returns
// without closing it leaves two goroutines blocked on a read that never completes.
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
