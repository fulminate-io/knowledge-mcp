// SPDX-License-Identifier: Apache-2.0

package thought

import "time"

// PersonalityProfile holds per-cluster-pair scalars derived from external
// charge accuracy — how much cluster A trusts influence from cluster B,
// where > 1.0 = gullible and < 1.0 = stubborn — in a SPARSE encoding.
//
// Within a row A, the scalar depends on the column only through whether the
// column is the evidence cluster of one of A's own charges, so every other
// column yields the identical value bit for bit. A row is therefore stored as
// that one shared value plus an entry per genuinely differing column, not as a
// full row of C entries.
//
// RowDefault[A] is row A's shared value — the multiplier A applies to its other
// columns, NOT a multiplier applied TO A. It doubles as the profile's CLUSTER-ID
// SET: it carries exactly one entry per cluster the profile was built from, and
// Scalar's column check reads it as that set. Deviations is keyed
// [rowCluster][columnCluster] and holds only the columns whose value differs
// from the row's default; a row with no differing column carries no entry.
type PersonalityProfile struct {
	RowDefault    map[string]float64
	Deviations    map[string]map[string]float64
	ClusterLabels map[string]string
}

// Scalar returns the scalar for clusterA listening to clusterB, and whether
// the pair is present at all. It has three absent cases, and callers must
// consume ok rather than merely receive it: the self pair, a row that is not
// in the profile, and — the one that is easy to miss — a COLUMN that is not in
// the profile.
//
// That last gate is load-bearing rather than defensive. Both trust-row readers
// build their column identity from PERSISTED cluster_id node metadata, stamped
// by an earlier tick's cluster writeback, while the profile's cluster set is
// stamped by the tick that built the profile. Two stampers, so a reader can ask
// about a column this profile never knew. A dense map made that harmless: an
// inner key that was never written read as absent and the caller multiplied by
// nothing. Falling through to the row default for such a column would instead
// silently reweight the row, so an unknown column reports ok=false.
func (p PersonalityProfile) Scalar(clusterA, clusterB string) (float64, bool) {
	if clusterA == clusterB {
		return 0, false
	}
	rowDefault, ok := p.RowDefault[clusterA]
	if !ok {
		return 0, false
	}
	if _, isCluster := p.RowDefault[clusterB]; !isCluster {
		return 0, false
	}
	if deviation, ok := p.Deviations[clusterA][clusterB]; ok {
		return deviation, true
	}
	return rowDefault, true
}

// defaultColumnKey returns a column key guaranteed to be absent from present,
// which is the row's own set of charge evidence-cluster values. The row default
// is definitionally computeClusterPairScalar called with a column that matches
// none of those values, and extending the key until it is absent makes that
// true by construction rather than by assumption — it needs no assumption about
// cluster-ID format.
//
// It also cannot collide with the empty string, which is itself a REAL
// evidence-cluster value (the unresolved case, where a charge's evidence target
// resolved to no cluster). A naive empty-string sentinel would silently
// re-enforce every un-evidenced charge.
func defaultColumnKey(present map[string]bool) string {
	key := "\x00"
	for present[key] {
		key += "\x00"
	}
	return key
}

// computeSparseRow computes row clusterA of the profile: its shared default
// value, and the deviating columns keyed by cluster ID (nil when the row has
// none). Both come from the unchanged computeClusterPairScalar, which is what
// makes the sparse form bit-exact against the dense one rather than a
// re-derivation of it.
//
// The self column and the unresolved empty-string column are skipped: neither
// is a real pair, and each charge contributes at most one evidence cluster.
func computeSparseRow(clusterA ThoughtCluster, cache thoughtChargeCache) (float64, map[string]float64) {
	present := make(map[string]bool)
	for _, thoughtID := range clusterA.ThoughtIDs {
		for _, ci := range cache.charges[thoughtID] {
			present[ci.evidenceCluster] = true
		}
	}
	rowDefault := computeClusterPairScalar(clusterA, defaultColumnKey(present), cache, time.Time{})

	var deviations map[string]float64
	for column := range present {
		if column == "" || column == clusterA.ID {
			continue
		}
		if deviations == nil {
			deviations = make(map[string]float64, len(present))
		}
		deviations[column] = computeClusterPairScalar(clusterA, column, cache, time.Time{})
	}
	return rowDefault, deviations
}
