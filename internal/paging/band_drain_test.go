// SPDX-License-Identifier: Apache-2.0

package paging

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// bandCorpus builds n zero-padded ids so string order and numeric order agree — the
// band predicate is defined over string ordering, so the fixture must not smuggle a
// different one in.
func bandCorpus(n int) []string {
	ids := make([]string, 0, n)
	for i := range n {
		ids = append(ids, fmt.Sprintf("id-%04d", i))
	}
	return ids
}

// bandEdge builds one edge sourced at from. The four-part identity varies with `ev`
// so a fixture can put two MEMBERSHIPS under one triple.
func bandEdge(from, to, ev string) knowledgev1.Edge {
	return knowledgev1.Edge{FromId: from, ToId: to, Type: "relates-to", Evidence: ev}
}

// bandServer is a fake server that answers a band with exactly the corpus edges whose
// source falls inside it — the shape a CORRECT server has. Records every band it was
// asked for so a test can assert the tiling that was actually walked.
type bandServer struct {
	edges []knowledgev1.Edge
	asked [][2]string
}

func (s *bandServer) fetch(lo, hi string) ([]knowledgev1.Edge, bool, error) {
	s.asked = append(s.asked, [2]string{lo, hi})
	var out []knowledgev1.Edge
	for i := range s.edges {
		if inBand(s.edges[i].FromId, lo, hi) {
			out = append(out, bandEdge(s.edges[i].FromId, s.edges[i].ToId, s.edges[i].Evidence))
		}
	}
	return out, false, nil
}

// bandEdgeSet renders a union as a comparable four-part key set.
func bandEdgeSet(es []knowledgev1.Edge) map[string]int {
	out := map[string]int{}
	for i := range es {
		out[strings.Join([]string{es[i].FromId, es[i].ToId, es[i].Type, es[i].Evidence}, "|")]++
	}
	return out
}

func TestEdgeBandBoundaries(t *testing.T) {
	t.Run("boundaries are ascending and cut the id space", func(t *testing.T) {
		got := EdgeBandBoundaries(bandCorpus(64), 4)
		require.Len(t, got, 3, "n bands need n-1 interior boundaries")
		assert.Equal(t, []string{"id-0016", "id-0032", "id-0048"}, got)
	})

	t.Run("the caller's slice is never reordered", func(t *testing.T) {
		// The reflection loop set-diffs the next tick against the very slice it
		// hands in, so an in-place sort here corrupts a later pass.
		ids := []string{"c", "a", "b"}
		_ = EdgeBandBoundaries(ids, 2)
		assert.Equal(t, []string{"c", "a", "b"}, ids, "the input slice must not be sorted in place")
	})

	t.Run("more bands than ids yields duplicate boundaries, which are empty bands", func(t *testing.T) {
		got := EdgeBandBoundaries([]string{"a", "b"}, 8)
		require.Len(t, got, 7)
		// Duplicates are CORRECT: [b, b) is empty under the half-open rule and
		// contributes nothing, so the tiling stays exact. The quantile index is
		// i*len/n, so i=1..3 land on "a" and i=4..7 on "b".
		assert.Equal(t, []string{"a", "a", "a", "b", "b", "b", "b"}, got)
		for i := 1; i < len(got); i++ {
			assert.LessOrEqual(t, got[i-1], got[i], "boundaries must never descend")
		}
	})

	t.Run("degenerate inputs yield one unbounded band", func(t *testing.T) {
		assert.Empty(t, EdgeBandBoundaries(nil, 16), "no ids means no boundaries")
		assert.Empty(t, EdgeBandBoundaries(bandCorpus(8), 1), "one band has no interior boundary")
	})
}

