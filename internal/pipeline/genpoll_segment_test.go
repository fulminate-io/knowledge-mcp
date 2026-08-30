// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// genpoll_segment_test.go bounds the segment cheap tick's NUDGE RATE.
//
// THE FAILURE THIS EXISTS TO PREVENT IS A HOT TICK, NOT A MISSED ONE. An earlier
// design compared the served stamp against this client's durable merge watermark.
// That watermark advances only to the SERVED SAFE HORIZON, which by construction
// LAGS the maximum stamp — rows newer than the safe horizon exist and are
// deliberately not served — so `stamp > watermark` stays true indefinitely while
// writes continue and the nudge fires on EVERY poll. At a measured ~415ms per
// rebuilt partition that is a continuous rebuild loop wearing a cheap tick's name,
// and it is a lane that fires forever on one cause. Comparing against the client's
// OWN poke history is self-limiting instead: one nudge per distinct advance.
//
// A RATE TEST IS THEREFORE THE RIGHT INSTRUMENT. "The nudge fired" is satisfied by
// the defect; only counting nudges across repeated identical polls distinguishes
// the two.

// segEntry builds a segment-axis poll entry carrying a stamp.
func segEntry(name string, stamp int64) *knowledgev1.PipelineGenPollEntry { //nolint:unparam // name is the intentional named API: it is the graph the entry reports for, and hardcoding it would desync the entry from the caller's registered collector
	return &knowledgev1.PipelineGenPollEntry{
		GraphType: string(genPollGT), GraphName: name, Axis: "segment",
		SegmentDeltaStampNanos: stamp,
	}
}

// TestSegmentTickDebouncesToOneNudgePerAdvance pins the rate: N consecutive polls
// carrying an UNCHANGED stamp produce exactly ONE nudge, and a single advance
// produces exactly one more.
func TestSegmentTickDebouncesToOneNudgePerAdvance(t *testing.T) {
	fake := newFakeWireClient()
	p := genPollTestPipeline(t, fake)
	ctx := context.Background()

	// The poll builds its request's graph set from the registered collectors, so a
	// graph with no collector is never asked about and never returns an entry.
	registerStubCollector(p, "repoA")

	var nudged []string
	p.SetSegmentNudger(func(_ kgtypes.GraphType, name string) {
		nudged = append(nudged, name)
	})

	const stamp = int64(1_700_000_000_000_000_000)

	// FIVE consecutive polls carrying the SAME stamp. The first is a genuine
	// advance from the cold-start zero; the four after it are repeats.
	for range 5 {
		fake.seedGenPoll(segEntry("repoA", stamp))
		_, throttled := p.genPollOnce(ctx)
		require.False(t, throttled)
	}
	require.Len(t, nudged, 1,
		"five polls carrying an UNCHANGED stamp must produce exactly ONE nudge; more than one means the comparison is against something that keeps moving (the durable watermark) rather than against this client's own poke history, which is a continuous rebuild loop")

	// A SINGLE ADVANCE produces exactly one more.
	fake.seedGenPoll(segEntry("repoA", stamp+1))
	_, throttled := p.genPollOnce(ctx)
	require.False(t, throttled)
	require.Len(t, nudged, 2, "a distinct advance must produce exactly one further nudge")

	// AND A BACKWARD MOVE PRODUCES NONE. The stamp is a monotonic per-graph
	// maximum, so a lower value means nothing here — this is why the comparison is
	// `>` and not the `!=` its catalog_gen neighbor correctly uses.
	fake.seedGenPoll(segEntry("repoA", stamp-1000))
	_, throttled = p.genPollOnce(ctx)
	require.False(t, throttled)
	assert.Len(t, nudged, 2,
		"a BACKWARD stamp must produce no nudge: `!=` would wake on noise here, unlike the account-scoped catalog sample where a backward move is still movement")

	// A ZERO likewise: it is what the server serves for a graph whose stamp has
	// never been recorded, and it must never nudge.
	fake.seedGenPoll(segEntry("repoA", 0))
	_, throttled = p.genPollOnce(ctx)
	require.False(t, throttled)
	assert.Len(t, nudged, 2,
		"a ZERO stamp means 'never recorded' and must produce no nudge")
}
