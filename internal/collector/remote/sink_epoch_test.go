// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The collect epoch identifies one collection, and the server's collect GC keys on
// it. UNIQUENESS is the load-bearing property, not ordering: every server-side
// consumer compares the value for equality, and a REUSED value silently corrupts
// two different ways, neither of which needs concurrency to trigger —
//
//	the base sweep tombstones "collect_epoch <> $1", so a collection that reuses an
//	earlier collection's value leaves that earlier set alive forever; and a reused
//	value merges a crashed run's __collect_seen rows into this run's presence set,
//	hiding the very deletions the GC exists to make.
//
// The old mint was a bare Add(1) on a zero-valued atomic owned by one process, so
// every client's first collect was epoch 1 and every restart replayed the sequence.
// These tests pin the properties that replaced it.

func TestMintEpoch_NeverZeroAndFitsSignedInt64(t *testing.T) {
	s := &UploadSink{}

	got := s.mintEpoch()

	// 0 is reserved: the nodes column is `collect_epoch BIGINT NOT NULL DEFAULT 0`,
	// so a minted 0 would be indistinguishable from a never-stamped row.
	assert.NotZero(t, got, "0 is the default/unstamped sentinel and must never be minted")

	// Every SQL call site casts int64(epoch). A value above MaxInt64 persists
	// NEGATIVE, which silently breaks the age-based presence-row reclaim.
	assert.LessOrEqual(t, got, uint64(math.MaxInt64),
		"epoch must fit the signed BIGINT it persists into")
}

func TestMintEpoch_IsSelfDating(t *testing.T) {
	// The salt occupies the low epochSaltBits, so the value is the true time
	// rounded down to a slot boundary and then OR'd with the salt — accurate to
	// within one slot in either direction, never exact.
	const slack = uint64(1) << epochSaltBits

	before := uint64(time.Now().UnixNano())
	got := (&UploadSink{}).mintEpoch()
	after := uint64(time.Now().UnixNano())

	// The server reclaims presence rows leaked by collections that died before
	// their cleanup ran, using an age predicate on the epoch ITSELF. That only
	// works while the epoch approximates its own creation time. ~1ms of drift is
	// irrelevant against a reclaim window measured in hours; unbounded drift
	// (a counter, or a random value) would break the reclaim entirely.
	assert.GreaterOrEqual(t, got, before-slack, "epoch drifted below its creation time")
	assert.LessOrEqual(t, got, after+slack, "epoch drifted above its creation time")

	// Pin the drift bound itself, so widening epochSaltBits without revisiting
	// the server's reclaim window fails here rather than silently in production.
	assert.Less(t, slack, uint64(time.Second),
		"salt precision must stay far below the server's leak-reclaim window")
}

func TestMintEpoch_StrictlyIncreasingUnderConcurrency(t *testing.T) {
	s := &UploadSink{}
	const goroutines, perGoroutine = 8, 200

	var mu sync.Mutex
	seen := make(map[uint64]struct{}, goroutines*perGoroutine)

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perGoroutine {
				e := s.mintEpoch()
				mu.Lock()
				seen[e] = struct{}{}
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	// The wall clock has coarser resolution than the mint rate, so many of these
	// calls read an IDENTICAL time value. The prev+1 CAS floor is what keeps them
	// distinct — without it a burst of collections inside one clock tick would
	// reuse a value, which is exactly the corruption being fixed.
	assert.Len(t, seen, goroutines*perGoroutine,
		"every mint must be unique even when the clock does not advance between calls")
}

func TestMintEpoch_SurvivesABackwardsClock(t *testing.T) {
	s := &UploadSink{}

	// Simulate a process that already minted from a FUTURE clock (NTP step back,
	// or a suspended laptop resuming). A monotonic-only-by-clock mint would repeat
	// or reverse here.
	future := uint64(time.Now().Add(time.Hour).UnixNano())
	s.epoch.Store(future)

	first := s.mintEpoch()
	second := s.mintEpoch()

	assert.Greater(t, first, future, "a mint behind the last value must still advance")
	assert.Greater(t, second, first, "mints must remain strictly increasing")
}

func TestMintEpoch_DistinctSinksDoNotCollide(t *testing.T) {
	// Two UploadSinks stand in for two client PROCESSES — the real-world shape,
	// since each client process constructs its own sink and they all write the same
	// shared graphs. Under the old zero-seeded counter both would mint 1, then 2,
	// then 3, colliding on every single collect.
	a, b := &UploadSink{}, &UploadSink{}

	const each = 50
	epochs := make(map[uint64]string, each*2)
	for i := range each {
		ea, eb := a.mintEpoch(), b.mintEpoch()

		prev, dup := epochs[ea]
		require.False(t, dup, "sink A collided with %s at iteration %d (epoch %d)", prev, i, ea)
		epochs[ea] = "A"

		prev, dup = epochs[eb]
		require.False(t, dup, "sink B collided with %s at iteration %d (epoch %d)", prev, i, eb)
		epochs[eb] = "B"
	}

	assert.Len(t, epochs, each*2)
}