func TestDrainBandedEdges(t *testing.T) {
	t.Run("rejects_fewer_than_two_bands", func(t *testing.T) {
		// Band 0's lower bound is vacuous by construction, so a single band leaves
		// the out-of-band guard with nothing to catch a range-ignoring server on.
		called := false
		_, err := DrainBandedEdges(bandCorpus(8), nil, 10,
			func(_, _ string) ([]knowledgev1.Edge, bool, error) {
				called = true
				return nil, false, nil
			})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least 2 bands")
		assert.False(t, called, "the drain must refuse before issuing any request")

		// KNOWN-POSITIVE CONTROL: one interior boundary is two bands and IS accepted,
		// so the guard rejects the degenerate case rather than everything.
		_, okErr := DrainBandedEdges(bandCorpus(8), []string{"id-0004"}, 10,
			func(_, _ string) ([]knowledgev1.Edge, bool, error) { return nil, false, nil })
		require.NoError(t, okErr, "exactly two bands is the minimum ACCEPTED tiling")
	})

	t.Run("unions_every_band_completely", func(t *testing.T) {
		ids := bandCorpus(64)
		srv := &bandServer{}
		for _, id := range ids {
			// Two MEMBERSHIPS of one triple, differing only in the group key, so a
			// three-part dedup would collapse them and come up short.
			srv.edges = append(srv.edges, bandEdge(id, "t", "ev-1"), bandEdge(id, "t", "ev-2"))
		}
		boundaries := EdgeBandBoundaries(ids, EdgeBandCount)
		require.Len(t, boundaries, EdgeBandCount-1)

		got, err := DrainBandedEdges(ids, boundaries, 1000, srv.fetch)
		require.NoError(t, err)

		// Against a FIXTURE-DERIVED constant, not against the fake's own count: two
		// counts derived the same way agree even when both are wrong.
		assert.Len(t, got, 128, "64 sources x 2 memberships, every one of them")
		assert.Equal(t, bandEdgeSet(srv.edges), bandEdgeSet(got), "the union is the whole corpus, exactly")

		require.Len(t, srv.asked, EdgeBandCount, "every band is walked once, with no halving")
		assert.Empty(t, srv.asked[0][0], "the first band is open below")
		assert.Empty(t, srv.asked[EdgeBandCount-1][1], "the last band is open above")
	})

	t.Run("halves_a_saturating_band", func(t *testing.T) {
		ids := bandCorpus(64)
		boundaries := []string{"id-0032"}
		// The lower band saturates on its FIRST ask by the LEN signal alone
		// (truncated stays false, so this sub-test isolates that detector), and the
		// halves then answer honestly. The drain must split and retry rather than
		// accept the saturated page — and must still come back with the WHOLE corpus.
		const edgeCap = 4
		srv := &bandServer{}
		for _, id := range ids {
			srv.edges = append(srv.edges, bandEdge(id, "t", ""))
		}
		saturateOnce := true
		var asked [][2]string
		got, err := DrainBandedEdges(ids, boundaries, edgeCap,
			func(lo, hi string) ([]knowledgev1.Edge, bool, error) {
				asked = append(asked, [2]string{lo, hi})
				if saturateOnce && lo == "" && hi == "id-0032" {
					saturateOnce = false
					out := make([]knowledgev1.Edge, 0, edgeCap)
					for i := range edgeCap {
						out = append(out, bandEdge(fmt.Sprintf("id-%04d", i), "t", ""))
					}
					return out, false, nil
				}
				return srv.fetch(lo, hi)
			})
		require.NoError(t, err)

		require.GreaterOrEqual(t, len(asked), 2)
		assert.Equal(t, [2]string{"", "id-0032"}, asked[0], "the saturating band is asked first")
		assert.Empty(t, asked[1][0], "its lower half keeps the open lower bound")
		assert.Equal(t, "id-0016", asked[1][1],
			"the split point is the MEDIAN of the caller's ids strictly inside the band")
		// HALVING MUST NOT LOSE ROWS — the saturated page returned only 4 of the 32
		// edges in that band, so a drain that accepted it would come back short.
		assert.Len(t, got, 64, "the union after halving is still the whole corpus")
	})

	t.Run("halves_a_saturating_band_reported_only_by_truncated", func(t *testing.T) {
		// THE SIGNAL THAT MATTERS. The server drops rows between its scan and the
		// count it returns, so a saturated band can come back SHORT — here one edge
		// against a cap of 4. The len test is blind to that; only the truncated flag
		// sees it, which is why both signals are wired.
		ids := bandCorpus(64)
		var asked [][2]string
		_, _ = DrainBandedEdges(ids, []string{"id-0032"}, 4,
			func(lo, hi string) ([]knowledgev1.Edge, bool, error) {
				asked = append(asked, [2]string{lo, hi})
				if lo == "" && hi == "id-0032" {
					return []knowledgev1.Edge{bandEdge("id-0000", "t", "")}, true, nil
				}
				return nil, false, nil
			})
		require.GreaterOrEqual(t, len(asked), 2,
			"a short page carrying truncated must still split — the len test cannot see this cut")
		assert.Equal(t, "id-0016", asked[1][1], "the split point is the median of the interior ids")
	})

	t.Run("errors_when_a_saturating_band_cannot_be_split", func(t *testing.T) {
		// A band holding NO interior id has no split point, so there is no smaller
		// request to make. The drain must say so rather than return a short union.
		//
		// THIS IS NOT A CONTRIVED SHAPE. The boundaries come from the CALLER'S id
		// list, but the edges come from the whole graph, so a band can hold far more
		// edges than the caller has ids inside it — here the caller holds no id below
		// "id-0032" at all, yet the band is full of edges sourced there.
		ids := []string{"id-0032", "id-0064"}
		_, err := DrainBandedEdges(ids, []string{"id-0032"}, 2,
			func(lo, hi string) ([]knowledgev1.Edge, bool, error) {
				if lo == "" {
					return []knowledgev1.Edge{
						bandEdge("id-0001", "t", "a"), bandEdge("id-0001", "t", "b"),
					}, false, nil
				}
				return nil, false, nil
			})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no interior id to split on")
		assert.Contains(t, err.Error(), "cannot be read completely")

		// KNOWN-POSITIVE CONTROL: give the caller ONE id inside that band and the
		// same saturating answer splits instead of erroring, so the error above is
		// the absence of a split point and not the saturation itself.
		splittable := []string{"id-0001", "id-0032", "id-0064"}
		var asked [][2]string
		_, splitErr := DrainBandedEdges(splittable, []string{"id-0032"}, 2,
			func(lo, hi string) ([]knowledgev1.Edge, bool, error) {
				asked = append(asked, [2]string{lo, hi})
				if lo == "" && hi == "id-0032" {
					return []knowledgev1.Edge{
						bandEdge("id-0001", "t", "a"), bandEdge("id-0001", "t", "b"),
					}, false, nil
				}
				return nil, false, nil
			})
		require.NoError(t, splitErr)
		require.GreaterOrEqual(t, len(asked), 2)
		assert.Equal(t, "id-0001", asked[1][1], "with an interior id present the band splits on it")
	})

	t.Run("rejects_a_row_outside_the_requested_band", func(t *testing.T) {
		// The version-skew guard: a server built before the range field ignores it
		// and answers every band with the whole graph. Dedup would keep the union
		// correct, so nothing else in this drain would ever notice.
		ids := bandCorpus(64)
		boundaries := EdgeBandBoundaries(ids, 4)
		all := make([]knowledgev1.Edge, 0, len(ids))
		for _, id := range ids {
			all = append(all, bandEdge(id, "t", ""))
		}
		_, err := DrainBandedEdges(ids, boundaries, 1000,
			func(_, _ string) ([]knowledgev1.Edge, bool, error) {
				return all, false, nil // ignores the band entirely
			})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside the requested range")
		assert.Contains(t, err.Error(), "the server ignored the from_id band")

		// KNOWN-POSITIVE CONTROL: the SAME corpus and tiling through a band-honoring
		// server succeeds. Without it, a drain that errored unconditionally would
		// satisfy the assertion above.
		srv := &bandServer{edges: all}
		got, okErr := DrainBandedEdges(ids, boundaries, 1000, srv.fetch)
		require.NoError(t, okErr)
		assert.Len(t, got, len(ids), "a band-honoring server drains the whole corpus cleanly")
	})
}

