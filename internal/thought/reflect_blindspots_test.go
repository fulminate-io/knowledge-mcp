// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// These tests target the PURE classifyBlindSpots facet classifier: charge slices,
// influence, hydrated nodes, and session labels are built directly as in-memory
// maps — no Execute fake, no wire calls — and a fixed `now` drives the time-based
// facets deterministically.

// fixedNow is a stable reference time for the recency-based facets so tests do not
// depend on wall-clock.
var fixedNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// mkCharge builds a charge node with the given polarity, weight, and timestamp.
// CreatedAt AND UpdatedAt are both set to the timestamp: a charge is created once
// and not subsequently mutated, so the two coincide (matching production charge
// nodes). UpdatedAt drives the read-time recency scalar in the fold
// (computePropertiesFromCharges); the staleness/belief-reversal facets read
// CreatedAt — both must carry the age the fixture intends. The node IDs are unique
// per call site via the caller-supplied id.
func mkCharge(id, polarity string, weight float64, createdAt time.Time) *knowledgev1.Node {
	ch := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeCharge), CreatedAt: createdAt.UnixNano(), UpdatedAt: createdAt.UnixNano()}
	kgtypes.SetValue(ch, "polarity", polarity)
	kgtypes.SetValue(ch, "weight", fmt.Sprintf("%g", weight))
	return ch
}

// mkThought builds a thought node with the given source/origin facets (empty =
// human genre).
func mkThought(id, source, origin string) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: id, SymbolName: id, Type: string(kgtypes.NodeThought)}
	if source != "" {
		kgtypes.SetValue(n, "source", source)
	}
	if origin != "" {
		kgtypes.SetValue(n, "origin", origin)
	}
	return n
}

// classifyForTest runs the per-thought classifier path with no cluster/topic
// inputs and no session labels — the shape the per-thought facet tests exercise
// (genre exclusion here is driven by the node source/origin facets, not session
// markers). The cluster/topic belief-reversal view is tested separately via
// classifyBlindSpots with clusters populated.
func classifyForTest(
	thoughtIDs []string,
	charges map[string][]*knowledgev1.Node,
	influence map[string]float64,
	nodeByID map[string]*knowledgev1.Node,
	citedCodeUpdatedAt map[string]int64,
	now time.Time,
) BlindSpotReport {
	return classifyBlindSpots(blindSpotInputs{
		thoughtIDs:         thoughtIDs,
		charges:            charges,
		influence:          influence,
		nodeByID:           nodeByID,
		citedCodeUpdatedAt: citedCodeUpdatedAt,
		now:                now,
	})
}

// facetItems returns the items of the named facet from a report (nil when absent).
func facetItems(r BlindSpotReport, key string) []BlindSpotItem {
	for _, f := range r.Facets {
		if f.Key == key {
			return f.Items
		}
	}
	return nil
}

// hasThought reports whether the named facet contains an item for thoughtID.
func hasThought(r BlindSpotReport, key, thoughtID string) bool {
	for _, it := range facetItems(r, key) {
		if it.ThoughtID == thoughtID {
			return true
		}
	}
	return false
}

// TestClassifyBlindSpots_ConfidentUntested (FAILS-WHEN-ABSENT): a thought with ONE
// heavy charge (magnitude>=1.5, consistency=1.0, ChargeCount=1) lands in
// confident_untested; a many-charge thought does NOT.
func TestClassifyBlindSpots_ConfidentUntested(t *testing.T) {
	// One heavy positive charge → magnitude = log(1+6) ≈ 1.95 >= 1.5, consistency 1.0.
	charges := map[string][]*knowledgev1.Node{
		"settled": {mkCharge("c0", "positive", 6, fixedNow)},
		// Many charges → ChargeCount > 1, excluded by the <=1-charge gate.
		"tested": {
			mkCharge("t0", "positive", 3, fixedNow),
			mkCharge("t1", "positive", 3, fixedNow),
			mkCharge("t2", "positive", 3, fixedNow),
		},
	}
	nodeByID := map[string]*knowledgev1.Node{
		"settled": mkThought("settled", "", "implementer"),
		"tested":  mkThought("tested", "", "implementer"),
	}
	r := classifyForTest([]string{"settled", "tested"}, charges, nil, nodeByID, nil, fixedNow)

	assert.True(t, hasThought(r, facetConfidentUntested, "settled"),
		"single heavy-charge high-magnitude thought is confident-but-untested")
	assert.False(t, hasThought(r, facetConfidentUntested, "tested"),
		"a many-charge thought is not confident-but-untested")
}

