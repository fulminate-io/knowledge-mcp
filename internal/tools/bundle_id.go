// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"crypto/rand"
	"encoding/hex"
)

// NewBundleID returns a fresh random bundle identifier: 16 bytes of
// crypto/rand, hex-encoded to a 32-character string. Client-side intercepts
// (and the instruction-bootstrap package) generate one of these at the start
// of a multi-write batch and stamp it onto the create_batch / update_batch
// wire envelope so every node + edge created by the batch shares a single
// bundle_anchor in the server's version overlay.
//
// Client-local port of the former pkg/store.NewBundleID — the generator is
// pure client-side (crypto/rand); only the resulting bundle_id STRING crosses
// the wire on MutationPlan.bundle_id.
//
// The function panics if crypto/rand.Read fails, which only happens on a
// fundamental OS randomness failure (kernel CSPRNG unavailable). The panic is
// intentional: callers cannot meaningfully recover, and silently returning an
// empty ID would cause the server to treat the batch as unbundled.
func NewBundleID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("tools.NewBundleID: crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}

// newBundleID is the unexported alias the in-package intercept code uses. It
// exists so the many existing newBundleID() call sites stay untouched while
// NewBundleID is the exported entry point the bootstrap package calls.
func newBundleID() string {
	return NewBundleID()
}