// pivotBandServer is the scripted backend for the PIVOT band-split escape. It holds
// every edge incident to a pivot in either direction and answers a page with those
// whose pivot is named AND whose from_id falls inside the requested band — the shape
// a correct server has once the band reaches a single-pivot read.
//
// It records every request so a test can assert that banded requests were actually
// issued; without that, a drain that never banded would satisfy a completeness
// assertion by simply accepting the unbanded page.
type pivotBandServer struct {
	edges []knowledgev1.Edge
	asked [][3]string
	// ignoreBand models the version-skewed deploy: a server older than the field
	// answers every band with the pivot's whole edge set.
	ignoreBand bool
	// truncateUnbanded sets the response's truncated flag on the UNBANDED page only,
	// modeling a server that dropped rows between its scan and its count. It is what
	// lets a sub-test drive saturation WITHOUT reaching the len >= edgeCap test.
	truncateUnbanded bool
}

func (s *pivotBandServer) fetch(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
	s.asked = append(s.asked, [3]string{strings.Join(idPage, "+"), fromIDGte, fromIDLt})
	var out []knowledgev1.Edge
	for _, p := range idPage {
		for i := range s.edges {
			e := &s.edges[i]
			if e.FromId != p && e.ToId != p {
				continue
			}
			if !s.ignoreBand && !inBand(e.FromId, fromIDGte, fromIDLt) {
				continue
			}
			out = append(out, bandEdge(e.FromId, e.ToId, e.Evidence))
		}
	}
	truncated := s.truncateUnbanded && fromIDGte == "" && fromIDLt == ""
	return out, truncated, nil
}

