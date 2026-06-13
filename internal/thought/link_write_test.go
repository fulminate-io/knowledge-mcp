// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// linkRecordingCaller serves the bulk relates-to pre-read with a scripted edge set
// and records every MUTATION_KIND_LINK the materializer writes.
type linkRecordingCaller struct {
	existingEdges []*knowledgev1.Edge
	links         []linkWrite
}

type linkWrite struct {
	from, to, method, relationship string
}

func (c *linkRecordingCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if q := req.GetQuery(); q != nil {
		if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
			return &knowledgev1.ExecuteResponse{Edges: c.existingEdges}, nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if m := req.GetMutation(); m != nil && m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_LINK {
		spec := m.GetEdgeSpec()
		from := ""
		if ids := m.GetSelection().GetIds(); len(ids) > 0 {
			from = ids[0]
		}
		c.links = append(c.links, linkWrite{
			from:         from,
			to:           spec.GetToId(),
			method:       spec.GetMethod(),
			relationship: spec.GetRelationship(),
		})
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestGroupSimilarity_LinkWrite: a link-candidate pair with no existing edge
// produces exactly one MUTATION_KIND_LINK (relates-to, medoidA→medoidB) carrying
// Method="topic-similarity" + a LinksCreated entry.
func TestGroupSimilarity_LinkWrite(t *testing.T) {
	rec := &linkRecordingCaller{}
	links := []LinkCandidate{{MedoidA: "mA", MedoidB: "mB", Score: 0.95}}

	rep, err := MaterializeLinks(context.Background(), rec, links)
	require.NoError(t, err)

	require.Len(t, rec.links, 1, "exactly one link edge must be written")
	w := rec.links[0]
	assert.Equal(t, "mA", w.from)
	assert.Equal(t, "mB", w.to)
	assert.Equal(t, "relates-to", w.relationship)
	assert.Equal(t, "topic-similarity", w.method, "the provenance Method tag is required")
	assert.Len(t, rep.Created, 1)
	assert.Equal(t, 0, rep.AlreadyLinked)
}

// TestGroupSimilarity_LinkIdempotent: a pair whose medoids already share a
// relates-to edge writes nothing and is reported as already-linked.
func TestGroupSimilarity_LinkIdempotent(t *testing.T) {
	rec := &linkRecordingCaller{
		existingEdges: []*knowledgev1.Edge{
			{Type: string(kgtypes.EdgeRelatesTo), FromId: "mB", ToId: "mA"}, // reverse direction
		},
	}
	links := []LinkCandidate{{MedoidA: "mA", MedoidB: "mB", Score: 0.95}}

	rep, err := MaterializeLinks(context.Background(), rec, links)
	require.NoError(t, err)

	assert.Empty(t, rec.links, "an already-linked pair must write no edge (idempotent, either direction)")
	assert.Empty(t, rep.Created)
	assert.Equal(t, 1, rep.AlreadyLinked)
}

// TestRunGroupSimilarity_MergeWritesNoLinkEdge: a merge-band pair is unioned by the
// cascade and never appears as a link candidate; only a link-band pair gets an
// edge. The merged-away medoid is absent from any link write.
func TestRunGroupSimilarity_MergeWritesNoLinkEdge(t *testing.T) {
	// Topics: M1 and M2 are near-identical (will merge); L is link-close to the
	// merged survivor but below the merge threshold; F is far (neither).
	merged1 := newVec([2]int{0, 99})
	merged2 := newVec([2]int{0, 99}) // identical to merged1 → sim 1.0 → merges
	// L differs from the merged centroid (=0-99) by ~20 bits → sim ~0.92: above
	// link (0.90) but below merge (0.97).
	linkVec := newVec([2]int{0, 99})
	for i := 100; i < 120; i++ { // +20 bits of difference
		linkVec[i/8] |= 1 << uint(i%8)
	}
	farVec := newVec([2]int{200, 255})

	idx := map[string][]byte{
		"m1": merged1, "m2": merged2, "l": linkVec, "f": farVec,
	}
	ca := map[string]int64{"m1": 1, "m2": 2, "l": 3, "f": 4}
	topics := []Topic{
		{PrimaryClusterID: "M1", MemberClusters: []string{"M1"}, MemberThoughtIDs: []string{"m1"}, Centroid: merged1, MedoidID: "m1"},
		{PrimaryClusterID: "M2", MemberClusters: []string{"M2"}, MemberThoughtIDs: []string{"m2"}, Centroid: merged2, MedoidID: "m2"},
		{PrimaryClusterID: "L", MemberClusters: []string{"L"}, MemberThoughtIDs: []string{"l"}, Centroid: linkVec, MedoidID: "l"},
		{PrimaryClusterID: "F", MemberClusters: []string{"F"}, MemberThoughtIDs: []string{"f"}, Centroid: farVec, MedoidID: "f"},
	}

	const linkThreshold = 0.90
	const mergeThreshold = 0.97

	// Cascade first → M1+M2 merge into one survivor; L and F untouched.
	survivors, chains := RunMergeCascade(topics, idx, ca, mergeThreshold)
	require.Len(t, chains, 1, "exactly one merge (M1+M2)")
	require.Len(t, survivors, 3, "survivors: merged-M + L + F")

	// Link classification over survivors, then materialize.
	candidates := RunGroupSimilarity(survivors, linkThreshold)
	rec := &linkRecordingCaller{}
	_, err := MaterializeLinks(context.Background(), rec, candidates)
	require.NoError(t, err)

	// The merged pair (m1↔m2) must never be a link edge — they are one topic now.
	for _, w := range rec.links {
		if (w.from == "m1" && w.to == "m2") || (w.from == "m2" && w.to == "m1") {
			t.Fatalf("the merge-band pair m1↔m2 was written as a link edge — it must be absorbed by the cascade")
		}
	}
	// The survivor↔L pair should be the link edge (survivor medoid is m1 or m2).
	require.Len(t, rec.links, 1, "exactly one link edge (survivor ↔ L)")
	w := rec.links[0]
	if w.to != "l" && w.from != "l" {
		t.Fatalf("the link edge does not involve L: %+v", w)
	}
}

// TestTopicSimilarityEdge_NotTensionEligible (tensions EXCLUSION stance): two
// opposite-valence medoid thoughts joined by a topic-similarity
// relates-to edge now yield ZERO tensions — fetchTensionEdges pre-filters every
// machine-Method edge (isMachineTensionMethod) out of the tension predicate. A
// topic-similarity medoid link is clustering signal, not propositional
// disagreement, so it must never read as a tension. The edge still carries
// Method="topic-similarity" — that machine provenance is what the filter excludes
// on.
func TestTopicSimilarityEdge_NotTensionEligible(t *testing.T) {
	// A topic-similarity relates-to edge between the two opposite-valence thoughts.
	edge := &knowledgev1.Edge{
		Type:   string(kgtypes.EdgeRelatesTo),
		FromId: "A",
		ToId:   "B",
		Method: topicSimilarityMethod,
	}
	f := newTensionFake([]*knowledgev1.Edge{edge})

	tensions, err := ReflectTensions(context.Background(), f)
	require.NoError(t, err)
	assert.Empty(t, tensions,
		"a topic-similarity (machine) relates-to edge must NOT surface a tension — it is clustering signal, not disagreement")

	// Provenance: the excluded edge carries the topic-similarity machine tag.
	assert.Equal(t, topicSimilarityMethod, edge.Method,
		"the topic-similarity link edge carries the machine provenance the tension filter excludes on")
}