// TestClassifyBlindSpots_FoundationalUnexamined (FAILS-WHEN-ABSENT): influence>0
// and ChargeCount==0 lands in foundational_unexamined; a 0-influence 0-charge
// thought does not.
func TestClassifyBlindSpots_FoundationalUnexamined(t *testing.T) {
	charges := map[string][]*knowledgev1.Node{} // both thoughts zero-charge.
	influence := map[string]float64{"hub": 0.4, "isolated": 0}
	nodeByID := map[string]*knowledgev1.Node{
		"hub":      mkThought("hub", "", "implementer"),
		"isolated": mkThought("isolated", "", "implementer"),
	}
	r := classifyForTest([]string{"hub", "isolated"}, charges, influence, nodeByID, nil, fixedNow)

	assert.True(t, hasThought(r, facetFoundationalUnexamined, "hub"),
		"high-influence zero-charge thought is foundational-but-unexamined")
	assert.False(t, hasThought(r, facetFoundationalUnexamined, "isolated"),
		"a zero-influence zero-charge thought is not foundational")
}

// TestClassifyBlindSpots_FragileSinglePoint (FAILS-WHEN-ABSENT): a thought whose
// net stance flips sign when one pivotal charge is removed is flagged; a
// same-polarity history is not; a <=1-charge thought is trivially flagged.
func TestClassifyBlindSpots_FragileSinglePoint(t *testing.T) {
	charges := map[string][]*knowledgev1.Node{
		// Net positive (5 pos - 2 neg = +3); removing the +5 leaves -2 → sign flips.
		"fragile": {
			mkCharge("f0", "positive", 5, fixedNow),
			mkCharge("f1", "negative", 2, fixedNow),
		},
		// Two same-polarity charges → removing either keeps the sign.
		"robust": {
			mkCharge("r0", "positive", 4, fixedNow),
			mkCharge("r1", "positive", 3, fixedNow),
		},
		// Single charge → trivially fragile.
		"single": {mkCharge("s0", "positive", 3, fixedNow)},
	}
	nodeByID := map[string]*knowledgev1.Node{
		"fragile": mkThought("fragile", "", "implementer"),
		"robust":  mkThought("robust", "", "implementer"),
		"single":  mkThought("single", "", "implementer"),
	}
	r := classifyForTest([]string{"fragile", "robust", "single"}, charges, nil, nodeByID, nil, fixedNow)

	assert.True(t, hasThought(r, facetFragileSinglePoint, "fragile"),
		"a thought that flips sign on single-charge removal is fragile")
	assert.True(t, hasThought(r, facetFragileSinglePoint, "single"),
		"a single-charge thought is trivially fragile")
	assert.False(t, hasThought(r, facetFragileSinglePoint, "robust"),
		"a same-polarity multi-charge thought is not fragile")
}

// TestClassifyBlindSpots_StaleConfidence (FAILS-WHEN-ABSENT): a thought whose
// newest charge is older than the staleness window (7 days) is flagged; a thought
// charged inside the window is not. The fixture straddles the 7d boundary — "stale"
// at 8 days old (past), "recent" at 6 days old (inside) — so the test discriminates
// at the 7d line specifically, not merely "ancient vs. now".
func TestClassifyBlindSpots_StaleConfidence(t *testing.T) {
	old := fixedNow.Add(-(blindSpotStaleWindow + 24*time.Hour))    // 8 days old → past the 7d window.
	within := fixedNow.Add(-(blindSpotStaleWindow - 24*time.Hour)) // 6 days old → inside the 7d window.
	charges := map[string][]*knowledgev1.Node{
		"stale":  {mkCharge("st0", "positive", 3, old)},
		"recent": {mkCharge("re0", "positive", 3, within)},
	}
	nodeByID := map[string]*knowledgev1.Node{
		"stale":  mkThought("stale", "", "implementer"),
		"recent": mkThought("recent", "", "implementer"),
	}
	r := classifyForTest([]string{"stale", "recent"}, charges, nil, nodeByID, nil, fixedNow)

	assert.True(t, hasThought(r, facetStaleConfidence, "stale"),
		"a thought whose newest charge predates the staleness window is stale")
	assert.False(t, hasThought(r, facetStaleConfidence, "recent"),
		"a recently-charged thought is not stale")
}

