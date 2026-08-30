// SPDX-License-Identifier: Apache-2.0

package cicd

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak gate.
//
// RunSubCollectors fans out one goroutine per sub-collector (runner.go:76). If a
// sub-collector blocks — a network call with no deadline, a channel nobody drains —
// its goroutine outlives the collect that started it, and the per-test symptom is
// nothing at all.
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
