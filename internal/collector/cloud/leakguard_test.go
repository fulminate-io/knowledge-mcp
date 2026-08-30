// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak gate.
//
// RunSubCollectors fans out one goroutine per sub-collector (runner.go:75), the
// same shape as the cicd collector beside it. A sub-collector that blocks on a
// cloud API call with no deadline leaks its goroutine into the rest of the binary.
//
// THE PER-TEST SYMPTOM IS NOTHING AT ALL, which is exactly why this has to be a
// package-level gate rather than an assertion someone remembers to write: a leaked
// goroutine does not fail the test that leaked it, it fails whatever runs after it,
// or nothing at all until the leak becomes a resource exhaustion in production.
//
// THE ONE ALLOWLIST ENTRY, and why it is a true statement rather than a silenced
// failure. MEASURED: this package's suite reports
//
//	goleak: Errors on successful test run: found unexpected goroutines:
//	... with go.opencensus.io/stats/view.(*worker).start on top of the stack
//	created by go.opencensus.io/stats/view.init.0
//
// "created by ... init.0" is the whole argument. The goroutine is started by a
// THIRD-PARTY package's init function, which runs when the binary loads, before any
// test — so no test started it, no test can stop it, and this repository exposes no
// lifecycle over it at all. It is reached transitively through the Google Cloud
// client libraries this collector uses. Ignoring it states a true fact about a
// dependency's goroutine lifetime; it hides no defect of ours.
//
// IT IS NARROW ON PURPOSE: IgnoreTopFunction pins one exact top-of-stack function
// name. A broad ignore, or one added to make some OTHER package's red disappear,
// would be the forbidden shape — it would silence real leaks in the same breath.
// Any FURTHER entry here needs its own recorded reason naming the goroutine and why
// its lifetime legitimately exceeds the test that started it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
	)
}
