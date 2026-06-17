// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"math"
	"sort"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// blindspots.go holds the faceted epistemic-risk diagnostic: the result shape,
// the named facet thresholds, and the PURE per-thought facet classifier. The
// classifier takes pre-fetched inputs (charges, influence, hydrated nodes,
// session labels) and issues NO graph reads — the caller (the propagation loop's
// computeBlindSpots) does the bulk reads once per tick and feeds them in. The
// on-demand query(mode:blind_spots) handler then serves the loop's cached report
// in O(1) rather than recomputing anything.
//
// Each facet is an INDEPENDENTLY-useful section, not collapsed into one scalar:
// a thought may legitimately appear under several facets, and each section is
// capped and deterministically ordered so the surface stays in-context and
// reproducible across runs.

// Facet keys — stable identifiers for the five epistemic-risk sections. These are
// the wire/json keys the report carries and the handler renders per section.
const (
	// facetConfidentUntested: high magnitude + high consistency but <=1 charge —
	// treated as settled yet never actually tested by competing evidence.
	facetConfidentUntested = "confident_untested"
	// facetFoundationalUnexamined: high influence but 0 charges — shapes other
	// beliefs' consensus, so an outsized blast radius if it is wrong.
	facetFoundationalUnexamined = "foundational_unexamined"
	// facetFragileSinglePoint: consensus flips if a single pivotal charge is
	// removed (trivially true for <=1 charge).
	facetFragileSinglePoint = "fragile_single_point"
	// facetStaleConfidence: charged long ago, never re-charged (v1 = recency only).
	facetStaleConfidence = "stale_confidence"
	// facetBeliefReversal: old charges net one polarity, recent charges net the
	// opposite — a regime-change / revelation detector.
	facetBeliefReversal = "belief_reversal"
)

// Facet thresholds. These are defensible defaults tied to the existing
// charge-property formulas in properties.go, not magic numbers.
const (
	// blindSpotHighMagnitude marks a thought "treated as settled". Magnitude is
	// math.Log(1+totalChargeWeight) (properties.go computePropertiesFromCharges:
	// Magnitude = math.Log(1+total)); log(1+~4) ≈ 1.6, so ~1.5 corresponds to a
	// thought carrying several weighty charges' worth of total weight. Tied to the
	// log scale of the magnitude formula.
	blindSpotHighMagnitude = 1.5
	// blindSpotHighConsistency marks a thought whose charges are strongly
	// one-sided. Consistency is in [0,1] (properties.go: 1 - minSide/maxSide);
	// 0.8 means the dominant polarity holds at least ~80% of the weight balance.
	blindSpotHighConsistency = 0.8
	// blindSpotConfidentMaxCharges is the facet-1 charge gate: <=1 charge ("high
	// magnitude + high consistency but <=1 charge"). A <=1-charge thought has
	// magnitude = log(1+w); for it to ALSO clear blindSpotHighMagnitude it needs a
	// single heavy charge — that intersection IS the confident-but-untested signal,
	// intentional.
	blindSpotConfidentMaxCharges = 1
	// blindSpotFacetCap bounds each facet's item list. Smaller than a single global
	// cap would be (five facets × 10 keeps the whole surface in-context); the old
	// global cluster cap is removed with the old ranked path.
	blindSpotFacetCap = 10
)

// blindSpotStaleWindow is the facet-4 recency window: a thought whose NEWEST
// charge is older than this is "charged long ago, never re-charged". Tuned for the
// AI-era cadence — beliefs go stale fast, so a thought charged a week or more ago
// and never revisited since is already a staleness signal (a once-confident stance
// nobody has touched in 7+ days of active work). v1 is recency only — the "cited
// code/decision has since changed" enrichment needs cross-graph staleness and is
// out of scope.
const blindSpotStaleWindow = 7 * 24 * time.Hour

// The belief-reversal facet (facet 5) machinery — the old/recent partition
// window, the density gate, the BlindSpotGroup type, and both the per-thought and
// topic/cluster-pooled reversal classifiers — lives in blindspots_reversal.go.

// BlindSpotItem is one flagged thought within a facet, carrying the signal
// values that back the flag plus a short human-readable Reason. Name is the
// thought's SymbolName (or its ID when unhydrated).
type BlindSpotItem struct {
	ThoughtID   string
	Name        string
	Magnitude   float64
	Consistency float64
	ChargeCount int
	Influence   float64
	Reason      string
}

