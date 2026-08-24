// SPDX-License-Identifier: Apache-2.0

package contribhash

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// merge_test.go — the six subtests that pin MergeEdgesByIdentity's two rules:
// last copy wins every field EXCEPT Weight, which sums.

// implementsEdge builds an ID-addressed IMPLEMENTS row, the unweighted class.
func implementsEdge(to, method string) kgwire.BatchEdge {
	return kgwire.BatchEdge{
		FromIdx: -1,
		ToIdx:   -1,
		FromID:  "pkg/a.go:Spec",
		ToID:    to,
		Type:    kgtypes.EdgeImplements,
		Method:  method,
	}
}

// callEdge builds an ID-addressed CALLS row, the weighted class.
//
// IT TAKES NO `from` PARAMETER ON PURPOSE. `unparam` is NOT relaxed for _test.go
// in this repo's .golangci.yml, so a parameter that only ever receives one value
// is a lint failure; the caller is a constant inside instead.
func callEdge(to string, weight float64) kgwire.BatchEdge {
	return kgwire.BatchEdge{
		FromIdx: -1,
		ToIdx:   -1,
		FromID:  "pkg/a.go:Caller",
		ToID:    to,
		Type:    kgtypes.EdgeCalls,
		Method:  "typed-qualifier",
		Weight:  weight,
	}
}

// totalWeight is the conservation instrument: the sum the merge must not change.
func totalWeight(edges []kgwire.BatchEdge) float64 {
	var sum float64
	for _, e := range edges {
		sum += e.Weight
	}
	return sum
}

func TestMergeEdgesByIdentity(t *testing.T) {
	t.Run("keeps_last_at_first_position", func(t *testing.T) {
		in := []kgwire.BatchEdge{
			implementsEdge("pkg/b.go:Impl", "method-set:1"),
			implementsEdge("pkg/c.go:Other", "method-set:9"),
			implementsEdge("pkg/b.go:Impl", "method-set:2"),
		}

		got := MergeEdgesByIdentity(in)

		require.Len(t, got, 2)
		// The survivor sits where the FIRST copy sat — the determinism the
		// per-file hash depends on.
		require.Equal(t, "pkg/b.go:Impl", got[0].ToID)
		// ...carrying the LAST copy's fields, which is the winner the store's
		// own last-op-wins conflict resolution picks.
		require.Equal(t, "method-set:2", got[0].Method)
		// Control: a row with no rival is untouched.
		require.Equal(t, "method-set:9", got[1].Method)
	})

	t.Run("weights_sum_across_copies", func(t *testing.T) {
		in := []kgwire.BatchEdge{
			callEdge("pkg/g.go:Callee", 2),
			callEdge("pkg/h.go:Solo", 5),
			callEdge("pkg/g.go:Callee", 3),
		}

		got := MergeEdgesByIdentity(in)

		require.Len(t, got, 2)
		// 2 + 3, not the last copy's 3: the copies each hold a share of one
		// call count, so keeping one publishes fewer calls than the source makes.
		require.InDelta(t, 5.0, got[0].Weight, 0)
		// Control: an unduplicated row keeps its own weight, unsummed.
		require.InDelta(t, 5.0, got[1].Weight, 0)
		// Conservation is the property the whole-repo probe gates: the merge
		// moves weight between rows and creates or destroys none.
		require.InDelta(t, totalWeight(in), totalWeight(got), 0)

		// KNOWN-NEGATIVE for the no-allowlist rule: summing an unweighted class
		// cannot inflate it, because every non-CALLS collector edge type carries
		// Weight 0 by construction and 0+0 is 0.
		unweighted := MergeEdgesByIdentity([]kgwire.BatchEdge{
			implementsEdge("pkg/b.go:Impl", "method-set:1"),
			implementsEdge("pkg/b.go:Impl", "method-set:2"),
		})
		require.Len(t, unweighted, 1)
		require.InDelta(t, 0.0, unweighted[0].Weight, 0)
	})

	t.Run("distinct_identities_survive", func(t *testing.T) {
		// KNOWN-POSITIVE CONTROL FOR THE WHOLE FUNCTION. Five rows differing one
		// key part at a time. A merge that dropped everything, or one keyed on
		// too few fields, fails here and nowhere else.
		base := kgwire.BatchEdge{
			FromIdx: -1, ToIdx: -1,
			FromID:   "pkg/a.go:From",
			ToID:     "pkg/b.go:To",
			Type:     kgtypes.EdgeCalls,
			Evidence: "site:1",
		}
		differsFrom := base
		differsFrom.FromID = "pkg/z.go:From"
		differsTo := base
		differsTo.ToID = "pkg/z.go:To"
		differsType := base
		differsType.Type = kgtypes.EdgeUsesType
		differsEvidence := base
		differsEvidence.Evidence = "site:2"

		in := []kgwire.BatchEdge{base, differsFrom, differsTo, differsType, differsEvidence}

		got := MergeEdgesByIdentity(in)

		require.Len(t, got, len(in))
	})

	t.Run("index_addressed_not_collapsed", func(t *testing.T) {
		// A knowledge-graph collect names endpoints BY POSITION with both ID
		// fields empty. On the server's four string parts alone all of these
		// share a key, so a four-part merge would collapse a whole graph's
		// CONTAINS set to one row.
		byIndex := func(from, to int) kgwire.BatchEdge {
			return kgwire.BatchEdge{FromIdx: from, ToIdx: to, Type: kgtypes.EdgeContains}
		}
		in := []kgwire.BatchEdge{byIndex(0, 1), byIndex(0, 2), byIndex(1, 2)}

		got := MergeEdgesByIdentity(in)

		require.Len(t, got, 3)

		// ...and the indices are in the key rather than merely alongside it: a
		// genuine index-addressed duplicate still collapses.
		got = MergeEdgesByIdentity(append(in, byIndex(0, 1)))

		require.Len(t, got, 3)
	})

	t.Run("idempotent", func(t *testing.T) {
		// CHARACTERIZATION GUARD, GREEN BEFORE AND AFTER — not a red-first
		// assertion, and it must not be cited as proof of the summing rule. It
		// passes under deliberately-wrong merge variants too, because an
		// already-merged slice has no rivals left to resolve. What actually
		// forbids a second application doubling a call count is the exactly-once
		// assertion on the WriteResult call site. Kept for regression value.
		in := []kgwire.BatchEdge{
			callEdge("pkg/g.go:Callee", 2),
			callEdge("pkg/h.go:Solo", 5),
			callEdge("pkg/g.go:Callee", 3),
		}

		once := MergeEdgesByIdentity(in)
		twice := MergeEdgesByIdentity(once)

		require.Equal(t, once, twice)
	})

	t.Run("empty_input", func(t *testing.T) {
		require.Empty(t, MergeEdgesByIdentity(nil))
		require.Empty(t, MergeEdgesByIdentity([]kgwire.BatchEdge{}))
	})
}