// TestClassifyBlindSpots_CodeChanged (FAILS-WHEN-ABSENT): a thought whose cited
// code UpdatedAt is newer than its newest charge is flagged facetCodeChanged; a
// thought whose cited code is older-or-equal is not; an uncited thought (no map
// entry → 0) is never flagged. Mirrors the stale-confidence test: a single charge
// at fixedNow, and the citedCodeUpdatedAt fixture straddles that charge time.
func TestClassifyBlindSpots_CodeChanged(t *testing.T) {
	charges := map[string][]*knowledgev1.Node{
		"changed": {mkCharge("cc0", "positive", 3, fixedNow)},
		"fresh":   {mkCharge("fr0", "positive", 3, fixedNow)},
		"uncited": {mkCharge("un0", "positive", 3, fixedNow)},
	}
	nodeByID := map[string]*knowledgev1.Node{
		"changed": mkThought("changed", "", "implementer"),
		"fresh":   mkThought("fresh", "", "implementer"),
		"uncited": mkThought("uncited", "", "implementer"),
	}
	// "changed": code modified one hour AFTER the newest charge → flagged.
	// "fresh":   code modified one hour BEFORE the newest charge → not flagged.
	// "uncited": no entry (0) → never flagged (0 > any positive UnixNano is false).
	citedCodeUpdatedAt := map[string]int64{
		"changed": fixedNow.Add(time.Hour).UnixNano(),
		"fresh":   fixedNow.Add(-time.Hour).UnixNano(),
	}
	r := classifyForTest([]string{"changed", "fresh", "uncited"}, charges, nil, nodeByID, citedCodeUpdatedAt, fixedNow)

	assert.True(t, hasThought(r, facetCodeChanged, "changed"),
		"a thought whose cited code changed after its newest charge is flagged")
	assert.False(t, hasThought(r, facetCodeChanged, "fresh"),
		"a thought whose cited code predates its newest charge is not flagged")
	assert.False(t, hasThought(r, facetCodeChanged, "uncited"),
		"a thought with no resolvable cited code is never flagged")
}

// TestClassifyBlindSpots_BeliefReversal (FAILS-WHEN-ABSENT): OLD charges net one
// polarity and RECENT charges net the opposite polarity → flagged; a
// consistent-polarity history is not.
func TestClassifyBlindSpots_BeliefReversal(t *testing.T) {
	old := fixedNow.Add(-(blindSpotReversalRecentWindow + 24*time.Hour)) // old side.
	recent := fixedNow.Add(-1 * time.Hour)                               // recent side.
	charges := map[string][]*knowledgev1.Node{
		// Old negative, recent positive → reversal.
		"reversed": {
			mkCharge("rv0", "negative", 4, old),
			mkCharge("rv1", "positive", 5, recent),
		},
		// Old positive, recent positive → no reversal.
		"consistent": {
			mkCharge("co0", "positive", 4, old),
			mkCharge("co1", "positive", 5, recent),
		},
	}
	nodeByID := map[string]*knowledgev1.Node{
		"reversed":   mkThought("reversed", "", "implementer"),
		"consistent": mkThought("consistent", "", "implementer"),
	}
	r := classifyForTest([]string{"reversed", "consistent"}, charges, nil, nodeByID, nil, fixedNow)

	assert.True(t, hasThought(r, facetBeliefReversal, "reversed"),
		"old-negative/recent-positive history is a belief reversal")
	assert.False(t, hasThought(r, facetBeliefReversal, "consistent"),
		"a consistent-polarity history is not a reversal")
}

