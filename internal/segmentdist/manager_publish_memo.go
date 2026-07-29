// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"time"
)

// coveragePublishMemoTTL bounds how long the PUBLISH-path shipped denominator may be
// reused across consecutive publish attempts before it must be read fresh. It sits at
// the LOW end of the useful band on purpose: the storm-collapse benefit saturates
// early — a short window already removes most of the repeated round-trips a
// coverage-skip storm rings up, and widening it recovers only a few points more —
// while the staleness exposure (the interval in which another engine's publish
// against the SAME manifest goes unobserved by this manager) scales LINEARLY with the
// window. A flat benefit curve against a linear risk curve takes the smaller window.
const coveragePublishMemoTTL = 30 * time.Second

// coverageDenominator is the memoized RESULT of shippedDocCountForRatio — the shipped
// doc count and its disarm verdict — plus the instant the entry expires.
type coverageDenominator struct {
	shipped int
	disarm  bool
	expires time.Time
}

// belowCoverageRatio is the publish gate's ratio comparison, lifted VERBATIM out of
// publishCoverageOK so the cold read and the memo-derived re-verify provably share ONE
// expression rather than two re-derivations of it. disarm==true means the denominator
// is untrustworthy (a pre-doc_count blob) or the corpus is below the floor (a tiny
// graph): the ratio is not meaningful there, so it never blocks.
func belowCoverageRatio(resident, shipped int, disarm bool) bool {
	return !disarm && float64(resident) < residentBackstopRatio*float64(shipped)
}

// shippedDocCountForRatioCached is the PUBLISH-path read of the shipped denominator:
// it serves a live memo entry with NO List, and otherwise falls through to the shared
// shippedDocCountForRatio probe and memoizes that result for coveragePublishMemoTTL. A
// FAILED read is never memoized. `cached` reports whether the value came out of the
// memo — which is what obliges the caller to re-derive before honoring a PASS.
//
// THE SAFETY PROPERTY IS THE RE-VERIFY, NOT THE HOOKS: a publish never proceeds on a
// value that came out of the memo. When a cached denominator would let the ratio PASS,
// publishCoverageOK discards the entry and re-derives before honoring it. In the
// ordinary single-attempt case that re-derive is a fresh List; under a concurrent
// attempt it may instead land on an entry another in-flight attempt stored
// microseconds earlier — equally fresh, but not literally a new List. So the guarantee
// is "no pass is ever based on the rejected cached value", NOT "always issues a fresh
// List".
//
// WHAT THE INVALIDATION HOOKS CANNOT SEE: the two hooks fire on THIS manager's own
// ship and its own successful publish. They cannot cover the deterministic-rebuild
// engine, because the embed and rebuild HNSW engines carry the SAME format name and
// therefore key ONE per-writer manifest while living in DISTINCT distManager instances
// with DISTINCT memos. A rebuild's publish changes the shared denominator invisibly to
// this manager and is observed only at TTL expiry. Cross-instance invalidation is out
// of scope by design; the re-verify plus the TTL bound is the accepted shape.
//
// STALE-SMALL (the corpus-wipe direction) is CLOSED UNCONDITIONALLY by the re-verify.
// A cached denominator smaller than the current one makes the ratio easier to clear —
// exactly the direction the gate exists to refuse — and no pass is ever taken on a
// cached value. It costs nothing: a memo hit that would pass spends 0 on the cached
// read and 1 on the re-derive, exactly the 1 a cold read spends. A memo MISS never
// re-derives.
//
// STALE-LARGE (the conservative direction) is ACCEPTED and TTL-bounded. When the
// manifest SHRINKS relative to the cached value, the gate compares a real resident
// against an inflated denominator and SKIPS where fresh data would have passed. The
// route to an inflated denominator is the embed publish's UNION manifest: it publishes
// its own resident set unioned with the rebuild engine's resident digests, and a later
// rebuild-only publish drops the embed half, so the manifest shrinks. Both engines can
// contribute because they PARTITION the same nodes differently — the rebuild driver
// chunks id-ascending at a fixed segment size while the embed engine seals whatever
// the arrival order accumulates, so the two produce segments over DIFFERENT doc
// groupings, each carrying its own content-hash digest and doc count, and the publish
// dedups the union only where a grouping happens to be byte-identical. (A prune/merge
// shrinking the manifest is the second route.) Three reasons it is acceptable:
//
//   - It is an AMPLIFICATION, not a new failure class: the memo-free path reads the
//     same inflated manifest whenever it is the current one. The memo only extends how
//     long a SUPERSEDED inflated value keeps being reused, and only to expiry.
//   - The direction is conservative — a publish is DEFERRED, never wrongly allowed.
//   - The coverage-skip streak's escape hatch is untouched: markCoverageSkip reads the
//     resident doc count FRESH on every call and resets the streak on any resident
//     rise. The memo caches only the shipped DENOMINATOR, never the resident numerator.
//
// CONCURRENCY: no mutex. Two concurrent attempts can both miss and both List; that is
// benign (they compute the same denominator, last store wins) and costs one redundant
// List in a rare race, whereas holding a lock across a List round-trip would serialize
// publishes and raise a lock-ordering question against shipMu. This matches the
// package's existing lock-free field idiom (recovering, l2Loaded, importedGen/
// shippedGen).
func (m *distManager[Q, S]) shippedDocCountForRatioCached(
	ctx context.Context,
) (shipped int, disarm bool, cached bool, err error) {
	if entry := m.coverageMemo.Load(); entry != nil && time.Now().Before(entry.expires) {
		return entry.shipped, entry.disarm, true, nil
	}

	shipped, disarm, err = m.shippedDocCountForRatio(ctx)
	if err != nil {
		// A failed read is NEVER memoized — the next attempt must retry the probe.
		return 0, false, false, err
	}
	m.coverageMemo.Store(&coverageDenominator{
		shipped: shipped,
		disarm:  disarm,
		expires: time.Now().Add(coveragePublishMemoTTL),
	})
	return shipped, disarm, false, nil
}

// invalidateCoverageMemo drops this manager's memoized denominator so the next
// publish-path read is cold. It is called on THIS manager's own ship (new blobs
// landed, so the denominator has changed) and on its own successful publish (the
// manifest it just swapped IS the new denominator).
//
// KNOWN UNCOVERED PATH, recorded so the omission is not mistaken for an oversight:
// ship() runs shipNew and THEN reconcilePrune. On a pass whose ship diff is EMPTY,
// shipNew returns early without invalidating, yet reconcilePrune can still prune
// segments server-side and so shrink the shipped denominator. That is the stale-LARGE
// direction — conservative and self-correcting at expiry (see
// shippedDocCountForRatioCached) — so no hook is added for it.
func (m *distManager[Q, S]) invalidateCoverageMemo() {
	m.coverageMemo.Store(nil)
}
