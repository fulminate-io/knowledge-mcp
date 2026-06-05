// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"log/slog"
	"runtime/debug"
)

// withRecover wraps fn with a deferred recover that logs any panic with the
// given site name and a stack trace. A panic in a parallel-tokenize goroutine
// would otherwise crash the whole process; this contains it. Mirrors the HNSW
// format's recover.go (which mirrors the server's bm25/recover.go).
func withRecover(site string, fn func()) func() {
	return func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panic",
					"site", site,
					"err", r,
					"stack", string(debug.Stack()))
			}
		}()
		fn()
	}
}

// goWithRecover starts a new goroutine running fn under withRecover.
func goWithRecover(site string, fn func()) {
	go withRecover(site, fn)()
}
