// SPDX-License-Identifier: Apache-2.0

package ast

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak gate.
//
// runWorkers spawns the match feeder (match_walk.go:96) plus a worker per CPU. The
// feeder blocks sending into the work channel, so a match that is abandoned
// mid-walk — a cancelled context, an early return on first hit — leaves the feeder
// parked on a send that nobody will ever receive.
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
