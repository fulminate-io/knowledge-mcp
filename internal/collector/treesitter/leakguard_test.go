// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak gate.
//
// This package's goroutines are TEST-side: chunker_multilang_test.go drives the
// chunker concurrently to prove the parser pool is safe under parallel use.
// Production chunking here is synchronous. The guard therefore protects test
// hygiene — a driver goroutine outliving its test still holds a tree-sitter parser,
// which is a cgo handle rather than a plain Go object.
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
