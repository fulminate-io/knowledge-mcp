// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when a test leaves a goroutine running.
//
// IT GUARDS A SPECIFIC, MEASURED FAILURE. Every distManager owns a
// SegmentedIndex, and every SegmentedIndex spawns a merger goroutine at
// construction that only Close stops. A test that builds a manager and drops it
// therefore leaks a ticker goroutine for the remainder of the test BINARY, and
// this package builds a great many managers. The per-test symptom is nothing at
// all — which is why this has to be a package-level gate rather than an
// assertion someone remembers to write. The damage surfaces only once enough of
// them accumulate to starve the scheduler, and it surfaces on whichever
// unrelated test is running at that moment.
//
// The allowlist is deliberately EMPTY. This package starts no goroutine that
// outlives a test on purpose, so any survivor is a defect; an entry added here
// later must name the goroutine and say why its lifetime legitimately exceeds
// the test that started it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