// BlindSpotFacet is one capped, deterministically-ordered section of the report.
// Items is the per-thought view. Groups is the topic/cluster-pooled view, used
// today only by facet 5 (belief reversal) to surface group-wide regime changes
// alongside the per-thought ones; empty for every other facet.
type BlindSpotFacet struct {
	Key    string
	Title  string
	Items  []BlindSpotItem
	Groups []BlindSpotGroup
}

// BlindSpotReport is the whole faceted epistemic-risk diagnostic. Computed=false
// is the cold sentinel: the propagation loop has not produced a report yet (zero
// value after a daemon restart, before the first tick), and the handler renders a
// not-yet-computed message rather than an empty report. TotalThoughts is the
// count of non-machine-genre thoughts considered.
type BlindSpotReport struct {
	Facets        []BlindSpotFacet
	TotalThoughts int
	Computed      bool
}

// facetTitles maps each facet key to its human-readable section title, used by
// both the classifier (to build sections in a stable order) and the renderer.
var facetTitles = map[string]string{
	facetConfidentUntested:      "Confident but untested",
	facetFoundationalUnexamined: "Foundational but unexamined",
	facetFragileSinglePoint:     "Fragile single-point",
	facetStaleConfidence:        "Stale confidence",
	facetBeliefReversal:         "Belief reversal",
}

// facetOrder is the stable section order facets render in.
var facetOrder = []string{
	facetConfidentUntested,
	facetFoundationalUnexamined,
	facetFragileSinglePoint,
	facetStaleConfidence,
	facetBeliefReversal,
}

// blindSpotInputs bundles the pre-fetched, pure inputs classifyBlindSpots folds
// over so the signature stays readable as the classifier grows. The loop's
// computeBlindSpots populates it once per tick from its bulk reads; none of these
// fields is mutated by the classifier.
type blindSpotInputs struct {
	thoughtIDs       []string
	charges          map[string][]*knowledgev1.Node
	influence        map[string]float64
	nodeByID         map[string]*knowledgev1.Node
	sessionByThought map[string]string
	// clusters is the Leiden partition for this tick (each .ThoughtIDs); topics
	// rolls member clusters sharing a topic-summary doc into one unit. Both feed the
	// cluster/topic-level belief-reversal view (facet 5 Groups). A nil clusters
	// slice simply yields no group-level reversals — the per-thought facets are
	// unaffected.
	clusters []ThoughtCluster
	topics   TopicGrouping
	now      time.Time
}

// classifyBlindSpots is the PURE facet classifier — no graph reads, every input
// supplied by the caller. For each non-machine-genre thought it computes the
// charge-derived properties once and tests each facet's gate, appending a
// BlindSpotItem (with the backing signals + a Reason) to every facet it matches.
// Each facet's items are then sorted by the facet's natural score descending with
// a ThoughtID tie-break (deterministic across runs) and capped to
// blindSpotFacetCap. Facet 5 additionally carries a topic/cluster-pooled
// belief-reversal view (Groups) computed by classifyClusterReversals. Computed is
// set true and TotalThoughts is the count of non-machine-genre thoughts considered.
//
// Reuses computePropertiesFromCharges (charge fold), computePropertiesExcluding
// (pure remove-charge simulation for facet 3), isMachineGenreThought (genre
// exclusion), and nanosToTime (charge CreatedAt comparison for facets 4/5) — all
// pure, all in-package.
func classifyBlindSpots(in blindSpotInputs) BlindSpotReport {
	items := map[string][]BlindSpotItem{}
	considered := 0

	machine := func(id string) bool {
		return isMachineGenreThought(in.nodeByID[id], in.sessionByThought[id])
	}
	for _, id := range in.thoughtIDs {
		if machine(id) {
			continue
		}
		considered++
		for key, it := range facetsForThought(id, in.charges[id], in.influence[id], in.nodeByID[id], in.now) {
			items[key] = append(items[key], it)
		}
	}

	// Topic/cluster-pooled belief reversals attach to facet 5's Groups view.
	groups := classifyClusterReversals(in, machine)

	report := BlindSpotReport{Computed: true, TotalThoughts: considered}
	for _, key := range facetOrder {
		facetItems := items[key]
		var facetGroups []BlindSpotGroup
		if key == facetBeliefReversal {
			facetGroups = groups
		}
		if len(facetItems) == 0 && len(facetGroups) == 0 {
			continue
		}
		sortFacetItems(key, facetItems)
		if len(facetItems) > blindSpotFacetCap {
			facetItems = facetItems[:blindSpotFacetCap]
		}
		report.Facets = append(report.Facets, BlindSpotFacet{
			Key:    key,
			Title:  facetTitles[key],
			Items:  facetItems,
			Groups: facetGroups,
		})
	}
	return report
}

