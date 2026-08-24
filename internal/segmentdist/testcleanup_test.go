// SPDX-License-Identifier: Apache-2.0

package segmentdist

import "testing"

// closeOnCleanup registers v.Close with the test and returns v unchanged, so a
// value owning a background worker can be minted AND made the test's
// responsibility in a single expression.
//
// IT EXISTS SO THE OWNERSHIP IS EXPRESSED AT THE MINT POINT. A Manager lazily
// constructs a SegmentedIndex per graph and format, and every SegmentedIndex
// starts a merger goroutine that only Close stops. A test that builds a Manager
// and drops it therefore leaks one ticker per graph it touched — for the
// remainder of the test BINARY, since nothing else ever closes them. Wrapping
// the constructor is what keeps that from being a rule each of a few hundred
// call sites has to remember: the value and its cleanup arrive together, and the
// package's goroutine-leak gate (leakguard_test.go) fails the package if a new
// call site ever forgets.
//
// The constraint is Close() with no return, which is the shape both *Manager and
// *searchengine.SegmentedIndex have. A future closer that returns an error does
// not fit and should not be forced to — it wants its error checked.
func closeOnCleanup[T interface{ Close() }](t testing.TB, v T) T {
	t.Helper()
	t.Cleanup(v.Close)
	return v
}
