// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

// sink_epoch.go — the per-collection epoch: how it is salted per process and
// how it is minted monotonically. Split out of sink.go, which keeps the sink
// type, the upload flow and the diff plan, so that file stays under the repo's
// file-length cap. The epoch state itself lives on UploadSink (sink.go), because
// Go cannot add a struct field from another file.

// epochSaltBits is how many low bits of the epoch carry the per-process salt
// instead of the clock. It trades timestamp precision for cross-process
// separation: 20 bits leaves ~1ms of dating resolution (irrelevant against the
// server's hours-long leak-reclaim window) and gives 2^20 process slots.
const epochSaltBits = 20

// newEpochSalt draws a salt in [1, 2^epochSaltBits). Never 0 — the sink treats a
// zero salt as "not yet drawn", and a fixed salt would put every minter in one
// slot and reinstate the collision the salt exists to prevent.
func newEpochSalt() uint64 {
	const mask = 1<<epochSaltBits - 1
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Degrade to the clock rather than to a constant.
		if v := uint64(time.Now().UnixNano()) & mask; v != 0 {
			return v
		}
		return 1
	}
	if v := binary.LittleEndian.Uint64(b[:]) & mask; v != 0 {
		return v
	}
	return 1
}

// salt returns this sink's epoch salt, drawing it on first use so the zero-value
// UploadSink stays valid.
//
// The salt is what disambiguates one minter from another at the same instant. Two
// minters reading time.Now() inside the same tick compute the SAME nanoseconds and
// share no atomic to break the tie, so a clock-only mint would reissue one value
// to two collections — the exact reuse this mechanism exists to prevent. A
// monotonic CAS cannot help: it is per-sink state, and each sink's CAS succeeds
// independently of the other's.
//
// Scoped per SINK rather than per process deliberately. Production constructs one
// sink per client process, so the two are equivalent there; making it per-sink
// costs nothing and additionally separates any two sinks that ever coexist.
func (s *UploadSink) salt() uint64 {
	if v := s.epochSalt.Load(); v != 0 {
		return v
	}
	if s.epochSalt.CompareAndSwap(0, newEpochSalt()) {
		return s.epochSalt.Load()
	}
	return s.epochSalt.Load() // lost the race; the winner's salt is authoritative
}

// mintEpoch returns the identifier for one collection. It is:
//
//   - UNIQUE across processes and restarts — the high bits come from the wall
//     clock and the low bits from this process's salt, so two clients collide
//     only by drawing the same 1-in-2^20 salt AND minting within the same ~1ms.
//     Contrast the old bare Add(1) from zero, where two clients collided on
//     EVERY collect with certainty. Uniqueness is the property the collect GC
//     depends on; ordering is not — every consumer compares for equality.
//   - MONOTONIC within this process — the CAS floor advances by a whole salt
//     slot, so a coarse or backwards clock cannot repeat or reverse a value we
//     already minted, and the advance preserves the salt in the low bits.
//   - SELF-DATING to ~1ms — the value IS approximately its own creation time,
//     which is what lets the server reclaim presence rows leaked by collections
//     that died before their cleanup ran, using an age predicate on the epoch
//     itself. Do not replace this with a pure counter or a pure random value
//     without giving __collect_seen its own timestamp column; the reclaim in
//     runOverlayDeletionGC depends on this property.
//
// This is a PROBABILISTIC uniqueness guarantee. The only way to make it absolute
// is to allocate the epoch server-side, once per collection, which needs a
// collection delimiter the wire does not currently have.
//
// Nanoseconds fit the signed BIGINT the epoch persists into (~1.8e18 today vs a
// 9.2e18 ceiling, good past 2262) — note every SQL call site casts int64, so a
// value above MaxInt64 would persist NEGATIVE.
func (s *UploadSink) mintEpoch() uint64 {
	const saltMask = 1<<epochSaltBits - 1
	now := uint64(time.Now().UnixNano())&^saltMask | s.salt()
	for {
		prev := s.epoch.Load()
		next := now
		if next <= prev {
			next = prev + 1<<epochSaltBits // whole slot, so the salt survives
		}
		if s.epoch.CompareAndSwap(prev, next) {
			return next
		}
	}
}
