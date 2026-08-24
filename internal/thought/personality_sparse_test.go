// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// sparseOracleCorpus builds a synthetic corpus for the equivalence tests: c
// single-thought clusters, the first chargedRows of them carrying three charges
// each (positive, negative, positive, at increasing timestamps, with weights that
// vary by row so rows do not all collapse onto the same scalar), and evPerRow of
// each charged row's charges routed to a thought in a NEIGHBORING cluster so the
// deviation path is exercised rather than merely present.
//
// Each routed charge targets a different neighbor, so a row with evPerRow routed
// charges produces evPerRow distinct deviating columns.
//
// It reuses chargeFakeCaller and chargeNode from personality_test.go rather than
// introducing a second fake: that one already answers the two Execute round-trips
// the charge map read issues. Step 3.1's scale test reuses this builder, which is
// why it is a package-level helper rather than a closure.
func sparseOracleCorpus(c, chargedRows, evPerRow int) ([]ThoughtCluster, *chargeFakeCaller, map[string][]string) {
	clusters := make([]ThoughtCluster, 0, c)
	for i := range c {
		clusters = append(clusters, ThoughtCluster{
			ID:         fmt.Sprintf("cl%05d", i),
			Label:      fmt.Sprintf("label-%05d", i),
			ThoughtIDs: []string{fmt.Sprintf("t%05d", i)},
		})
	}

	polarities := [3]string{"positive", "negative", "positive"}
	createdAt := [3]int64{100, 200, 300}

	chargedBy := make(map[string][]string, chargedRows)
	chargeNodes := make(map[string]*knowledgev1.Node, chargedRows*3)
	evidenceAdj := make(map[string][]string)
	for i := range chargedRows {
		thoughtID := fmt.Sprintf("t%05d", i)
		for k := range 3 {
			chargeID := fmt.Sprintf("ch%05d_%d", i, k)
			chargeNodes[chargeID] = chargeNode(chargeID, polarities[k], float64(3+k+(i%3)), createdAt[k])
			chargedBy[thoughtID] = append(chargedBy[thoughtID], chargeID)
			if k < evPerRow {
				evidenceAdj[chargeID] = []string{fmt.Sprintf("t%05d", (i+1+k)%c)}
			}
		}
	}
	return clusters, &chargeFakeCaller{chargedBy: chargedBy, chargeNodes: chargeNodes}, evidenceAdj
}

// denseOracle rebuilds the FULL cluster-pair matrix the retired dense producer
// emitted, by driving the same buildChargeCache and computeClusterPairScalar the
// dense implementation drove, for every ordered pair.
//
// This is an oracle rather than a tautology: it is deliberately NOT the path the
// sparse producer takes. The sparse producer calls computeClusterPairScalar once
// per row with a DERIVED absent column key, plus once per deviating column — it
// never walks the pair space at all, so agreeing with this matrix everywhere is a
// real claim about the encoding.
func denseOracle(t *testing.T, ctx context.Context, gc Caller, clusters []ThoughtCluster, evidenceAdj map[string][]string) map[string]map[string]float64 {
	t.Helper()
	thoughtToCluster := make(map[string]string)
	for _, c := range clusters {
		for _, tid := range c.ThoughtIDs {
			thoughtToCluster[tid] = c.ID
		}
	}
	cache := buildChargeCache(ctx, gc, clusters, thoughtToCluster, evidenceAdj, nil)

	dense := make(map[string]map[string]float64, len(clusters))
	for _, clusterA := range clusters {
		row := make(map[string]float64, len(clusters))
		for _, clusterB := range clusters {
			if clusterA.ID == clusterB.ID {
				continue
			}
			row[clusterB.ID] = computeClusterPairScalar(clusterA, clusterB.ID, cache, time.Time{})
		}
		dense[clusterA.ID] = row
	}
	return dense
}

// denseTopK reproduces the retired full-matrix selection: materialize every pair
// of the dense matrix, sort ascending then descending with the SAME comparators
// production uses, and slice personalityTopK off each end.
func denseTopK(dense map[string]map[string]float64, labels map[string]string, clusterFilter string) ([]ClusterPairScalar, []ClusterPairScalar) {
	var pairs []ClusterPairScalar
	for clusterA, row := range dense {
		if clusterFilter != "" && clusterA != clusterFilter {
			continue
		}
		for clusterB, scalar := range row {
			pairs = append(pairs, ClusterPairScalar{
				ClusterA: clusterA,
				ClusterB: clusterB,
				LabelA:   labels[clusterA],
				LabelB:   labels[clusterB],
				Scalar:   scalar,
			})
		}
	}
	sortPairs(pairs, lessPairAsc)
	limit := min(len(pairs), personalityTopK)
	stubborn := append([]ClusterPairScalar(nil), pairs[:limit]...)
	sortPairs(pairs, lessPairDesc)
	limit = min(len(pairs), personalityTopK)
	gullible := append([]ClusterPairScalar(nil), pairs[:limit]...)
	return stubborn, gullible
}

func sortPairs(pairs []ClusterPairScalar, less func(a, b ClusterPairScalar) bool) {
	sort.Slice(pairs, func(i, j int) bool { return less(pairs[i], pairs[j]) })
}

// oracleShape is one corpus shape in the equivalence tables.
type oracleShape struct {
	clusters    int
	chargedRows int
	evPerRow    int
}

func (s oracleShape) name() string {
	return fmt.Sprintf("c%d_charged%d_ev%d", s.clusters, s.chargedRows, s.evPerRow)
}

