// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sort"

	"connectrpc.com/connect"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// fetchMisses Fetches the named segment ids from the source in COUNT-capped
// sub-batches (at most maxFetchSegmentIDs ids per RPC) and concatenates the
// results, so a cold load never issues one unbounded Fetch(allMisses) (the
// 2026-06-19 OOM). It is the single shared Fetch path for both load and reload.
//
// ADAPTIVE HALVING: a chunk is count-capped, but blobs have no client-visible
// byte size, so a count-capped chunk can still exceed the server's
// store.MaxSegmentFetchResponseBytes byte ceiling. When the server rejects a
// chunk with connect.CodeResourceExhausted (the byte-ceiling backstop maps
// store.ErrSegmentFetchTooLarge to that code), fetchMisses HALVES the chunk and
// retries each half, recursing until each sub-chunk fits under the ceiling. Only
// CodeResourceExhausted triggers halving; ANY OTHER error propagates immediately
// with no retry.
//
// BYTE-CEILING HARD-ERROR: if a SINGLE id's blob alone exceeds the server ceiling
// (pathological — one segment > MaxSegmentFetchResponseBytes), halving a 1-id
// chunk cannot make it fit, so fetchMisses returns a hard error rather than
// looping forever. That error propagates to load()/reload() BEFORE any Import or
// importedGen advance, so the id stays re-listable on the next load.
//
// PARTIAL (SHORT-BUT-OK) RESPONSE: fetchMisses does NOT guarantee a blob for every
// requested id. A server may return OK while OMITTING some requested ids (e.g. a
// refcount-GC raced between the caller's List and this Fetch), so the returned set
// can be a strict subset of missIDs with no error. This is NOT silently lossy: the
// caller (loadFromServer) detects the kept-format metas whose blob is absent and
// CLAMPS the importedGen advance below the lowest such generation, so the omitted
// segment stays re-listable and is imported on the next load. fetchMisses returns
// whatever the source served (possibly a subset), or a hard error.
//
// BACKPRESSURE COUPLING (load-bearing assumption): halving keys on
// CodeResourceExhausted, but a server may have a SECOND source of that code — a
// backpressure mechanism that sheds DB-heavy RPCs with CodeResourceExhausted
// meaning "server busy, back off and retry the SAME batch" (the semantic
// OPPOSITE of "batch too big, halve it"). This is SAFE TODAY because the segment
// Fetch RPC is NOT subject to that backpressure shedding — the byte ceiling is
// the sole ResourceExhausted source on Fetch. If a future change makes Fetch
// subject to backpressure, this code MUST disambiguate the byte-ceiling error
// from a backpressure shed (e.g. via a distinguishable detail on
// ErrSegmentFetchTooLarge) BEFORE halving. (graphclient.IsRetryableTransportError
// deliberately does NOT retry ResourceExhausted, so the segment-level halving
// sees the code cleanly with no double-retry interference.)
func (m *distManager[Q, S]) fetchMisses(ctx context.Context, missIDs []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	if len(missIDs) == 0 {
		return nil, nil
	}
	out := make([]searchengine.SegmentBlob, 0, len(missIDs))
	for start := 0; start < len(missIDs); start += maxFetchSegmentIDs {
		end := min(start+maxFetchSegmentIDs, len(missIDs))
		blobs, err := m.fetchChunkAdaptive(ctx, missIDs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, blobs...)
	}
	return out, nil
}

// fetchChunkAdaptive Fetches one already-count-capped chunk, halving it on a
// server byte-ceiling rejection (CodeResourceExhausted) until each sub-chunk
// fits. A 1-id chunk that still exceeds the ceiling is a hard error (no infinite
// loop). Any non-ResourceExhausted error propagates immediately. See fetchMisses
// for the full halving + backpressure-coupling rationale.
func (m *distManager[Q, S]) fetchChunkAdaptive(ctx context.Context, chunk []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	blobs, err := m.source.Fetch(ctx, chunk)
	if err == nil {
		return blobs, nil
	}
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		return nil, err // not a byte-ceiling rejection — propagate, no retry
	}
	// Byte ceiling: the chunk is too large in bytes despite being count-capped.
	// A single id that still over-runs the ceiling cannot be split further.
	if len(chunk) <= 1 {
		return nil, err
	}
	mid := len(chunk) / 2
	left, err := m.fetchChunkAdaptive(ctx, chunk[:mid])
	if err != nil {
		return nil, err
	}
	right, err := m.fetchChunkAdaptive(ctx, chunk[mid:])
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

// unloadUnderPressure drops resident segments (lowest generation first) via
// engine.Unload until the approximate resident-byte total is at or below target.
// Returns the ids it unloaded so the caller can reload them later. The L2 cache
// retains the bytes, so reload is a cache hit.
func (m *distManager[Q, S]) unloadUnderPressure(targetResidentBytes int) []searchengine.SegmentID {
	m.resMu.Lock()
	defer m.resMu.Unlock()

	type res struct {
		id  searchengine.SegmentID
		seg residentSeg
	}
	ordered := make([]res, 0, len(m.resident))
	total := 0
	for id, seg := range m.resident {
		ordered = append(ordered, res{id: id, seg: seg})
		total += seg.bytes
	}
	// Lowest generation = oldest = evict first.
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].seg.generation < ordered[j].seg.generation
	})

	var unloaded []searchengine.SegmentID
	for _, r := range ordered {
		if total <= targetResidentBytes {
			break
		}
		m.engine.Unload([]searchengine.SegmentID{r.id})
		delete(m.resident, r.id)
		total -= r.seg.bytes
		unloaded = append(unloaded, r.id)
	}
	return unloaded
}
