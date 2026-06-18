// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// personalityFixtureProfile builds a profile whose Scalars span two clusters (c1,
// c2) so ReflectPersonality emits a stubborn/gullible pair row, with ClusterLabels
// seeded to known sentinel text. The sentinel is what the ServesCache test looks for
// in the rendered output (it could only be there if the handler sourced clusters
// from the provider, not from a gc re-detect).
func personalityFixtureProfile() *clientthought.PersonalityProfile {
	return &clientthought.PersonalityProfile{
		Scalars: map[string]map[string]float64{
			"c1": {"c2": 0.5}, // stubborn (c1 resists c2)
			"c2": {"c1": 1.7}, // gullible (c2 open to c1)
		},
		ClusterLabels: map[string]string{
			"c1": "SENTINEL-cluster-one",
			"c2": "SENTINEL-cluster-two",
		},
	}
}

func personalityFixtureClusters() []clientthought.ThoughtCluster {
	return []clientthought.ThoughtCluster{
		{ID: "c1", Label: "SENTINEL-cluster-one", Size: 2, ThoughtIDs: []string{"t1", "t2"}},
		{ID: "c2", Label: "SENTINEL-cluster-two", Size: 1, ThoughtIDs: []string{"t3"}},
	}
}

// TestHandleReflectPersonality_ServesCache (FAILS-WHEN-ABSENT): the handler renders
// clusters sourced from the ClusterProvider — the sentinel labels appear in the
// output. The gc returns no topic docs (empty corpus), so ApplyTopicLabels leaves
// the provider's labels intact, proving the labels came from the provider, not a
// gc re-detect.
func TestHandleReflectPersonality_ServesCache(t *testing.T) {
	deps := interceptTestDeps{
		gc:              &reflectFakeCaller{}, // no docs → ApplyTopicLabels is a no-op
		clusters:        personalityFixtureClusters(),
		clusterProfile:  personalityFixtureProfile(),
		clusterComputed: true,
	}
	text := resultText(handleReflectPersonality(context.Background(), deps, queryReflectArgs{}))
	assert.Contains(t, text, "SENTINEL-cluster-one", "render reflects the provider's cluster label (provider-sourced)")
	assert.Contains(t, text, "SENTINEL-cluster-two", "render reflects the second provider cluster label")
}

// TestHandleReflectPersonality_ColdStart (FAILS-WHEN-ABSENT): computed=false renders
// the cold cluster-state message, not an empty profile.
func TestHandleReflectPersonality_ColdStart(t *testing.T) {
	deps := interceptTestDeps{clusterComputed: false}
	text := resultText(handleReflectPersonality(context.Background(), deps, queryReflectArgs{}))
	assert.Equal(t, coldClusterStateMessage, text, "cold cache renders the not-yet-computed message")
}

// TestHandleReflectPersonality_NilProvider (FAILS-WHEN-ABSENT): a nil ClusterProvider
// renders the loop-not-running message rather than panicking or recomputing.
func TestHandleReflectPersonality_NilProvider(t *testing.T) {
	deps := interceptTestDeps{clusterProviderNil: true}
	text := resultText(handleReflectPersonality(context.Background(), deps, queryReflectArgs{}))
	assert.Contains(t, text, "reflection loop is not running", "nil provider renders the loop-not-running message")
}

// TestHandleReflectPersonality_ClusterFilter (param-coverage): the personality
// `cluster` filter still reaches ReflectPersonality post-repoint. With a profile
// spanning c1 and c2, filtering on c1 yields only rows whose ClusterA == c1.
func TestHandleReflectPersonality_ClusterFilter(t *testing.T) {
	deps := interceptTestDeps{
		gc:              &reflectFakeCaller{},
		clusters:        personalityFixtureClusters(),
		clusterProfile:  personalityFixtureProfile(),
		clusterComputed: true,
	}
	text := resultText(handleReflectPersonality(context.Background(), deps, queryReflectArgs{Cluster: "c1"}))
	// c1's only pair targets c2 → the c1->c2 row appears; the c2->c1 row (ClusterA=c2)
	// must be filtered out. Each row renders "LabelA -> LabelB"; with the filter only
	// the c1-origin row survives, so cluster-two must NOT appear as a row ORIGIN.
	assert.Contains(t, text, "SENTINEL-cluster-one -> SENTINEL-cluster-two",
		"the c1-origin pair survives the cluster:c1 filter")
	assert.NotContains(t, text, "SENTINEL-cluster-two -> SENTINEL-cluster-one",
		"the c2-origin pair is filtered out by cluster:c1")
}

// TestHandleReflectPersonality_DoesNotMutateProvider (SHARED-CACHE COPY LOCK): the
// handler must NOT mutate the provider's backing slice or profile. The gc returns a
// topic doc that ApplyTopicLabels WOULD apply (overwriting clusters[i].Label +
// profile.ClusterLabels[c1]); a non-defensive handler would corrupt the shared
// fixture. After the call, the fixture's cluster Labels and the profile's
// ClusterLabels map must be UNCHANGED — proving the Phase 3 defensive copy + the
// ClusterLabels map clone landed. Catches both the slice-element race and the
// concurrent-map-write hazard structurally; the -race package run is the secondary lock.
func TestHandleReflectPersonality_DoesNotMutateProvider(t *testing.T) {
	clusters := personalityFixtureClusters()
	profile := personalityFixtureProfile()
	// A topic doc for c1 → ApplyTopicLabels would relabel c1 to "Topic Override".
	gc := &reflectFakeCaller{
		docs: []*knowledgev1.Node{reflectTopicDoc("doc1", "c1", "c1", "Topic Override")},
	}
	deps := interceptTestDeps{gc: gc, clusters: clusters, clusterProfile: profile, clusterComputed: true}

	_ = handleReflectPersonality(context.Background(), deps, queryReflectArgs{})

	assert.Equal(t, "SENTINEL-cluster-one", clusters[0].Label,
		"the provider's backing cluster slice must NOT be mutated by ApplyTopicLabels (defensive slice copy)")
	assert.Equal(t, "SENTINEL-cluster-one", profile.ClusterLabels["c1"],
		"the provider's profile ClusterLabels map must NOT be mutated (defensive map clone — prevents concurrent-map-write)")
}

