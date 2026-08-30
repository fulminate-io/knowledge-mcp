// SPDX-License-Identifier: Apache-2.0

package web

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak gate.
//
// This package's goroutines are TEST-side: fetch_test.go runs a stub HTTP server to
// serve the fetcher under test. A stub server left running holds a listening socket
// for the rest of the binary, which is how an unrelated later test acquires a port
// it did not expect.
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