// TestClassifyBlindSpots_BeliefReversal_NetZeroSideNoFlag (FAILS-WHEN-ABSENT,
// reviewer-pinned boundary): a side with equal positive and negative weight is
// net-zero and has no net polarity, so it can NEVER establish a reversal. Here the
// OLD side is net-zero (4 pos + 4 neg) while the recent side is clearly positive;
// the thought MUST NOT be flagged. Pins the deterministic no-flag boundary.
func TestClassifyBlindSpots_BeliefReversal_NetZeroSideNoFlag(t *testing.T) {
	old := fixedNow.Add(-(blindSpotReversalRecentWindow + 24*time.Hour))
	recent := fixedNow.Add(-1 * time.Hour)
	charges := map[string][]*knowledgev1.Node{
		"netzero": {
			mkCharge("nz0", "positive", 4, old),    // old side: +4
			mkCharge("nz1", "negative", 4, old),    // old side: -4 → net 0
			mkCharge("nz2", "positive", 5, recent), // recent side: +5
		},
	}
	nodeByID := map[string]*knowledgev1.Node{"netzero": mkThought("netzero", "", "implementer")}
	r := classifyForTest([]string{"netzero"}, charges, nil, nodeByID, nil, fixedNow)

	assert.False(t, hasThought(r, facetBeliefReversal, "netzero"),
		"a net-zero old side has no net polarity and must NOT flag a reversal")
}

// TestClassifyBlindSpots_MachineGenreExcluded (FAILS-WHEN-ABSENT): a dream-source
// thought never appears in any facet and is excluded from TotalThoughts.
func TestClassifyBlindSpots_MachineGenreExcluded(t *testing.T) {
	charges := map[string][]*knowledgev1.Node{
		// Dream thought with a single heavy charge — would be confident_untested if
		// it were human-genre.
		"dream": {mkCharge("d0", "positive", 6, fixedNow)},
		"human": {mkCharge("h0", "positive", 6, fixedNow)},
	}
	nodeByID := map[string]*knowledgev1.Node{
		"dream": mkThought("dream", "dream:analyze", "main"),
		"human": mkThought("human", "", "implementer"),
	}
	r := classifyForTest([]string{"dream", "human"}, charges, nil, nodeByID, nil, fixedNow)

	assert.Equal(t, 1, r.TotalThoughts, "only the human-genre thought is considered")
	for _, f := range r.Facets {
		assert.False(t, hasThought(r, f.Key, "dream"),
			"a dream-source thought must not appear in facet %s", f.Key)
	}
	assert.True(t, hasThought(r, facetConfidentUntested, "human"),
		"the human-genre thought still classifies")
}

// TestClassifyBlindSpots_Determinism (FAILS-WHEN-ABSENT): the same input classified
// twice yields identical facet ordering, and each facet caps at blindSpotFacetCap.
func TestClassifyBlindSpots_Determinism(t *testing.T) {
	// More than blindSpotFacetCap zero-charge high-influence thoughts → facet 2 caps.
	const total = blindSpotFacetCap + 5
	ids := make([]string, total)
	charges := map[string][]*knowledgev1.Node{}
	influence := map[string]float64{}
	nodeByID := map[string]*knowledgev1.Node{}
	for i := range ids {
		id := fmt.Sprintf("t%02d", i)
		ids[i] = id
		influence[id] = float64(i+1) * 0.01 // distinct influences for a stable order.
		nodeByID[id] = mkThought(id, "", "implementer")
	}

	r1 := classifyForTest(ids, charges, influence, nodeByID, nil, fixedNow)
	r2 := classifyForTest(ids, charges, influence, nodeByID, nil, fixedNow)

	foundational := facetItems(r1, facetFoundationalUnexamined)
	require.Len(t, foundational, blindSpotFacetCap, "facet 2 caps at blindSpotFacetCap")

	// Identical ordering across runs.
	items2 := facetItems(r2, facetFoundationalUnexamined)
	require.Len(t, items2, blindSpotFacetCap)
	for i := range foundational {
		assert.Equal(t, foundational[i].ThoughtID, items2[i].ThoughtID,
			"facet ordering is deterministic across runs at index %d", i)
	}
	// Influence-desc ordering: the highest-influence thought ranks first.
	assert.Equal(t, "t14", foundational[0].ThoughtID,
		"highest-influence thought ranks first under the cap")
}

