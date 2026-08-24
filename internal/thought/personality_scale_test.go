// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// scaleClusterCount is a FIXTURE constant, NOT a reading of the current graph. It
// is the live cluster population the dense representation was measured at (4,306
// to 4,317 observed across the hourly ticks). Do not refresh it from a running
// daemon: the test's value comes from holding one fixed reference point over
// time, not from tracking today's corpus.
const scaleClusterCount = 4317

// scaleAllocCeilingBytes bounds what either stage may allocate at
// scaleClusterCount. The dense predecessor measured 1,804.5 MB for the producer
// and 6,836.4 MB for the render at this size; the sparse form measured 6.9 MB and
// 3.6 MB. 100 MB therefore sits more than an order of magnitude ABOVE the sparse
// measurements and more than an order of magnitude BELOW the dense ones — it
// fires on any reintroduction of a per-pair materialization, without flaking on
// allocator noise.
const scaleAllocCeilingBytes = 100 << 20

// TestPersonalityProfile_ScaleBounds is this ticket's acceptance MEASUREMENT: at
// the live cluster population the profile stays bounded by its own encoding
// invariant, and neither the producer nor the render allocates at dense scale.
//
// It deliberately does NOT build a dense reference at this size. That would cost
// about eleven seconds and prove nothing the differential equivalence tests do
// not already prove exactly at small sizes.
//
// KNOWN PROXY LIMIT, stated rather than left implicit: runtime.MemStats.TotalAlloc
// is process-wide and cumulative, so a concurrent allocator inside the same test
// binary would inflate the deltas below. Go runs a package's tests sequentially
// and the ceiling carries more than an order of magnitude of headroom over the
// measurement, so the risk is nominal — but this is a proxy, not an isolated
// reading, and a future reader should not treat it as one.
func TestPersonalityProfile_ScaleBounds(t *testing.T) {
	ctx := context.Background()
	clusters, gc, evidenceAdj := sparseOracleCorpus(scaleClusterCount, 1000, 3)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	profile, err := ComputePersonalityScalars(ctx, gc, clusters, evidenceAdj, nil)
	require.NoError(t, err)
	runtime.ReadMemStats(&after)
	producerAlloc := after.TotalAlloc - before.TotalAlloc

	runtime.GC()
	runtime.ReadMemStats(&before)
	report := ReflectPersonality(clusters, &profile, "")
	runtime.ReadMemStats(&after)
	renderAlloc := after.TotalAlloc - before.TotalAlloc

	deviations := 0
	for _, row := range profile.Deviations {
		deviations += len(row)
	}
	entries := len(profile.RowDefault) + deviations
	densePairs := len(clusters) * (len(clusters) - 1)

	t.Logf("C=%d entries=%d (rows=%d deviations=%d) dense_pairs=%d producer_alloc=%dB render_alloc=%dB rendered=%d+%d",
		len(clusters), entries, len(profile.RowDefault), deviations, densePairs,
		producerAlloc, renderAlloc, len(report.TopStubborn), len(report.TopGullible))

	// (1) The encoding's own invariant, expressed against values DERIVED FROM THE
	// FIXTURE rather than a magic number, so it stays true if the fixture is
	// retuned: one row per cluster, plus at most one deviation per
	// evidence-carrying charge.
	require.LessOrEqual(t, entries, len(clusters)+len(evidenceAdj),
		"profile entries must stay bounded by one row per cluster plus at most one deviation per evidence-carrying charge")

	// (2) and (3) Neither stage may allocate at dense scale.
	require.Less(t, producerAlloc, uint64(scaleAllocCeilingBytes),
		"the producer must not allocate at dense scale")
	require.Less(t, renderAlloc, uint64(scaleAllocCeilingBytes),
		"the render must not allocate at dense scale")

	// (4) NON-VACUITY CONTROLS. Without the first, a corpus too small to matter
	// satisfies both ceilings; without the second, a render that produced nothing
	// does.
	require.Greater(t, densePairs, 1000*entries,
		"control: the dense pair space must dwarf the sparse entry count, or the ceilings prove nothing")
	require.Len(t, report.TopStubborn, personalityTopK,
		"control: the render must actually have produced its full row set")
}