// bandedAsks counts the requests that carried a band, which is the direct evidence
// that the escape fired rather than the page being accepted whole.
func (s *pivotBandServer) bandedAsks() int {
	n := 0
	for _, a := range s.asked {
		if a[1] != "" || a[2] != "" {
			n++
		}
	}
	return n
}

// TestDrainPivotEdges_BandSplit covers the escape drainPivotPage takes when ONE pivot
// saturates its page: the pivot is re-read as a tiling of half-open from_id bands
// instead of aborting, and the abort survives for the case no band can divide.
func TestDrainPivotEdges_BandSplit(t *testing.T) {
	// The pivot sits in the MIDDLE of the id space so bands exist on both sides of
	// it, and the ids are the drain's own argument — which is also the population
	// EdgeBandBoundaries quantiles, exactly as production does it.
	ids := bandCorpus(16)
	const pivot = "id-0008"
	incoming := func() []knowledgev1.Edge {
		var es []knowledgev1.Edge
		for _, src := range ids {
			es = append(es, bandEdge(src, pivot, ""))
		}
		return es
	}

	t.Run("splits_a_saturated_pivot_into_bands", func(t *testing.T) {
		// edgeCap equals the pivot's whole incoming set, so the unbanded page is
		// saturated by the drain's own len >= edgeCap test and the escape must fire.
		srv := &pivotBandServer{edges: incoming()}
		got, err := DrainPivotEdges(ids, 1, len(ids), srv.fetch)
		require.NoError(t, err, "a saturated pivot whose bands come in under the cap must drain, not abort")
		assert.Len(t, got, len(ids), "the banded reads union back into the COMPLETE incoming set")
		assert.Greater(t, srv.bandedAsks(), 1,
			"more than one BANDED request must have been issued — a drain that never banded "+
				"would satisfy the completeness assertion by accepting the unbanded page")
	})

	t.Run("band_split_union_equals_unbanded", func(t *testing.T) {
		// THE LOSSLESSNESS ASSERTION. Two reads of the SAME fixture: one forced down
		// the band-split path by a tight cap, one served whole by a slack cap.
		split := &pivotBandServer{edges: incoming()}
		splitGot, err := DrainPivotEdges(ids, 1, len(ids), split.fetch)
		require.NoError(t, err)
		require.Positive(t, split.bandedAsks(), "precondition: this arm must actually have banded")

		whole := &pivotBandServer{edges: incoming()}
		wholeGot, err := DrainPivotEdges(ids, 1, 0, whole.fetch)
		require.NoError(t, err)
		require.Zero(t, whole.bandedAsks(), "precondition: this arm must NOT have banded")

		// Cardinality against a FIXTURE-DERIVED constant first: two sets that lost the
		// same members compare equal to each other, so set equality alone is not proof.
		require.Len(t, wholeGot, len(ids), "control: the unbanded read sees every seeded edge")

		splitSet, wholeSet := bandEdgeSet(splitGot), bandEdgeSet(wholeGot)
		// BOTH DIRECTIONS. A missing key is an edge the band split DROPPED; an extra
		// key is one it returned that the whole read does not carry.
		for k := range wholeSet {
			assert.Contains(t, splitSet, k, "the band split DROPPED an edge the unbanded read returned")
		}
		for k := range splitSet {
			assert.Contains(t, wholeSet, k, "the band split returned an edge the unbanded read does not carry")
		}
		assert.Equal(t, wholeSet, splitSet, "band-split and unbanded reads must be the same multiset")
	})

	t.Run("errors_when_the_pivot_has_no_splittable_from_id", func(t *testing.T) {
		// THE OUTGOING-HEAVY CASE: every edge leaves the pivot, so every from_id IS
		// the pivot and no from_id boundary can divide them. THE ERROR'S SURVIVAL IS
		// THE ASSERTION — this is the case the ticket requires stay loud.
		var outgoing []knowledgev1.Edge
		for i := range 4 {
			outgoing = append(outgoing, bandEdge(pivot, fmt.Sprintf("t-%d", i), ""))
		}
		srv := &pivotBandServer{edges: outgoing}
		got, err := DrainPivotEdges(ids, 1, len(outgoing), srv.fetch)
		require.Error(t, err, "a pivot no band can divide must abort rather than return a short union")
		assert.Nil(t, got, "no partial union is handed back alongside the error")
		assert.Contains(t, err.Error(), pivot, "the error names the pivot the caller must deal with")
		assert.Contains(t, err.Error(), "no interior id to split on",
			"and names WHY banding failed, so the caller can tell it from a transport error")
		assert.Positive(t, srv.bandedAsks(),
			"banding must have been ATTEMPTED — an abort that never tried is the old behaviour, not this one")
	})

	t.Run("rejects_a_row_outside_the_requested_band", func(t *testing.T) {
		// The version-skewed deploy: a server that ignores the band answers every band
		// with the whole set. Without the guard the split would recurse to exhaustion
		// and report "cannot split" for a pivot that splits fine — a true statement
		// about the wrong subject.
		srv := &pivotBandServer{edges: incoming(), ignoreBand: true}
		_, err := DrainPivotEdges(ids, 1, len(ids), srv.fetch)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside the requested range")
		assert.Contains(t, err.Error(), "the server ignored the from_id band")
		assert.NotContains(t, err.Error(), "no interior id to split on",
			"the guard must fire FIRST — a recursion-exhaustion error here would name the wrong cause")
	})

	t.Run("splits_on_the_response_truncated_flag_alone", func(t *testing.T) {
		// THE ONLY CATCHER FOR THE TRUNCATED THREADING. edgeCap is far above the row
		// count, so the drain's len >= edgeCap test CANNOT fire; the only signal that
		// the unbanded page was cut is the response flag. A closure returning a
		// hardcoded false passes every other sub-test in this file and ships the
		// plumbing inert — this one goes red.
		srv := &pivotBandServer{edges: incoming(), truncateUnbanded: true}
		got, err := DrainPivotEdges(ids, 1, 10*len(ids), srv.fetch)
		require.NoError(t, err, "the bands come back unflagged and under the cap, so the drain completes")
		assert.Len(t, got, len(ids), "and the union is still complete")
		assert.Greater(t, srv.bandedAsks(), 1,
			"the truncated flag ALONE must have driven the split — with it dropped, the page is "+
				"under the cap and unflagged, and zero banded requests would be issued")
	})
}