// facetsForThought classifies a SINGLE non-machine-genre thought into the facets
// it matches, returning a facetKey→item map (a thought may match several facets).
// All inputs are pre-fetched and pure — no graph reads. The base item carries the
// backing signal values; each matched facet sets a per-facet Reason.
func facetsForThought(
	id string,
	thoughtCharges []*knowledgev1.Node,
	influence float64,
	node *knowledgev1.Node,
	now time.Time,
) map[string]BlindSpotItem {
	props := computePropertiesFromCharges(thoughtCharges)
	name := id
	if node != nil && node.SymbolName != "" {
		name = node.SymbolName
	}
	base := BlindSpotItem{
		ThoughtID:   id,
		Name:        name,
		Magnitude:   props.Magnitude,
		Consistency: props.Consistency,
		ChargeCount: props.ChargeCount,
		Influence:   influence,
	}
	out := map[string]BlindSpotItem{}
	add := func(key, reason string) {
		it := base
		it.Reason = reason
		out[key] = it
	}

	// Facet 1: confident but untested.
	if props.Magnitude >= blindSpotHighMagnitude &&
		props.Consistency >= blindSpotHighConsistency &&
		props.ChargeCount <= blindSpotConfidentMaxCharges {
		add(facetConfidentUntested, "high magnitude + high consistency on <=1 charge — treated as settled, never tested")
	}
	// Facet 2: foundational but unexamined.
	if influence > 0 && props.ChargeCount == 0 {
		add(facetFoundationalUnexamined, "high influence with 0 charges — shapes other beliefs but carries no evidence")
	}
	// Facet 3: fragile single-point.
	if reason, fragile := fragileSinglePoint(thoughtCharges, props); fragile {
		add(facetFragileSinglePoint, reason)
	}
	// Facet 4: stale confidence.
	if props.ChargeCount >= 1 {
		if newest, ok := newestChargeTime(thoughtCharges); ok && newest.Before(now.Add(-blindSpotStaleWindow)) {
			add(facetStaleConfidence, "newest charge older than the staleness window — charged long ago, never re-charged")
		}
	}
	// Facet 5: belief reversal.
	if reason, reversed := beliefReversal(thoughtCharges, now); reversed {
		add(facetBeliefReversal, reason)
	}
	return out
}

// fragileSinglePoint reports whether removing a single charge would flip the
// thought's consensus. A thought with <=1 charge is trivially fragile (its whole
// stance rests on one or zero charges). Otherwise it simulates removing each
// charge in turn via computePropertiesExcluding (pure) and flags the thought if
// any single removal flips the sign of Valence (a non-zero before-valence is
// required so a net-zero stance is not spuriously "flipped"). The reason names
// the pivotal charge.
func fragileSinglePoint(charges []*knowledgev1.Node, props ThoughtProperties) (string, bool) {
	if props.ChargeCount <= 1 {
		return "<=1 charge — the entire stance rests on a single point", true
	}
	if props.Valence == 0 {
		return "", false
	}
	beforeSign := math.Signbit(props.Valence)
	for _, c := range charges {
		after := computePropertiesExcluding(charges, c.Id)
		if after.Valence == 0 {
			continue
		}
		if math.Signbit(after.Valence) != beforeSign {
			return "removing charge " + c.Id + " flips the consensus sign — fragile single-point", true
		}
	}
	return "", false
}

// newestChargeTime returns the most recent charge CreatedAt as a time.Time. ok is
// false when there are no charges (or none carry a non-zero CreatedAt).
func newestChargeTime(charges []*knowledgev1.Node) (time.Time, bool) {
	var newest int64
	for _, c := range charges {
		if c.CreatedAt > newest {
			newest = c.CreatedAt
		}
	}
	if newest == 0 {
		return time.Time{}, false
	}
	return nanosToTime(newest), true
}

// sortFacetItems orders a facet's items by the facet's natural score descending,
// with a ThoughtID ascending tie-break so equal-score rows order reproducibly
// across runs (the items are gathered in thoughtIDs order, but the tie-break makes
// the result independent of that order for stability). Facet 2 ranks by influence
// so the cap keeps the most foundational; the others rank by magnitude (the
// "treated as settled" signal) which is a reasonable shared default.
func sortFacetItems(key string, items []BlindSpotItem) {
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		var sa, sb float64
		if key == facetFoundationalUnexamined {
			sa, sb = a.Influence, b.Influence
		} else {
			sa, sb = a.Magnitude, b.Magnitude
		}
		if sa != sb {
			return sa > sb
		}
		return a.ThoughtID < b.ThoughtID
	})
}
