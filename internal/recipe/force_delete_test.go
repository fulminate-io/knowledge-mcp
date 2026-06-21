// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func evidenceFor(slug string) string {
	b, err := json.Marshal(map[string]string{"source": slug})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestForceDeleteBySource_OnlyCurrentSlug_OneHardDelete(t *testing.T) {
	// Target graph holds three patterns: p1+p2 from slug "alpha", p3 from
	// "beta". A force-delete for "alpha" must doom exactly p1 and p2 and issue
	// ONE hard delete carrying both ids.
	f := &fakeGraphCaller{
		nodes: []*knowledgev1.Node{
			{Id: "p1", Type: "pattern"},
			{Id: "p2", Type: "pattern"},
			{Id: "p3", Type: "pattern"},
		},
		edges: []*knowledgev1.Edge{
			{FromId: "p1", ToId: "s_a1", Type: string(kgtypes.EdgeTranslatedFrom), Evidence: evidenceFor("alpha")},
			{FromId: "p2", ToId: "s_a2", Type: string(kgtypes.EdgeTranslatedFrom), Evidence: evidenceFor("alpha")},
			{FromId: "p3", ToId: "s_b1", Type: string(kgtypes.EdgeTranslatedFrom), Evidence: evidenceFor("beta")},
		},
	}
	target := TargetSpec{GraphType: kgtypes.GraphPractice, Name: "design-patterns"}

	n, err := forceDeleteBySource(context.Background(), f, target, "alpha", []string{"pattern"})
	require.NoError(t, err)
	assert.Equal(t, 2, n, "only the two alpha-sourced nodes are deleted")

	// Exactly one DELETE mutation carrying both doomed ids — not N per-id deletes.
	require.Len(t, f.mutations, 1, "exactly one delete mutation")
	m := f.mutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, m.GetKind())
	assert.ElementsMatch(t, []string{"p1", "p2"}, m.GetSelection().GetIds())

	// HardDelete MUST be true — a soft default would tombstone and collide with
	// the re-emitted StableID on a Force re-run.
	assert.True(t, m.GetHardDelete(), "force-delete must hard-delete")
}

func TestForceDeleteBySource_EmptySlug_NoOp(t *testing.T) {
	f := &fakeGraphCaller{}
	n, err := forceDeleteBySource(context.Background(), f, TargetSpec{}, "", []string{"pattern"})
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, f.mutations)
	assert.Zero(t, f.calls, "empty slug short-circuits before any RPC")
}

func TestForceDeleteBySource_NoMatches_NoDelete(t *testing.T) {
	// Target nodes exist but none carry the requested slug → no delete mutation.
	f := &fakeGraphCaller{
		nodes: []*knowledgev1.Node{{Id: "p3", Type: "pattern"}},
		edges: []*knowledgev1.Edge{
			{FromId: "p3", ToId: "s_b1", Type: string(kgtypes.EdgeTranslatedFrom), Evidence: evidenceFor("beta")},
		},
	}
	n, err := forceDeleteBySource(context.Background(), f, TargetSpec{GraphType: kgtypes.GraphPractice, Name: "dp"}, "alpha", []string{"pattern"})
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, f.mutations, "no matching source → no delete issued")
}
