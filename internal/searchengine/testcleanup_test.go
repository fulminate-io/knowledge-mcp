// SPDX-License-Identifier: Apache-2.0

package searchengine

import "testing"

// closeOnCleanup registers v.Close with the test and returns v unchanged, so an
// index can be minted AND made the test's responsibility in a single expression.
//
// IT EXISTS SO THE OWNERSHIP IS EXPRESSED AT THE MINT POINT. New starts a merger
// goroutine that only Close stops, so an index built and dropped leaks a ticker
// for the remainder of the test BINARY — a cost paid by every test that runs
// after it, and by none of the assertions in the test that caused it. Wrapping
// the constructor is what keeps that from being a rule each call site has to
// remember: the index and its cleanup arrive together, and the package's
// goroutine-leak gate (leakguard_test.go) fails the package if a new call site
// ever forgets.
func closeOnCleanup[Q, S any](t testing.TB, e *SegmentedIndex[Q, S]) *SegmentedIndex[Q, S] {
	t.Helper()
	t.Cleanup(e.Close)
	return e
}
