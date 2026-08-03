package searchengine

import (
	"math/bits"
	"sync/atomic"
)

// liveDocs is a lock-free bitset tracking which intra-segment ordinals are still
// live. It is the ONLY mutable per-segment state. Reads (Live) are pure atomic
// word loads with no lock; writes (Kill) clear a bit via a CAS loop. This is what
// lets Search fan out over segments concurrently while Delete flips bits.
//
// Written fresh rather than reusing the server's bitset
// (cmd/knowledge-server/internal/index/vector/heap.go): that type is non-atomic
// (and server-internal / unimportable from this module), so it cannot back
// lock-free concurrent reads while a deleter mutates it.
type liveDocs struct {
	words []atomic.Uint64
	n     int
}

// newLiveDocs returns an all-live bitset sized for n ordinals.
func newLiveDocs(n int) *liveDocs {
	ld := &liveDocs{words: make([]atomic.Uint64, (n+63)/64), n: n}
	for i := range ld.words {
		ld.words[i].Store(^uint64(0))
	}
	return ld
}

// newLiveDocsFromTombstones builds an all-live bitset over n ORDINALS then kills
// the ordinals of any member listed in tombstones. Used at Import so pulled
// segments start with their already-deleted documents dead.
//
// n IS THE ORDINAL COUNT, NOT THE MEMBER COUNT, and the two differ exactly when a
// segment carries an id more than once: members is a last-wins map, so it is
// smaller than the ordinal space, and an ordinal past its end reads DEAD. Sizing
// this bitset by len(members) would therefore silently kill every member whose
// ordinal fell beyond the distinct count.
func newLiveDocsFromTombstones(n int, tombstones []ExternalID, members idSet) *liveDocs {
	ld := newLiveDocs(n)
	for _, id := range tombstones {
		if ord, ok := members[id]; ok {
			ld.Kill(ord)
		}
	}
	return ld
}

// distinctDeadCount counts the DISTINCT members whose ordinal is dead. It is the
// counterpart of a distinct DocCount: liveDocs.DeadCount walks the ordinal space,
// so on a segment holding duplicate ids it counts per copy, and pairing a
// per-copy dead count with a distinct doc count yields a dead RATIO that can
// exceed 1.0 and drive the merge trigger off nonsense. Both counts are derived
// the same way so the ratio stays a ratio.
func distinctDeadCount(members idSet, live *liveDocs) int {
	dead := 0
	for _, ord := range members {
		if !live.Live(ord) {
			dead++
		}
	}
	return dead
}

// Live reports whether ordinal i is still live. Lock-free: a single atomic load
// plus a bit-mask. Out-of-range ordinals are reported dead.
func (l *liveDocs) Live(i int) bool {
	if i < 0 || i >= l.n {
		return false
	}
	w := l.words[i>>6].Load()
	return w&(uint64(1)<<(uint(i)&63)) != 0
}

// Kill clears the live bit for ordinal i via a CAS loop. Idempotent — killing an
// already-dead ordinal is a no-op. Safe to call concurrently with Live.
func (l *liveDocs) Kill(i int) {
	if i < 0 || i >= l.n {
		return
	}
	word := &l.words[i>>6]
	mask := uint64(1) << (uint(i) & 63)
	for {
		old := word.Load()
		if old&mask == 0 {
			return // already dead
		}
		if word.CompareAndSwap(old, old&^mask) {
			return
		}
	}
}

// Revive sets the live bit for ordinal i via a CAS loop — the mirror of Kill, and
// the ONLY other mutator of this bitset. Idempotent; out-of-range ordinals are
// ignored exactly as Kill ignores them; safe to call concurrently with Live.
//
// IT EXISTS FOR ONE CALLER: the write-side supersede, where a batch re-added after
// a delete produces a segment whose content hash ALIASES a resident one, so the
// publish is dropped as idempotent and the resident copy — currently dead — is the
// copy that must answer for the write. A WRITE IS THE ONLY THING ENTITLED TO DO
// THIS: it is a delete being undone by a later write of the same id, never a
// resurrection of something still deleted.
func (l *liveDocs) Revive(i int) {
	if i < 0 || i >= l.n {
		return
	}
	word := &l.words[i>>6]
	mask := uint64(1) << (uint(i) & 63)
	for {
		old := word.Load()
		if old&mask != 0 {
			return // already live
		}
		if word.CompareAndSwap(old, old|mask) {
			return
		}
	}
}

// LiveCount returns the number of live ordinals (atomic loads, lock-free).
func (l *liveDocs) LiveCount() int {
	live := 0
	for i := range l.words {
		live += bits.OnesCount64(l.words[i].Load())
	}
	// The final word may have padding bits above n that newLiveDocs set live;
	// subtract them so the count reflects only real ordinals.
	if pad := len(l.words)*64 - l.n; pad > 0 {
		live -= pad
	}
	return live
}

// DeadCount returns the number of killed ordinals (lock-free).
func (l *liveDocs) DeadCount() int {
	return l.n - l.LiveCount()
}
