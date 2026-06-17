// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"math"
	"sort"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// blindspots_reversal.go holds the belief-reversal facet (facet 5) machinery, split
// from blindspots.go to keep that file under the 500-line limit. It carries BOTH
// views: the per-thought reversal (beliefReversal, called from facetsForThought) and
// the topic/cluster-pooled reversal (classifyClusterReversals, attached to the
// facet's Groups). Both share one partition/flip implementation so "old vs recent
// net polarity" means the same thing in each. All pure — no graph reads.

// blindSpotReversalRecentWindow is the facet-5 old/recent partition boundary:
// charges created within this window of `now` are "recent", older charges are
// "old". It is the SINGLE source of truth for the belief-reversal partition — used
// by BOTH the per-thought reversal and the cluster/topic-pooled reversal so the
// two views always agree on what "old" vs "recent" means. 30 days separates a
// current stance from a prior one for the regime-change detector.
const blindSpotReversalRecentWindow = 30 * 24 * time.Hour

// blindSpotReversalMinOldMass is the density gate for the cluster/topic-level
// belief reversal: a group must carry at least this much summed OLD-charge weight
// (|oldNet|, the net mass of the prior stance) before a flip counts. Without it a
// sparsely-charged group — e.g. one old +3 vs one recent -3 — would masquerade as a
// topic-wide regime change. 6.0 is roughly two weighty charges' worth on the old
// side, the floor at which "the group genuinely held the prior stance" is credible.
const blindSpotReversalMinOldMass = 6.0

// BlindSpotGroup is one flagged topic/cluster-level reversal: a group of thoughts
// (a topic, or a raw Leiden cluster when the topic has no summary doc) whose
// POOLED charges net one polarity in the old window and the opposite in the recent
// window. Members are the thought IDs driving the reversal (those carrying charges
// on either side), so a reader can see what moved.
type BlindSpotGroup struct {
	Key         string   // topic key (primary cluster_id) or raw cluster ID
	Label       string   // topic summary, or the cluster label fallback
	OldNet      float64  // signed pooled weight in the old window (+ = positive)
	RecentNet   float64  // signed pooled weight in the recent window
	MemberCount int      // distinct member thoughts in the group
	Members     []string // member thought IDs carrying charges on either side
	Reason      string   // short human-readable old→recent swing description
}

// partitionChargeWeight folds a charge set into signed net weight in the OLD vs
// RECENT windows, partitioning each charge by its OWN CreatedAt against the
// blindSpotReversalRecentWindow cutoff (so cluster churn between ticks cannot
// corrupt the signal — a charge always lands in the bucket its own timestamp
// dictates). Positive polarity adds weight, negative subtracts; unknown polarity
// and zero-CreatedAt charges are skipped. Shared by the per-thought
// (beliefReversal) and pooled cluster/topic (classifyClusterReversals) views so
// both compute "old vs recent net polarity" identically.
func partitionChargeWeight(charges []*knowledgev1.Node, now time.Time) (oldNet, recentNet float64) {
	cutoff := now.Add(-blindSpotReversalRecentWindow)
	for _, c := range charges {
		if c.CreatedAt == 0 {
			continue
		}
		w := parseFloat(kgtypes.Value(c, "weight"))
		switch kgtypes.Value(c, "polarity") {
		case "negative":
			w = -w
		case "positive":
			// keep w positive
		default:
			continue
		}
		if nanosToTime(c.CreatedAt).Before(cutoff) {
			oldNet += w
		} else {
			recentNet += w
		}
	}
	return oldNet, recentNet
}

// netReversed reports whether two signed net weights constitute a reversal: both
// non-zero (a net-zero side has no net polarity, so it can never establish a
// reversal — deterministically NO flag) and of opposite sign.
func netReversed(oldNet, recentNet float64) bool {
	if oldNet == 0 || recentNet == 0 {
		return false
	}
	return math.Signbit(oldNet) != math.Signbit(recentNet)
}

