// SPDX-License-Identifier: Apache-2.0

package thought

import "sync"

// singleflight.go holds the per-account reflection single-flight guard. The
// hourly PropagationLoop tick, the boot-time detection in Start, and the manual
// propagate tool all run the SAME full cluster-detection + DeGroot pass over the
// same graph identity. Without a guard a manual propagate firing while the hourly
// tick is mid-pass (or vice versa) would stampede two concurrent full recomputes
// against one corpus. The guard coalesces them: the first claimant runs, every
// concurrent claimant gets ok==false and skips with a loud absorbed-trigger log.
//
// The guard lives in the thought package (the reflection owner) rather than in
// tools (where the RebuildSegments single-flight idiom this mirrors lives) so the
// internal loop can claim it WITHOUT importing tools — tools already imports
// thought, so the guard's home here avoids an import cycle.

// reflectInFlight is the per-key reflection single-flight guard, mirroring the
// RebuildSegments rebuildSegmentsInFlight shape (a plain Mutex + set claimed at
// entry, released on completion). A concurrent claim for a key already in the set
// coalesces (ok==false) rather than racing a duplicate full pass.
var (
	reflectInFlightMu sync.Mutex
	reflectInFlight   = map[string]struct{}{}
)

// ReflectionPassKey is the single graph identity every reflection pass operates
// on. It mirrors the string(graphType)+"/"+name precedent shape of the
// RebuildSegments single-flight key. The OSS daemon is single-account (a
// multi-account daemon is out of scope per the ticket) and cloud login already
// scopes the router, so one constant key is correct and future-proof: when a
// per-account key is ever needed, this constant becomes the per-account derivation
// without changing any claim site.
const ReflectionPassKey = "knowledge/default"

// AcquireReflectionPass claims the reflection single-flight guard for key. On the
// first (uncontended) claim it returns (release, true) where release frees the key
// — call it on every exit path, typically via defer. On a concurrent claim while
// the key is held it returns (nil, false): a benign coalesce, NOT an error (mirrors
// RebuildSegments returning ran=false on a busy key). The caller treats ok==false
// as "another pass absorbed this trigger" and skips its body.
func AcquireReflectionPass(key string) (release func(), ok bool) {
	reflectInFlightMu.Lock()
	if _, busy := reflectInFlight[key]; busy {
		reflectInFlightMu.Unlock()
		return nil, false
	}
	reflectInFlight[key] = struct{}{}
	reflectInFlightMu.Unlock()
	return func() {
		reflectInFlightMu.Lock()
		delete(reflectInFlight, key)
		reflectInFlightMu.Unlock()
	}, true
}
