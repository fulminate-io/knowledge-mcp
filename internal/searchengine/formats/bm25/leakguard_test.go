// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when a test leaves a goroutine running.
//
// This package's tests build SegmentedIndex values over this format, and each
// one spawns a merger goroutine that only Close stops. Without this gate a
// leaked ticker survives to the end of the test binary and its cost lands on
// whichever later test is running — a failure with no local symptom, which is
// why it is a package-level check rather than a per-test assertion.
//
// The allowlist is deliberately EMPTY: this package starts no goroutine that
// outlives a test on purpose, so any survivor is a defect.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
