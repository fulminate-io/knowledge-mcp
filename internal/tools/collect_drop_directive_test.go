// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestRawCollectDropDirective_FamiliesAndTarget fences the two things a drop
// directive can get wrong, and they are different kinds of wrong.
//
// WHICH FAMILIES. A directive that leaks onto a non-raw collect tells an
// operator to drop the durable artifact they just built — a code graph, a
// practice graph, a cloud inventory. The family table below is referenced
// THROUGH kgtypes constants, so renaming one breaks compilation rather than
// silently dropping a row, and it is checked against the canonical declared
// population so a NEW family forces a decision here instead of defaulting into
// the silent branch.
//
// WHICH TARGET. A directive naming the collect id instead of the produced graph
// renders a call that cannot resolve. For pdf the two are not even the same kind
// of string: the id is a filesystem path and the graph name is a
// basename-plus-content-hash slug.
func TestRawCollectDropDirective_FamiliesAndTarget(t *testing.T) {
	t.Parallel()

	// The family table. A raw family owes a directive; every other family owes
	// silence.
	wantsDirective := map[kgtypes.GraphType]bool{
		kgtypes.GraphKnowledge: false,
		kgtypes.GraphCode:      false,
		kgtypes.GraphCloud:     false,
		kgtypes.GraphCICD:      false,
		kgtypes.GraphPractice:  false,
		kgtypes.GraphLinkage:   false,
		kgtypes.GraphChecks:    false,
		kgtypes.GraphLogs:      false,
		kgtypes.GraphWebRaw:    true,
		kgtypes.GraphPDFRaw:    true,
	}

	t.Run("the_table_covers_every_declared_family", func(t *testing.T) {
		// THE POPULATION GUARD. BuiltinGraphTypeNames projects the canonical
		// allGraphTypes slice, so this compares the table against the declaration
		// itself rather than against a number copied out of it. A family added to
		// the client and not classified here fails HERE, with its name, instead
		// of silently taking the no-directive branch.
		declared := kgtypes.BuiltinGraphTypeNames()
		var classified []string
		for gt := range wantsDirective {
			classified = append(classified, string(gt))
		}
		assert.ElementsMatch(t, declared, classified,
			"every declared graph family must be classified as owing a drop directive or not")
	})

	t.Run("exactly_the_two_raw_families_get_a_directive", func(t *testing.T) {
		const produced = "produced-slug"
		var got int
		for gt, want := range wantsDirective {
			d := rawCollectDropDirective(string(gt), produced)
			if !want {
				assert.Empty(t, d,
					"%s is not a raw document family and must carry no drop directive", gt)
				continue
			}
			got++
			require.NotEmpty(t, d, "%s is a raw document family and must carry a drop directive", gt)
			assert.Contains(t, d, produced, "%s directive must name the produced graph", gt)
			assert.Contains(t, d, string(gt), "%s directive must name its own family", gt)
		}
		// Known-positive on the loop itself: a table that silently lost both raw
		// rows would satisfy every assertion above without measuring anything.
		assert.Equal(t, 2, got, "control: both raw families were actually exercised")
	})

	t.Run("a_pdf_directive_names_the_slug_and_not_the_path", func(t *testing.T) {
		// The two strings a pdf collect carries: what it was ASKED for, and what
		// it PRODUCED. drop_graph resolves only the second.
		const id = "/Users/x/corpus/stopford.pdf"
		const slug = "stopford-81814990"
		d := rawCollectDropDirective(string(kgtypes.GraphPDFRaw), slug)
		require.NotEmpty(t, d)
		assert.Contains(t, d, slug, "the directive must name the produced slug")
		assert.NotContains(t, d, id,
			"the directive must never name the collect id — drop_graph cannot resolve a filesystem path")
	})

	t.Run("two_different_collects_yield_two_different_directives", func(t *testing.T) {
		a := rawCollectDropDirective(string(kgtypes.GraphWebRaw), "site-alpha")
		b := rawCollectDropDirective(string(kgtypes.GraphWebRaw), "site-beta")
		require.NotEmpty(t, a)
		require.NotEmpty(t, b)
		assert.NotEqual(t, a, b,
			"the directive is per-collect: a constant would tell every operator to drop the same graph")
	})

	t.Run("no_produced_graph_yields_no_directive", func(t *testing.T) {
		for _, gt := range []kgtypes.GraphType{kgtypes.GraphWebRaw, kgtypes.GraphPDFRaw} {
			assert.Empty(t, rawCollectDropDirective(string(gt), ""),
				"%s with no produced graph must yield nothing, never a directive with an empty target", gt)
		}
	})

	t.Run("the_produced_name_threaded_is_the_collector_s_own_not_the_id", func(t *testing.T) {
		// THE BOUNDARY LEG. Everything above tests the classifier against a
		// string a test chose. This drives the real construction end to end: a
		// registered collector reports a GraphName that differs from the id it
		// was called with, and builtinCollectWork must surface THAT.
		compositionStubOnce.Do(func() { collector.Register(compositionStubCollector{}) })
		deps := &detachFullDeps{rt: NewCollectRuntime(), gc: &fakeGraphCaller{}}

		_, produced, err := builtinCollectWork(context.Background(), deps,
			collectArgs{Type: compositionStubType, ID: "composition-id"},
			collector.CollectOptions{Sink: noopSink{}}, "")

		require.NoError(t, err)
		assert.Equal(t, "composition-smoke", produced,
			"the produced graph name must come from the collector's own result")
		assert.NotEqual(t, "composition-id", produced,
			"the collect id is not the produced graph name; a directive built on it would not resolve")
	})

	t.Run("the_directive_states_why_the_graph_is_temporary", func(t *testing.T) {
		// The wording is a reviewed deliverable rather than a gate on typing, so
		// this asserts only that the directive says the graph is droppable and
		// shows the call — not the sentence it says it in.
		d := rawCollectDropDirective(string(kgtypes.GraphWebRaw), "site-alpha")
		assert.Contains(t, d, "drop_graph",
			"the directive must show the manage(drop_graph) call an operator can run")
	})
}
