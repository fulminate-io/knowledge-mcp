// SPDX-License-Identifier: Apache-2.0

package tools

// rebuild_scanned_nothing_test.go pins the zero-scan explanation.
//
// THE SENTENCE THIS REPLACES WAS FALSE AND IT MISLED A REAL OPERATOR: a graph
// holding 2556 embedded nodes was told it "has no embedded nodes to rebuild
// segments from". out.Scanned is watermark-relative, so it reads zero on an
// already-drained graph while the corpus is full — an absence reported without
// the filter that produced it. Each case below asserts the message states what
// was actually checked.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// rebuildStatsDeps is a ClientDeps whose GraphCaller answers Stats from a canned
// GraphStats, so the corpus counts the message discriminates on are programmable.
type rebuildStatsDeps struct {
	*interceptDeps
	stats *knowledgev1.GraphStats
}

func (d *rebuildStatsDeps) GraphCaller() GraphCaller {
	if d.stats == nil {
		// A genuinely-nil GraphCaller, which is what a router-less client returns.
		// NOT interceptDeps' typed-nil *GraphClient: that satisfies the statsRPC
		// assertion and then panics on the call, which is a fixture artifact rather
		// than anything production can produce.
		return nil
	}
	return rebuildStatsCaller{stats: d.stats}
}

type rebuildStatsCaller struct{ stats *knowledgev1.GraphStats }

func (rebuildStatsCaller) Execute(
	context.Context, *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

func (c rebuildStatsCaller) Stats(
	context.Context, *knowledgev1.StatsRequest,
) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{GraphStats: c.stats}, nil
}

func TestRebuildScannedNothing_StatesWhatItActuallyChecked(t *testing.T) {
	depsFor := func(nodes, embedded int32) ClientDeps {
		return &rebuildStatsDeps{
			interceptDeps: &interceptDeps{},
			stats:         &knowledgev1.GraphStats{NodeCount: nodes, BinaryVectorCount: embedded},
		}
	}

	t.Run("drained_graph_names_the_watermark_not_an_empty_corpus", func(t *testing.T) {
		// THE REGRESSION CASE, at the exact numbers that produced the false message.
		msg := rebuildScannedNothing(context.Background(), depsFor(3117, 2556),
			manageArgs{Graph: "practice", Name: "design-patterns"})
		assert.Contains(t, msg, "since the stored watermark", "the message must disclose the FILTER")
		assert.Contains(t, msg, "2556", "and state the embedded count it actually has")
		assert.Contains(t, msg, `"reset": true`, "and name the remedy that works")
		// THE FALSIFYING LEG: the old claim must be gone, not merely supplemented.
		assert.NotContains(t, msg, "no embedded nodes",
			"a graph with 2556 vectors must never be told it has none")
	})

	t.Run("genuinely_unembedded_graph_says_so", func(t *testing.T) {
		// KNOWN POSITIVE for the leg above: when there really are no vectors, the
		// message SHOULD say so — otherwise "never says no embedded nodes" would be
		// satisfiable by a message that can never describe an unembedded graph.
		msg := rebuildScannedNothing(context.Background(), depsFor(3117, 0),
			manageArgs{Graph: "practice", Name: "fresh"})
		assert.Contains(t, msg, "NONE are embedded yet")
		assert.Contains(t, msg, "3117", "and states the node count")
		assert.NotContains(t, msg, "since the stored watermark",
			"the watermark is not the cause here, so it must not be blamed")
	})

	t.Run("empty_graph_says_empty", func(t *testing.T) {
		msg := rebuildScannedNothing(context.Background(), depsFor(0, 0),
			manageArgs{Graph: "practice", Name: "void"})
		assert.Contains(t, msg, "empty")
		assert.NotContains(t, msg, "since the stored watermark")
	})

	t.Run("reset_run_does_not_blame_the_watermark", func(t *testing.T) {
		// With reset:true the watermark was ZEROED, so it cannot be the cause. Naming
		// it would trade one false explanation for another.
		msg := rebuildScannedNothing(context.Background(), depsFor(3117, 2556),
			manageArgs{Graph: "practice", Name: "design-patterns", Reset: true})
		assert.NotContains(t, msg, "since the stored watermark",
			"reset zeroes the watermark — it cannot explain a zero scan")
		assert.Contains(t, msg, "pipeline fault", "so the message points at the real remaining cause")
	})

	t.Run("unmeasurable_corpus_states_the_scan_not_a_cause", func(t *testing.T) {
		// No stats seam: the corpus counts are unknown. An unmeasured operand must
		// not become a claim about the graph.
		msg := rebuildScannedNothing(context.Background(),
			&rebuildStatsDeps{interceptDeps: &interceptDeps{}},
			manageArgs{Graph: "practice", Name: "unknown"})
		assert.Contains(t, msg, "since the stored watermark")
		assert.Contains(t, msg, `"reset": true`)
		assert.NotContains(t, msg, "no embedded nodes",
			"an unmeasured corpus must not be described as empty")
	})
}
