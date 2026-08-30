// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak gate.
//
// RunPrecheck spawns the provider probe (precheck.go:73) on a context it derives
// from its caller's. A precheck started and then abandoned — the common shape in a
// table test that only wants the first result — leaves the probe finishing an HTTP
// round trip against a provider after its test has passed.
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