// --- summary cache-serve tests ---

// TestHandleReflectSummary_ServesCache (FAILS-WHEN-ABSENT): summary renders clusters
// from the ClusterProvider — the sentinel labels appear in the Top Clusters section.
func TestHandleReflectSummary_ServesCache(t *testing.T) {
	deps := interceptTestDeps{
		gc:              &reflectFakeCaller{}, // no topic docs → ApplyTopicLabels leaves the sentinel labels intact
		clusters:        personalityFixtureClusters(),
		clusterComputed: true,
	}
	text := resultText(handleReflectSummary(context.Background(), deps, queryReflectArgs{}))
	assert.Contains(t, text, "SENTINEL-cluster-one", "summary Top Clusters reflects the provider's cluster label (provider-sourced)")
}

// TestHandleReflectSummary_ColdStart (FAILS-WHEN-ABSENT): computed=false renders the
// cold cluster-state message.
func TestHandleReflectSummary_ColdStart(t *testing.T) {
	deps := interceptTestDeps{clusterComputed: false}
	text := resultText(handleReflectSummary(context.Background(), deps, queryReflectArgs{}))
	assert.Equal(t, coldClusterStateMessage, text, "cold cache renders the not-yet-computed message")
}

// TestHandleReflectSummary_NilProvider (FAILS-WHEN-ABSENT): a nil ClusterProvider
// renders a NON-error loop-not-running message — NOT an errorResult. This pins the
// Phase 3 check-order reorder: the provider check must precede the gc-nil check, so
// a nil-gc fixture does not error before the provider check is reached.
func TestHandleReflectSummary_NilProvider(t *testing.T) {
	deps := interceptTestDeps{clusterProviderNil: true} // GraphCaller() is also nil here.
	res := handleReflectSummary(context.Background(), deps, queryReflectArgs{})
	assert.False(t, res.IsError, "nil provider returns a non-error loop-not-running message, not an error")
	assert.Contains(t, resultText(res), "reflection loop is not running",
		"nil provider renders the loop-not-running message")
}

// TestHandleReflectSummary_TopClustersSizeOrdered (CLUSTER-ORDER contract): the
// provider serves SIZE-VARIED clusters in NON-size order; default granularity must
// render Top Clusters Size-desc with an ID-asc tie-break (the order
// DetectPersistedClusters would have produced), proving the Phase 3 order-restore
// sort runs. It ALSO doubles as the summary-side copy lock: the fixture slice's
// element order must be UNCHANGED after the call (the handler sorted a COPY).
func TestHandleReflectSummary_TopClustersSizeOrdered(t *testing.T) {
	// Scrambled input order; expected render order is a-big(9), c-big(9), c-mid(5), c-small(2).
	scrambled := []clientthought.ThoughtCluster{
		{ID: "c-small", Label: "L-small", Size: 2},
		{ID: "c-big", Label: "L-cbig", Size: 9},
		{ID: "c-mid", Label: "L-mid", Size: 5},
		{ID: "a-big", Label: "L-abig", Size: 9},
	}
	deps := interceptTestDeps{
		gc:              reflectCorpus(), // non-empty corpus so TotalThoughts>0 path runs
		clusters:        scrambled,
		clusterComputed: true,
	}
	text := resultText(handleReflectSummary(context.Background(), deps, queryReflectArgs{}))

	// Assert the rendered Top Clusters order: a-big before c-big (ID tie-break at Size 9),
	// then c-mid (5), then c-small (2).
	iAbig := strings.Index(text, "L-abig")
	iCbig := strings.Index(text, "L-cbig")
	iMid := strings.Index(text, "L-mid")
	iSmall := strings.Index(text, "L-small")
	assert.Greater(t, iAbig, -1, "a-big row present")
	assert.Less(t, iAbig, iCbig, "a-big precedes c-big (ID-asc tie-break at equal Size 9)")
	assert.Less(t, iCbig, iMid, "c-big (9) precedes c-mid (5) — Size desc")
	assert.Less(t, iMid, iSmall, "c-mid (5) precedes c-small (2) — Size desc")

	// Copy lock: the handler must have sorted a COPY, leaving the fixture slice order intact.
	assert.Equal(t, "c-small", scrambled[0].ID, "fixture slice order unchanged (handler sorted a copy)")
	assert.Equal(t, "c-big", scrambled[1].ID, "fixture slice order unchanged")
	assert.Equal(t, "c-mid", scrambled[2].ID, "fixture slice order unchanged")
	assert.Equal(t, "a-big", scrambled[3].ID, "fixture slice order unchanged")
}