// facetGroups returns the Groups of the named facet from a report (nil when absent).
func facetGroups(r BlindSpotReport, key string) []BlindSpotGroup {
	for _, f := range r.Facets {
		if f.Key == key {
			return f.Groups
		}
	}
	return nil
}

// hasGroup reports whether the named facet contains a group for the given key.
func hasGroup(r BlindSpotReport, facetKey, groupKey string) bool {
	for _, g := range facetGroups(r, facetKey) {
		if g.Key == groupKey {
			return true
		}
	}
	return false
}

// classifyWithClusters runs the full classifier including the cluster/topic-level
// reversal view (no topic grouping → each cluster is its own unit).
func classifyWithClusters(
	charges map[string][]*knowledgev1.Node,
	nodeByID map[string]*knowledgev1.Node,
	clusters []ThoughtCluster,
	topics TopicGrouping,
	now time.Time,
) BlindSpotReport {
	ids := make([]string, 0, len(nodeByID))
	for id := range nodeByID {
		ids = append(ids, id)
	}
	return classifyBlindSpots(blindSpotInputs{
		thoughtIDs: ids,
		charges:    charges,
		nodeByID:   nodeByID,
		clusters:   clusters,
		topics:     topics,
		now:        now,
	})
}

// TestClassifyClusterReversals_Flags (FAILS-WHEN-ABSENT): a cluster whose POOLED
// member charges net negative in the old window and positive in the recent window,
// with old mass above the density gate, surfaces as a facet-5 Group.
func TestClassifyClusterReversals_Flags(t *testing.T) {
	old := fixedNow.Add(-(blindSpotReversalRecentWindow + 24*time.Hour))
	recent := fixedNow.Add(-1 * time.Hour)
	// Two members; pooled old = -8 (negative), pooled recent = +9 (positive) → flip,
	// and |old| = 8 >= blindSpotReversalMinOldMass (6).
	charges := map[string][]*knowledgev1.Node{
		"m0": {mkCharge("m0o", "negative", 5, old), mkCharge("m0r", "positive", 4, recent)},
		"m1": {mkCharge("m1o", "negative", 3, old), mkCharge("m1r", "positive", 5, recent)},
	}
	nodeByID := map[string]*knowledgev1.Node{
		"m0": mkThought("m0", "", "implementer"),
		"m1": mkThought("m1", "", "implementer"),
	}
	clusters := []ThoughtCluster{{ID: "C", Label: "auth cluster", ThoughtIDs: []string{"m0", "m1"}, Size: 2}}

	r := classifyWithClusters(charges, nodeByID, clusters, TopicGrouping{}, fixedNow)

	groups := facetGroups(r, facetBeliefReversal)
	require.Len(t, groups, 1, "the reversed cluster surfaces as one group")
	g := groups[0]
	assert.Equal(t, "C", g.Key)
	assert.Equal(t, "auth cluster", g.Label, "raw cluster label is the fallback when no topic doc")
	assert.Negative(t, g.OldNet, "old side nets negative")
	assert.Positive(t, g.RecentNet, "recent side nets positive")
	assert.ElementsMatch(t, []string{"m0", "m1"}, g.Members, "both charged members drive the reversal")
}

