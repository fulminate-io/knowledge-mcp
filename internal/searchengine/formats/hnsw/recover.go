// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"log/slog"
	"runtime/debug"
)

// withRecover wraps fn with a deferred recover that logs any panic with the
// given site name and a stack trace. A panic in a build-pool goroutine would
// otherwise crash the whole process; this contains it. Copied from the server's
// vector package (recover.go), which in turn mirrors domains/store/recover.go.
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
