// SPDX-License-Identifier: Apache-2.0

package paging

import (
	"fmt"
	"slices"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// band_drain.go holds the from_id RANGE-BAND drain: the whole-graph edge read cut
// into half-open from_id ranges that each come back under the server's row ceiling.
// It is the sibling of browse_drain.go's pivot drain and deliberately not folded
// into it — DrainPivotEdges pages the caller's PIVOT SET and issues an ids[]-pivot
// plan, while this pages the from_id SPACE and issues a pivot-free one. A shared
// function would need a mode flag that changes the wire shape, which is a different
// operation wearing one name. What IS shared is the four-part dedup key and the
// saturation-halving idiom.

// EdgeBandCount is the band count the reflection pass tiles the id space into.
//
// THE NUMBER IS MEASURED, and the trade is recorded because a bare constant invites
// drift. At production cardinality (256,088 edges) 16 bands put at most 19,065 rows
// in a band — 2.6x under the server's 50,000-row ceiling — so the corpus must roughly
// TRIPLE before the halving path ever fires. 8 bands measured BETTER on both axes
// (9,664 buffers against 12,638 for the widest read, 8 statements against 16) but
// leaves only 1.5x of headroom, which is a 52% corpus growth away from halving on
// every pass. 16 is chosen so halving stays a GUARD rather than a running cost. If a
// later measurement says otherwise, this constant is the single place to change.
const EdgeBandCount = 16

// EdgeBandBoundaries returns the n-1 INTERIOR boundaries that cut the id space into
// n half-open bands: band 0 is ["", b0), band i is [b(i-1), bi), and band n-1 is
// [b(n-2), ""). The two outer bands are open-ended on purpose, so the tiling covers
// ids sorting below the smallest boundary and above the largest.
//
// The boundaries are QUANTILES OF THE CALLER'S OWN ID LIST rather than cuts in a
// fixed alphabet. That is what makes this work on any graph: knowledge node ids are
// 32-char hex, but code-graph node ids are file paths, and a hex partition would be
// wrong there. It is also free — every caller already holds the list.
//
// It sorts a COPY and never the caller's slice. Callers hold memo-owned slices whose
// aliasing is load-bearing (the reflection loop set-diffs the next tick against the
// adjacency it was handed), so reordering one in place would corrupt a later pass.
//
// DUPLICATE BOUNDARIES ARE POSSIBLE AND ARE CORRECT. When n-1 exceeds len(ids) the
// quantile indices collide and the same id is emitted as two adjacent boundaries,
// producing a band [b, b) that is EMPTY under the half-open rule. An empty band
// contributes nothing and the tiling stays exact. This is said explicitly because the
// natural reading of "n bands" is that all n are non-empty, and a future reader who
// "fixes" the duplicates by widening a bound would break the tiling.
//
// DEGENERATE CASE: len(ids) == 0 (or n <= 1) yields NO boundaries, hence a single
// unbounded band — the whole-graph read. Note that DrainBandedEdges REFUSES that
// input; see its own doc for why fewer than two bands cannot be guarded.
func EdgeBandBoundaries(ids []string, n int) []string {
	if len(ids) == 0 || n <= 1 {
		return nil
	}
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	out := make([]string, 0, n-1)
	for i := 1; i < n; i++ {
		// i*len/n is strictly below len for every i <= n-1 under integer division,
		// so the index is always in range.
		out = append(out, sorted[i*len(sorted)/n])
	}
	return out
}

// EdgeFromBandOrNil builds the plan's band field, and returns NIL when both bounds
// are empty. The nil is the point: the server refuses a plan carrying a non-nil
// edge_from_band alongside two or more pivots (bootstrap/engine_edges.go), and an
// ordinary chunk-loop page carries up to EdgePivotPageSize pivots, so a caller that
// set the field unconditionally would turn every ordinary page into an
// InvalidArgument. Two empty bounds look harmless and are not.
//
// EVERY pivot-drain closure builds its plan's band through THIS function rather than
// composing the message inline, so the nil rule is stated once and cannot be got
// wrong per site.
func EdgeFromBandOrNil(fromIDGte, fromIDLt string) *knowledgev1.EdgeFromBand {
	if fromIDGte == "" && fromIDLt == "" {
		return nil
	}
	return &knowledgev1.EdgeFromBand{FromIdGte: fromIDGte, FromIdLt: fromIDLt}
}

// bandFetchFn is the widened page-fetch closure both band walks drive. idPage is the
// pivot set the page names — EMPTY for the pivot-free match-all arm, a single pivot
// for the escape — and the two bounds are the half-open band. The bool is the
// response's truncated flag.
type bandFetchFn func(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error)

// drainBandTiling walks a half-open band tiling into the shared union, one drainBand
// call per band. It is the loop BOTH band arms share — DrainBandedEdges' whole-graph
// tiling and drainPivotPage's per-pivot escape — extracted so the two cannot drift on
// the boundary arithmetic.
//
// The tiling is walked as a run of UPPER bounds — the interior boundaries followed by
// one empty bound — carrying each band's lower bound over from the previous iteration.
// Band 0 is ["", b0) because lo starts empty, and the final band is [b(n-2), "")
// because the appended bound is empty, so n-1 boundaries yield exactly n bands with no
// index arithmetic to get wrong.
func drainBandTiling(
	pivots []string,
	splitPoints []string,
	boundaries []string,
	edgeCap int,
	fetchPage bandFetchFn,
	seen map[edgeDedupKey]bool,
	out *[]knowledgev1.Edge,
) error {
	lo := ""
	for _, hi := range append(slices.Clone(boundaries), "") {
		if err := drainBand(pivots, splitPoints, lo, hi, edgeCap, fetchPage, seen, out); err != nil {
			return err
		}
		lo = hi
	}
	return nil
}

// DrainBandedEdges reads the WHOLE edge set in bounded from_id bands and returns one
// deduped union. boundaries defines the tiling (band count is len(boundaries)+1) and
// ids supplies the SPLIT POINTS used when a band saturates. Neither is redundant: a
// caller passing only boundaries could tile but could not halve.
//
// ITS EXPORTED CLOSURE SHAPE IS DELIBERATELY UNCHANGED — this arm is pivot-free by
// construction, so a pivot parameter would be noise at every one of its call sites.
// It adapts that closure into the widened bandFetchFn internally, supplying no pivots.
//
// fetchPage is called once per band with that band's half-open [fromIDGte, fromIDLt)
// bounds, an empty string meaning unbounded on that end. Its third return is the
// response's truncated flag.
//
// FEWER THAN TWO BANDS IS AN ERROR, not a degenerate pass-through. Band 0's lower
// bound is vacuous by construction, so with a single band there is no bound for the
// out-of-band guard below to catch a server on — the guard's determinism claim would
// be false and the drain would silently accept a whole-graph answer as a band.
//
// A SATURATING BAND IS NEVER ACCEPTED. Both saturation signals are used, and they are
// not redundant: the len >= edgeCap test is the mechanism the pivot drain has always
// used and works against any server, while the response's truncated flag is the one
// that actually fires in the dangerous case — the server drops rows between its scan
// and the count it returns, so a saturated scan can come back SHORT and the len test
// alone would be blind to it. A saturating band splits at the median of the caller's
// ids strictly inside it and both halves are retried. A short union is never returned.
//
// SERIAL ON PURPOSE, and this is a stated trade rather than an oversight. Unlike the
// keyset drain in this package the bands are INDEPENDENT — band N+1's request does not
// depend on band N's response — so this drain COULD run in parallel. The objective is
// to reduce the database's CPU and buffer consumption, and issuing every band
// concurrently against one instance concentrates the identical work into a burst
// rather than reducing it. The caller (an hourly background pass) has no latency
// budget pressure. A parallel version would need a mutex on the shared dedup map.
func DrainBandedEdges(
	ids []string,
	boundaries []string,
	edgeCap int,
	fetchPage func(fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error),
) ([]knowledgev1.Edge, error) {
	if len(boundaries) < 1 {
		return nil, fmt.Errorf(
			"paging: DrainBandedEdges needs at least 2 bands, got %d boundaries (%d band); "+
				"band 0 has no lower bound, so a single band cannot catch a server that ignores the range",
			len(boundaries), len(boundaries)+1)
	}
	splitPoints := slices.Clone(ids)
	slices.Sort(splitPoints)
	seen := make(map[edgeDedupKey]bool, len(ids))
	out := make([]knowledgev1.Edge, 0, len(ids))
	// The adapter: this arm names no pivots, so it discards the widened closure's
	// pivot argument. Passing nil pivots below is what makes that correct rather than
	// lossy — there is no pivot set to drop.
	widened := func(_ []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
		return fetchPage(fromIDGte, fromIDLt)
	}
	if err := drainBandTiling(nil, splitPoints, boundaries, edgeCap, widened, seen, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// drainPivotByBands is the ESCAPE drainPivotPage takes when a page holding exactly ONE
// pivot comes back saturated: instead of aborting, it re-reads that pivot as a tiling
// of half-open from_id bands and unions the pieces. It shares drainBandTiling and
// drainBand with the whole-graph arm, so the two cannot drift on the boundary rule or
// on the out-of-band guard — the parts that matter.
//
// WHAT IT CAN AND CANNOT DIVIDE. A from_id band subdivides the pivot's INCOMING edges,
// whose from_ids are drawn from across the graph. It cannot divide the OUTGOING ones:
// every edge leaving the pivot carries the pivot's own id as from_id, so they all land
// in whichever single band contains that id. A pivot saturated by out-degree alone is
// therefore genuinely unsplittable, every band containing its id holds no interior id
// to split on, and the error below is the correct outcome rather than a shortfall.
//
// THE ERROR NAMES THE PIVOT AND THE BAND. The pivot half preserves the wording the
// unconditional abort used before this escape existed, because that is what callers
// and their tests recognize; the band half arrives wrapped from drainBand. Both the
// unsplittable case and the out-of-band guard surface through here, and they stay
// distinguishable by their inner text.
//
// THE TILING ALWAYS HAS AT LEAST TWO BANDS, which is what gives the out-of-band guard
// a lower bound to catch a band-ignoring server on. That is an argument, not an
// assumption: DrainPivotEdges returns early on an empty id set, so ids is non-empty
// here, and EdgeBandBoundaries then yields EdgeBandCount-1 boundaries. Note the
// efficacy limit that follows from the same source — a caller draining a ONE-element
// id set gets duplicate boundaries and exactly two non-empty bands, so it gains a
// two-way split rather than a general escape.
func drainPivotByBands(
	pivot string,
	ids []string,
	edgeCap int,
	fetchPage bandFetchFn,
	seen map[edgeDedupKey]bool,
	out *[]knowledgev1.Edge,
) error {
	splitPoints := slices.Clone(ids)
	slices.Sort(splitPoints)
	boundaries := EdgeBandBoundaries(ids, EdgeBandCount)
	if err := drainBandTiling([]string{pivot}, splitPoints, boundaries, edgeCap, fetchPage, seen, out); err != nil {
		return fmt.Errorf(
			"paging: pivot %q alone returns at least %d edges, the per-page ceiling, and band-splitting it "+
				"did not divide its edge set — it cannot be read completely by a pivot drain: %w",
			pivot, edgeCap, err)
	}
	return nil
}

// drainBand reads ONE band into the shared union, splitting and retrying when the
// band comes back saturated.
//
// pivots is the page's pivot set, forwarded verbatim to fetchPage: EMPTY for the
// whole-graph arm, exactly one id for drainPivotPage's escape. It is the FIRST
// parameter because it identifies WHAT is being read, where the bounds only narrow it.
func drainBand(
	pivots []string,
	splitPoints []string,
	lo, hi string,
	edgeCap int,
	fetchPage bandFetchFn,
	seen map[edgeDedupKey]bool,
	out *[]knowledgev1.Edge,
) error {
	edges, truncated, err := fetchPage(pivots, lo, hi)
	if err != nil {
		return err
	}

	// THE OUT-OF-BAND GUARD RUNS FIRST, before saturation and before dedup, because
	// it is the check that makes a version-skewed deploy LOUD. A server built before
	// the range field exists ignores it and answers every band with the whole graph;
	// the union would still be correct after dedup, so nothing else here would ever
	// notice — until the graph outgrew the row ceiling and each band started
	// returning an arbitrary sample instead. From band 1 onward such a server returns
	// rows below lo with certainty, so this fires deterministically rather than
	// probabilistically.
	for i := range edges {
		if !inBand(edges[i].FromId, lo, hi) {
			return fmt.Errorf(
				"paging: band [%q,%q) returned an edge with from_id %q, outside the requested range — "+
					"the server ignored the from_id band (a server older than the field answers every band with the whole graph)",
				lo, hi, edges[i].FromId)
		}
	}

	if (edgeCap > 0 && len(edges) >= edgeCap) || truncated {
		split, ok := medianInside(splitPoints, lo, hi)
		if !ok {
			return fmt.Errorf(
				"paging: band [%q,%q) saturates at the %d-edge ceiling and holds no interior id to split on — "+
					"its edge set cannot be read completely by a banded drain",
				lo, hi, edgeCap)
		}
		// The split is strictly inside the band, so both halves are strictly
		// narrower AND the split id itself leaves both halves' candidate sets —
		// it is excluded from [lo,split) for being too high and from [split,hi)
		// for being the lower bound. The candidate set therefore shrinks by at
		// least one per level, which is what terminates the recursion.
		if err := drainBand(pivots, splitPoints, lo, split, edgeCap, fetchPage, seen, out); err != nil {
			return err
		}
		return drainBand(pivots, splitPoints, split, hi, edgeCap, fetchPage, seen, out)
	}

	for i := range edges {
		e := &edges[i]
		key := edgeDedupKey{fromID: e.FromId, toID: e.ToId, edgeType: e.Type, evidence: e.Evidence}
		if seen[key] {
			continue
		}
		seen[key] = true
		// Built field-by-field into append: knowledgev1.Edge embeds a proto
		// MessageState, so returning or copying one by value trips copylocks.
		*out = append(*out, knowledgev1.Edge{
			FromId:        e.FromId,
			ToId:          e.ToId,
			Type:          e.Type,
			Weight:        e.Weight,
			Confidence:    e.Confidence,
			Method:        e.Method,
			Evidence:      e.Evidence,
			LastValidated: e.LastValidated,
		})
	}
	return nil
}

// inBand reports whether fromID falls in the half-open range [lo, hi). An empty bound
// is unbounded on that end.
func inBand(fromID, lo, hi string) bool {
	if lo != "" && fromID < lo {
		return false
	}
	if hi != "" && fromID >= hi {
		return false
	}
	return true
}

// medianInside returns the median of the sorted ids lying STRICTLY inside (lo, hi),
// which is the split point for a saturating band. Strictly: an id equal to lo would
// produce an empty lower half and an id equal to hi no progress at all, so neither is
// a usable split. Reports false when the band holds no interior id, which is the
// unsplittable case the caller turns into an error rather than a short union.
func medianInside(sorted []string, lo, hi string) (string, bool) {
	start := 0
	if lo != "" {
		// BinarySearch returns the LEFTMOST insertion point, so advancing past any
		// run of ids equal to lo lands on the first id strictly greater than it.
		start, _ = slices.BinarySearch(sorted, lo)
		for start < len(sorted) && sorted[start] == lo {
			start++
		}
	}
	end := len(sorted)
	if hi != "" {
		end, _ = slices.BinarySearch(sorted, hi)
	}
	if start >= end {
		return "", false
	}
	return sorted[start+(end-start)/2], true
}