// TestEdgeFromBandOrNil pins the constructor every pivot-drain closure builds its
// band through.
func TestEdgeFromBandOrNil(t *testing.T) {
	t.Run("both_bounds_empty_yields_no_band", func(t *testing.T) {
		// THE CATCHER FOR THE SERVER'S MULTI-PIVOT REJECTION. The server refuses a
		// non-nil edge_from_band alongside two or more pivots, and an ordinary
		// chunk-loop page carries up to EdgePivotPageSize of them — so a constructor
		// that always returned a value would turn every ordinary page of every caller
		// into an InvalidArgument. No band-split sub-test would notice, because those
		// all run with a band actually set.
		assert.Nil(t, EdgeFromBandOrNil("", ""),
			"an unbanded page must carry NO band field, not an empty one")
	})

	t.Run("either_bound_set_yields_the_band", func(t *testing.T) {
		// The first and last bands of any tiling are open-ended, so the one-sided
		// forms are the common case rather than edge cases.
		lower := EdgeFromBandOrNil("id-0003", "")
		require.NotNil(t, lower, "a lower-only band is still a band")
		assert.Equal(t, "id-0003", lower.GetFromIdGte())
		assert.Empty(t, lower.GetFromIdLt(), "unbounded above")

		upper := EdgeFromBandOrNil("", "id-0007")
		require.NotNil(t, upper, "an upper-only band is still a band")
		assert.Empty(t, upper.GetFromIdGte(), "unbounded below")
		assert.Equal(t, "id-0007", upper.GetFromIdLt())

		both := EdgeFromBandOrNil("id-0003", "id-0007")
		require.NotNil(t, both)
		assert.Equal(t, "id-0003", both.GetFromIdGte())
		assert.Equal(t, "id-0007", both.GetFromIdLt())
	})
}
