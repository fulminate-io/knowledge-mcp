// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestApplyPersonalityScalarsToRow_UnknownColumnIsNeutral pins what the
// trust-row reader DOES with the accessor's second return value. Nothing else
// covers that: the equivalence tests pin what Scalar RETURNS, and a reader that
// drops the ok-gate — multiplying an unknown column by the zero value instead of
// leaving it alone — is vet-clean and passes both package suites.
//
// The fixture is built so every wrong wiring lands on a DIFFERENT number.
// RowDefault["A"] is the value row A applies to its columns; it is NOT the value
// applied TO column A. So the A-to-B entry must land on 0.5, A's own default,
// and never on 0.25, B's. All four constants are binary-exact, so the assertions
// are exact equality rather than an epsilon comparison.
func TestApplyPersonalityScalarsToRow_UnknownColumnIsNeutral(t *testing.T) {
	profile := PersonalityProfile{
		RowDefault: map[string]float64{"A": 0.5, "B": 0.25, "C": 0.125},
		Deviations: map[string]map[string]float64{"A": {"C": 2.0}},
	}

	newMatrix := func() TrustMatrix {
		ids := []string{"t0", "t1", "t2", "t3", "t4"}
		idIndex := make(map[string]int, len(ids))
		row := make([]SparseEntry, 0, len(ids))
		for col, id := range ids {
			idIndex[id] = col
			row = append(row, SparseEntry{Col: col, Val: 1.0})
		}
		return TrustMatrix{IDs: ids, IDIndex: idIndex, Rows: [][]SparseEntry{row}}
	}

	t.Run("known_unknown_and_deviating_columns", func(t *testing.T) {
		matrix := newMatrix()
		thoughtToCluster := map[string]string{
			"t0": "A",     // the row's own thought
			"t1": "B",     // a known column
			"t2": "GHOST", // a persisted cluster_id this profile does not carry
			"t3": "C",     // a deviating column of row A
			"t4": "A",     // an entry in the row's own cluster
		}

		applyPersonalityScalarsToRow(matrix, 0, thoughtToCluster, profile)

		row := matrix.Rows[0]
		assert.InDelta(t, 1.0, row[0].Val, 0, "the self entry (j == i) is skipped")
		assert.InDelta(t, 0.5, row[1].Val, 0, "a known column is multiplied by the ROW's default (0.5), never by the column cluster's own default (0.25)")
		assert.InDelta(t, 1.0, row[2].Val, 0, "an unknown column is NEUTRAL: the reader must consume the accessor's ok and multiply by nothing")
		assert.InDelta(t, 2.0, row[3].Val, 0, "a deviating column takes its deviation, not the row default")
		assert.InDelta(t, 1.0, row[4].Val, 0, "an entry in the row's own cluster is skipped")
	})

	t.Run("row_absent_from_profile_is_a_no_op", func(t *testing.T) {
		matrix := newMatrix()
		thoughtToCluster := map[string]string{
			"t0": "GHOSTROW", // the profile carries no row for this cluster
			"t1": "B",
			"t2": "GHOST",
			"t3": "C",
			"t4": "A",
		}

		applyPersonalityScalarsToRow(matrix, 0, thoughtToCluster, profile)

		for k, entry := range matrix.Rows[0] {
			assert.InDelta(t, 1.0, entry.Val, 0, "entry %d is untouched when the row is absent from the profile", k)
		}
	})
}
