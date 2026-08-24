// SPDX-License-Identifier: Apache-2.0

package searchengine

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when a test leaves a goroutine running.
//
// IT GUARDS A SPECIFIC, MEASURED FAILURE. Every SegmentedIndex spawns a merger
// goroutine at construction (startMerger), and the only thing that stops it is
// Close. A test that mints an index and drops it therefore leaks a goroutine
// waking on a mergeTickInterval ticker for the remainder of the test BINARY, not
// merely the test — and the cost is paid by every test that runs after it. The
// per-test symptom is nothing at all, which is exactly why this has to be a
// package-level gate rather than an assertion someone remembers to write: the
// damage only becomes visible as scheduler starvation once enough of them
// accumulate, and it lands on whichever test happens to be running then.
//
// The allowlist is deliberately EMPTY. This package starts no goroutine that
// outlives a test on purpose, so any survivor is a defect; an entry added here
// later must name the goroutine and say why its lifetime legitimately exceeds
// the test that started it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
