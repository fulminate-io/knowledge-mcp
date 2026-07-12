// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// focalDiagonal returns the (already row-normalized) self-trust diagonal entry —
// the SparseEntry whose Col equals the focal thought "F"'s own row index — in a
// built TrustMatrix.
func focalDiagonal(t *testing.T, m TrustMatrix) float64 {
	t.Helper()
	i, ok := m.IDIndex["F"]
	require.True(t, ok, "focal thought F present in matrix")
	for _, e := range m.Rows[i] {
		if e.Col == i {
			return e.Val
		}
	}
	t.Fatalf("no self-diagonal entry for F")
	return 0
}

// TestMatrixRecency_DiagonalMatchesFold (assertion a) proves `now` is threaded
// through the matrix-builder chain to the SelfTrust diagonal at fillSparseRows: at
// a FIXED injected now T (≠ wall-clock), the focal thought's normalized self-diagonal
// equals the Phase-1 fold SelfTrust/(SelfTrust+1). The focal thought has MIXED
// pos/neg charge ages so SelfTrust is recency-SENSITIVE — a builder that captured
// its own time.Now() would mis-age the charges and the equality would fail.
func TestMatrixRecency_DiagonalMatchesFold(t *testing.T) {
	ctx := context.Background()
	T := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	c := newInfluenceCorpus()
	c.addThought("F", "Focal", "10")          // creates c-F positive
	c.charges["c-F"].UpdatedAt = T.UnixNano() // positive at age 0 (scalar 1.0)
	c.charges["c-F-neg"] = recencyCharge("c-F-neg", "negative", 10, nanosDaysAgo(T, 365))
	c.chargeOf["F"] = append(c.chargeOf["F"], "c-F-neg")
	c.addThought("N", "Neighbor", "1") // exactly one neighbor for the focal row
	c.addEdge("F", "N")
	c.addEdge("N", "F") // reverse, in case fetchAdjacency is directional
	ids := []string{"F", "N"}

	matrixWC, chargeMap, err := BuildTrustMatrixWithCharges(ctx, c, ids, T)
	require.NoError(t, err)
	matrixBare, err := BuildTrustMatrix(ctx, c, ids, T)
	require.NoError(t, err)

	// Oracle: the Phase-1-verified fold at the SAME injected T (expected value only;
	// the ASSERTED quantity is produced by the matrix path).
	want := computePropertiesFromCharges(chargeMap["F"], T).SelfTrust
	wantNorm := want / (want + 1.0) // one neighbor of raw weight 1.0

	assert.InDelta(t, wantNorm, focalDiagonal(t, matrixWC), 1e-9,
		"BuildTrustMatrixWithCharges diagonal == fold SelfTrust at injected now")
	assert.InDelta(t, wantNorm, focalDiagonal(t, matrixBare), 1e-9,
		"BuildTrustMatrix diagonal == fold SelfTrust at injected now")

	// DIRECTION SANITY: the MIXED pos/neg-age diagonal is strictly GREATER than an
	// all-RECENT uniform config (both charges at T ⇒ recency-weighted pos==neg ⇒
	// Consistency 0 ⇒ SelfTrust == baseSelfTrust), confirming mixed ages raise
	// Consistency above the canceling baseline.
	u := newInfluenceCorpus()
	u.addThought("F", "Focal", "10")
	u.charges["c-F"].UpdatedAt = T.UnixNano()
	u.charges["c-F-neg"] = recencyCharge("c-F-neg", "negative", 10, T.UnixNano()) // both at T (age 0)
	u.chargeOf["F"] = append(u.chargeOf["F"], "c-F-neg")
	u.addThought("N", "Neighbor", "1")
	u.addEdge("F", "N")
	u.addEdge("N", "F")
	uniformMatrix, _, err := BuildTrustMatrixWithCharges(ctx, u, []string{"F", "N"}, T)
	require.NoError(t, err)
	assert.Greater(t, focalDiagonal(t, matrixWC), focalDiagonal(t, uniformMatrix),
		"mixed pos/neg ages raise the diagonal above the all-recent canceling baseline")
}

// TestMatrixRecency_InfluenceRankingPrecedence (assertion b) proves recency
// precedence lands in the influence ranking: two charged thoughts with IDENTICAL
// raw weight+count and SYMMETRIC topology (so equal eigenvector influence) rank
// with the all-RECENT thought ABOVE the all-OLD one, driven purely by the
// recency-weighted chargeWeight in partitionInfluenceRanking.
func TestMatrixRecency_InfluenceRankingPrecedence(t *testing.T) {
	ctx := context.Background()
	T := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	c := newInfluenceCorpus()
	// OLD is appended BEFORE recent on purpose: with identical raw weight+count and
	// equal influence, partitionInfluenceRanking's stable sort keeps input order on
	// a tie — so absent the recency weighting the OLD thought would rank first. Only
	// the recency-weighted chargeWeight can lift RECENT above it, making this a true
	// fails-when-absent discriminator.
	c.addThought("O", "Old", "10")
	c.addThought("R", "Recent", "10")
	c.addUnchargedThought("Hub", "Hub")               // common hub keeps both leaves in Evidenced
	c.charges["c-R"].UpdatedAt = T.UnixNano()         // all-recent (age 0)
	c.charges["c-O"].UpdatedAt = nanosDaysAgo(T, 365) // all-old
	// Symmetric topology: each leaf bidirectionally joined to the hub only.
	c.addEdge("Hub", "R")
	c.addEdge("R", "Hub")
	c.addEdge("Hub", "O")
	c.addEdge("O", "Hub")
	ids := []string{"O", "R", "Hub"}

	matrix, chargeMap, err := BuildTrustMatrixWithCharges(ctx, c, ids, T)
	require.NoError(t, err)
	influence := ComputeInfluenceVector(matrix)
	ranking := partitionInfluenceRanking(ctx, c, ids, influence, chargeMap, 10, T)

	order := influenceOrder(ranking.Evidenced)
	idxR, idxO := -1, -1
	for i, id := range order {
		switch id {
		case "R":
			idxR = i
		case "O":
			idxO = i
		}
	}
	require.GreaterOrEqual(t, idxR, 0, "recent thought present in Evidenced")
	require.GreaterOrEqual(t, idxO, 0, "old thought present in Evidenced")
	assert.Less(t, idxR, idxO, "all-recent thought ranks above all-old of identical raw weight+count")
}
