// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak gate.
//
// Start spawns the thought loop (loop_lifecycle.go:30) and Stop spawns its drain
// (:107). The loop wakes on a ticker and on demand, so a test that starts a loop
// and asserts on one propagation without stopping it leaves the loop running
// against a store the test is about to tear down.
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
