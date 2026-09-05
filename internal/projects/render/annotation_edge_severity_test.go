// SPDX-License-Identifier: Apache-2.0

package render_test

// annotation_edge_severity_test.go covers the READ half of the severity-on-the-
// edge requirement: a section's review state is answerable from the section's own
// edges, with no annotation node hydrated.
//
// THE FIXTURE OMITS THE ANNOTATION NODES ENTIRELY, and that is the whole design
// of this test rather than a shortcut. A fixture that held them would let a read
// that hydrates every peer pass just as easily as one that reads the edge, and
// the two are exactly what this requirement distinguishes. With no annotation
// node in the graph at all, anything the read reports about kind or tier can only
// have come off the edge.
//
// The WRITE half — the create path stamping those two facts onto the edge it was
// asked to make — is TestMutate_PlanAnnotationEdgeCarriesKindAndTier in package
// tools, where the write path lives.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// TestSectionAnnotationSeverity_ReadableFromEdgesAlone is the requirement: an
// edge-metadata read from the SECTION reports each annotation's kind and tier
// without any annotation node existing to hydrate.
func TestSectionAnnotationSeverity_ReadableFromEdgesAlone(t *testing.T) {
	args := projects.PlanArgs{Name: "chunked", Goal: "g", Summary: "s", Sections: []projects.SectionArgs{
		{Name: "What to test", Body: "the body", Summary: "what to test"},
	}}
	w := newWireFixture()
	ids := seedFromBuilder(t, w, args, "plan-sev")
	sectionID := ids[1]

	// THREE EDGES, NO NODES. Each carries its own severity, and none of the three
	// annotations exists as a node anywhere in this fixture.
	for _, a := range []struct {
		id, kind, tier string
	}{
		{"ann-a", kgtypes.AnnotationKindFinding, "T1"},
		{"ann-b", kgtypes.AnnotationKindFinding, "T3"},
		{"ann-c", kgtypes.AnnotationKindCorrect, ""},
	} {
		evidence, err := kgtypes.MarshalAnnotationEdgeSeverity(a.kind, a.tier)
		require.NoError(t, err)
		w.addEdge(a.id, sectionID, string(kgtypes.EdgeRelatesTo), kgtypes.AnnotationEdgeMethod, evidence)
	}
	require.NotContains(t, w.nodes, "ann-a",
		"the fixture must hold NO annotation node, or a hydrating read would pass this test too")

	// The read: incoming relates-to edges of the section, with their metadata.
	edges, err := render.IterEdgesFor(context.Background(), w, []string{sectionID},
		kgwire.IncomingEdges, kgtypes.EdgeRelatesTo)
	require.NoError(t, err)
	require.Len(t, edges, 3)

	tiers := map[string]string{}
	kinds := map[string]string{}
	for _, e := range edges {
		require.Equal(t, kgtypes.AnnotationEdgeMethod, e.Method,
			"the method is what tells an annotation edge apart from any other relates-to edge")
		kind, tier, ok := kgtypes.ParseAnnotationEdgeSeverity(e.Evidence)
		require.True(t, ok, "every annotation edge carries a readable severity")
		kinds[e.FromId] = kind
		tiers[e.FromId] = tier
	}
	assert.Equal(t, map[string]string{"ann-a": "T1", "ann-b": "T3", "ann-c": ""}, tiers,
		"the TIER is readable per edge — this is the read the requirement names")
	assert.Equal(t, map[string]string{
		"ann-a": kgtypes.AnnotationKindFinding,
		"ann-b": kgtypes.AnnotationKindFinding,
		"ann-c": kgtypes.AnnotationKindCorrect,
	}, kinds)
}

// TestSectionAnnotationSeverity_PlainRelatesToIsNotAnAnnotation is the control.
// relates-to is the graph's most common edge and anything at all may point at a
// section with one, so the read must not report an unrelated peer as review
// state. Without this, a reader that treated every incoming relates-to edge as an
// annotation would pass the test above.
func TestSectionAnnotationSeverity_PlainRelatesToIsNotAnAnnotation(t *testing.T) {
	args := projects.PlanArgs{Name: "chunked", Goal: "g", Summary: "s", Sections: []projects.SectionArgs{
		{Name: "Touch points", Body: "b", Summary: "s"},
	}}
	w := newWireFixture()
	ids := seedFromBuilder(t, w, args, "plan-ctl")
	sectionID := ids[1]

	// An ordinary cross-reference: relates-to, no method, no evidence.
	w.addEdge("some-other-node", sectionID, string(kgtypes.EdgeRelatesTo), "", "")

	edges, err := render.IterEdgesFor(context.Background(), w, []string{sectionID},
		kgwire.IncomingEdges, kgtypes.EdgeRelatesTo)
	require.NoError(t, err)
	require.Len(t, edges, 1)

	assert.NotEqual(t, kgtypes.AnnotationEdgeMethod, edges[0].Method)
	_, _, ok := kgtypes.ParseAnnotationEdgeSeverity(edges[0].Evidence)
	assert.False(t, ok, "an edge carrying no severity reports none — a soft miss, never a zero-value kind")
}

// TestAnnotationEdgeSeverity_RoundTrip pins the payload itself, including the two
// shapes a reader must tell apart: a kind with no tier, which is legitimate for
// `correct`, and an absent severity, which is every other relates-to edge.
func TestAnnotationEdgeSeverity_RoundTrip(t *testing.T) {
	t.Run("kind and tier survive verbatim", func(t *testing.T) {
		blob, err := kgtypes.MarshalAnnotationEdgeSeverity(kgtypes.AnnotationKindNeededChange, "T2")
		require.NoError(t, err)
		kind, tier, ok := kgtypes.ParseAnnotationEdgeSeverity(blob)
		require.True(t, ok)
		assert.Equal(t, kgtypes.AnnotationKindNeededChange, kind)
		assert.Equal(t, "T2", tier)
	})
	t.Run("a kind with no tier is still a severity", func(t *testing.T) {
		blob, err := kgtypes.MarshalAnnotationEdgeSeverity(kgtypes.AnnotationKindCorrect, "")
		require.NoError(t, err)
		kind, tier, ok := kgtypes.ParseAnnotationEdgeSeverity(blob)
		require.True(t, ok, "correct carries no tier and is not thereby severity-less")
		assert.Equal(t, kgtypes.AnnotationKindCorrect, kind)
		assert.Empty(t, tier)
	})
	for _, bad := range []string{"", "not json", `{"annotation_tier":"T1"}`, `{}`} {
		t.Run("a miss is soft: "+bad, func(t *testing.T) {
			_, _, ok := kgtypes.ParseAnnotationEdgeSeverity(bad)
			assert.False(t, ok, "absent, unparseable or kind-less evidence is a miss, never an error and never a zero kind")
		})
	}
}