// TestPersonalityProfile_SparseBitExactVsDenseOracle is the ticket's equivalence
// bar: every ordered pair the sparse profile answers must carry the value the
// dense matrix held, to the BIT — because a TOLERANCE would let a re-derivation
// pass where only the identical sequence of floating-point operations should.
//
// THE SPELLING CHANGED AND THE RULE DID NOT. This comment used to read
// "require.Equal, never InDelta", written when InDelta implied a tolerance. The
// assertion is now require.InDelta with a delta of ZERO, which testify evaluates
// as |expected-actual| <= 0 — bit-exact, no tolerance admitted, and the same
// failure on NaN that require.Equal gave. What is forbidden is a NON-ZERO delta;
// if you widen that 0, you have deleted the property this test exists for.
//
// The single-cluster and two-cluster shapes are in the table because that is
// where an off-by-one in the column set hides.
func TestPersonalityProfile_SparseBitExactVsDenseOracle(t *testing.T) {
	ctx := context.Background()
	shapes := []oracleShape{
		{12, 8, 2}, {40, 40, 3}, {60, 5, 1}, {3, 3, 2}, {2, 2, 1}, {1, 1, 0},
	}
	for _, shape := range shapes {
		t.Run(shape.name(), func(t *testing.T) {
			clusters, gc, evidenceAdj := sparseOracleCorpus(shape.clusters, shape.chargedRows, shape.evPerRow)
			profile, err := ComputePersonalityScalars(ctx, gc, clusters, evidenceAdj, nil)
			require.NoError(t, err)
			dense := denseOracle(t, ctx, gc, clusters, evidenceAdj)

			for _, clusterA := range clusters {
				for _, clusterB := range clusters {
					if clusterA.ID == clusterB.ID {
						continue
					}
					got, ok := profile.Scalar(clusterA.ID, clusterB.ID)
					require.True(t, ok, "pair %s->%s must be present in the sparse profile", clusterA.ID, clusterB.ID)
					require.InDelta(t, dense[clusterA.ID][clusterB.ID], got, 0,
						"pair %s->%s must be BIT-EXACT against the dense oracle", clusterA.ID, clusterB.ID)
				}
			}

			// The three absent cases, each of which the dense map expressed as a
			// missing key and the sparse accessor must express as ok=false.
			first := clusters[0].ID
			_, ok := profile.Scalar(first, first)
			require.False(t, ok, "the self pair is absent")
			_, ok = profile.Scalar(first, "GHOST-CLUSTER")
			require.False(t, ok, "an unknown column is absent (stale persisted cluster_id case)")
			_, ok = profile.Scalar("GHOST-CLUSTER", first)
			require.False(t, ok, "an unknown row is absent")

			// NON-VACUITY CONTROLS. Without these, an all-neutral profile with a dead
			// deviation path satisfies every equality above — the oracle would agree
			// with the sparse form because both are 1.0 everywhere.
			if shape.chargedRows > 0 {
				offNeutral := 0
				for _, value := range profile.RowDefault {
					if value != 1.0 {
						offNeutral++
					}
				}
				require.Positive(t, offNeutral,
					"control: a charged corpus must move at least one row default off the 1.000 neutral")
			}
			if shape.evPerRow > 0 && shape.clusters > 2 {
				differing := 0
				for clusterA, row := range profile.Deviations {
					for _, value := range row {
						if value != profile.RowDefault[clusterA] {
							differing++
						}
					}
				}
				require.Positive(t, differing,
					"control: routed evidence must produce at least one deviation that differs from its own row default")
			}
		})
	}
}

// TestReflectPersonality_TopKMatchesDenseOracle pins that the BOUNDED candidate
// selection returns element-for-element what a full dense scan would have
// selected — labels included, so a label-resolution regression is caught too —
// under every cluster filter and at a cluster population well past the point
// where the dense gather was affordable.
func TestReflectPersonality_TopKMatchesDenseOracle(t *testing.T) {
	ctx := context.Background()
	shapes := []oracleShape{
		{12, 8, 2}, {40, 40, 3}, {60, 5, 1}, {3, 3, 2}, {2, 2, 1}, {1, 1, 0}, {200, 30, 3},
	}
	for _, shape := range shapes {
		t.Run(shape.name(), func(t *testing.T) {
			clusters, gc, evidenceAdj := sparseOracleCorpus(shape.clusters, shape.chargedRows, shape.evPerRow)
			profile, err := ComputePersonalityScalars(ctx, gc, clusters, evidenceAdj, nil)
			require.NoError(t, err)
			dense := denseOracle(t, ctx, gc, clusters, evidenceAdj)

			filters := []string{"", clusters[0].ID, clusters[len(clusters)-1].ID}
			for _, filter := range filters {
				report := ReflectPersonality(clusters, &profile, filter)
				wantStubborn, wantGullible := denseTopK(dense, profile.ClusterLabels, filter)
				require.Equal(t, wantStubborn, report.TopStubborn,
					"TopStubborn must equal the dense top-K element for element (filter %q)", filter)
				require.Equal(t, wantGullible, report.TopGullible,
					"TopGullible must equal the dense top-K element for element (filter %q)", filter)
			}

			// NON-VACUITY CONTROL: with more than one cluster there are real pairs to
			// select, so an empty selection would mean the gather returned nothing and
			// both sides agreed on emptiness.
			if shape.clusters > 1 {
				report := ReflectPersonality(clusters, &profile, "")
				require.NotEmpty(t, report.TopStubborn,
					"control: a multi-cluster corpus must select at least one stubborn row")
			}
		})
	}
}
