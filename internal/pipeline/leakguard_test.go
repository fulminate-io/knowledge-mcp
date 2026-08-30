// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak gate.
//
// Start spawns the summary and embed workers (pipeline.go), and waitWithCtx
// detaches its own waiter (pipeline_stop.go). The collector's wake loops run for
// the lifetime of the collector rather than the lifetime of a call, so a test that
// mints a pipeline or a collector and returns without stopping it leaves those
// loops waking on a ticker for the remainder of the test BINARY.
//
// THIS PACKAGE IS WHY THE GATE IS WORTH HAVING. Two real leaks were found here the
// moment a guard was put in place — one test minting a searchengine.SegmentedIndex
// without Close (the merger goroutine that package's own guard exists to catch,
// leaking through a package that had no guard), and one leaving a collector summary
// loop running. Neither failed any test. Both are fixed; this gate is what stops
// them coming back.
//
// The allowlist is deliberately EMPTY. An entry added here later must name the
// goroutine and say why its lifetime legitimately exceeds the test that started it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