// TestClassifyClusterReversals_BelowDensityGate (FAILS-WHEN-ABSENT): a cluster that
// flips old→recent but carries less than blindSpotReversalMinOldMass on the old
// side does NOT flag — a sparse flip can't masquerade as a topic reversal.
func TestClassifyClusterReversals_BelowDensityGate(t *testing.T) {
	old := fixedNow.Add(-(blindSpotReversalRecentWindow + 24*time.Hour))
	recent := fixedNow.Add(-1 * time.Hour)
	// Pooled old = -3 (|old| = 3 < gate 6) even though it flips to recent +4.
	charges := map[string][]*knowledgev1.Node{
		"s0": {mkCharge("s0o", "negative", 3, old), mkCharge("s0r", "positive", 4, recent)},
	}
	nodeByID := map[string]*knowledgev1.Node{"s0": mkThought("s0", "", "implementer")}
	clusters := []ThoughtCluster{{ID: "C", Label: "sparse cluster", ThoughtIDs: []string{"s0"}, Size: 1}}

	r := classifyWithClusters(charges, nodeByID, clusters, TopicGrouping{}, fixedNow)

	assert.False(t, hasGroup(r, facetBeliefReversal, "C"),
		"a below-density-gate flip must NOT surface as a topic/cluster reversal")
}

// TestClassifyClusterReversals_NoFlip (FAILS-WHEN-ABSENT): a cluster whose pooled
// charges are the same polarity old and recent (no flip) does NOT flag, even with
// ample mass.
func TestClassifyClusterReversals_NoFlip(t *testing.T) {
	old := fixedNow.Add(-(blindSpotReversalRecentWindow + 24*time.Hour))
	recent := fixedNow.Add(-1 * time.Hour)
	// Old +8, recent +6 → same polarity, no reversal.
	charges := map[string][]*knowledgev1.Node{
		"m0": {mkCharge("m0o", "positive", 8, old), mkCharge("m0r", "positive", 6, recent)},
	}
	nodeByID := map[string]*knowledgev1.Node{"m0": mkThought("m0", "", "implementer")}
	clusters := []ThoughtCluster{{ID: "C", Label: "stable cluster", ThoughtIDs: []string{"m0"}, Size: 1}}

	r := classifyWithClusters(charges, nodeByID, clusters, TopicGrouping{}, fixedNow)

	assert.False(t, hasGroup(r, facetBeliefReversal, "C"),
		"a same-polarity pooled history must NOT surface as a reversal")
}

// TestClassifyClusterReversals_TopicRollup (FAILS-WHEN-ABSENT): two Leiden clusters
// sharing a topic key pool together — neither clears the density gate alone, but the
// rolled-up topic unit does, and surfaces under the topic key + label.
func TestClassifyClusterReversals_TopicRollup(t *testing.T) {
	old := fixedNow.Add(-(blindSpotReversalRecentWindow + 24*time.Hour))
	recent := fixedNow.Add(-1 * time.Hour)
	// Each cluster contributes old -4 (below the gate of 6 alone); pooled old -8 clears it.
	charges := map[string][]*knowledgev1.Node{
		"a0": {mkCharge("a0o", "negative", 4, old), mkCharge("a0r", "positive", 5, recent)},
		"b0": {mkCharge("b0o", "negative", 4, old), mkCharge("b0r", "positive", 5, recent)},
	}
	nodeByID := map[string]*knowledgev1.Node{
		"a0": mkThought("a0", "", "implementer"),
		"b0": mkThought("b0", "", "implementer"),
	}
	clusters := []ThoughtCluster{
		{ID: "cA", Label: "cluster A", ThoughtIDs: []string{"a0"}, Size: 1},
		{ID: "cB", Label: "cluster B", ThoughtIDs: []string{"b0"}, Size: 1},
	}
	// cB rolls into cA's topic; cA is the primary key carrying the summary label.
	topics := TopicGrouping{
		TopicOf: map[string]string{"cA": "cA", "cB": "cA"},
		Labels:  map[string]string{"cA": "Authentication topic"},
	}

	r := classifyWithClusters(charges, nodeByID, clusters, topics, fixedNow)

	groups := facetGroups(r, facetBeliefReversal)
	require.Len(t, groups, 1, "the two clusters roll into one topic group")
	g := groups[0]
	assert.Equal(t, "cA", g.Key, "the topic primary key is the group key")
	assert.Equal(t, "Authentication topic", g.Label, "the topic summary is the group label")
	assert.ElementsMatch(t, []string{"a0", "b0"}, g.Members, "both clusters' members pool into the topic reversal")
}