// polarityName maps a signed net weight to its polarity word for reason strings.
func polarityName(net float64) string {
	if net < 0 {
		return "negative"
	}
	return "positive"
}

// beliefReversal reports whether the OLD charges net one polarity and the RECENT
// charges net the OPPOSITE polarity. Charges created within
// blindSpotReversalRecentWindow of now are "recent"; older charges are "old". A
// side with equal positive and negative weight (net-zero) has no net polarity, so
// it can never establish a reversal — the boundary is deterministic and does NOT
// flag. The reason states the old→recent polarity flip.
func beliefReversal(charges []*knowledgev1.Node, now time.Time) (string, bool) {
	oldNet, recentNet := partitionChargeWeight(charges, now)
	if !netReversed(oldNet, recentNet) {
		return "", false
	}
	return "old charges net " + polarityName(oldNet) + ", recent charges net " +
		polarityName(recentNet) + " — belief reversal", true
}

// classifyClusterReversals computes the topic/cluster-pooled belief-reversal view.
// It rolls Leiden clusters that share a topic-summary doc into one unit (via
// in.topics.topicKeyFor — falling back to the raw cluster when the topic has no
// summary), pools every NON-machine-genre member thought's charges, and flags the
// group when the pooled OLD net polarity reverses to the pooled RECENT net polarity
// AND the old side carries at least blindSpotReversalMinOldMass weight (the density
// gate, so a sparsely-charged group can't masquerade as a regime change). Pure: it
// reuses the charge map the caller already fetched — no new wire call. Groups are
// ordered by |old-charge mass| descending (then key) and capped to
// blindSpotFacetCap.
func classifyClusterReversals(in blindSpotInputs, machine func(string) bool) []BlindSpotGroup {
	// Pool non-machine member thoughts per topic key over CURRENT membership.
	membersByKey := map[string][]string{}
	labelByKey := map[string]string{}
	var keyOrder []string
	for _, c := range in.clusters {
		key := in.topics.topicKeyFor(c.ID)
		if _, seen := labelByKey[key]; !seen {
			labelByKey[key] = in.topics.labelFor(key, c.Label)
			keyOrder = append(keyOrder, key)
		}
		for _, tid := range c.ThoughtIDs {
			if !machine(tid) {
				membersByKey[key] = append(membersByKey[key], tid)
			}
		}
	}

	var groups []BlindSpotGroup
	for _, key := range keyOrder {
		members := membersByKey[key]
		if len(members) == 0 {
			continue
		}
		pooled := make([]*knowledgev1.Node, 0, len(members))
		driving := make([]string, 0, len(members))
		for _, tid := range members {
			tc := in.charges[tid]
			if len(tc) > 0 {
				pooled = append(pooled, tc...)
				driving = append(driving, tid)
			}
		}
		oldNet, recentNet := partitionChargeWeight(pooled, in.now)
		if math.Abs(oldNet) < blindSpotReversalMinOldMass {
			continue // density gate: the group never credibly held the prior stance.
		}
		if !netReversed(oldNet, recentNet) {
			continue
		}
		sort.Strings(driving)
		groups = append(groups, BlindSpotGroup{
			Key:         key,
			Label:       labelByKey[key],
			OldNet:      oldNet,
			RecentNet:   recentNet,
			MemberCount: len(members),
			Members:     driving,
			Reason: "pooled charges flip old=" + polarityName(oldNet) + " → recent=" +
				polarityName(recentNet) + " — topic/cluster-level belief reversal",
		})
	}

	// Order by old-charge mass desc (biggest prior stance first), key tie-break.
	sort.Slice(groups, func(i, j int) bool {
		mi, mj := math.Abs(groups[i].OldNet), math.Abs(groups[j].OldNet)
		if mi != mj {
			return mi > mj
		}
		return groups[i].Key < groups[j].Key
	})
	if len(groups) > blindSpotFacetCap {
		groups = groups[:blindSpotFacetCap]
	}
	return groups
}
