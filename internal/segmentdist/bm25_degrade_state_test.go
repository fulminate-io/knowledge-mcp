// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestBM25DegradeCensusRoundTripsDurably gates the DURABLE half of the carrier.
//
// THE ON-DISK LEG IS THE ONE THAT MATTERS MOST: without it a per-Manager
// in-memory map satisfies every other assertion here and loses the census on
// every daemon restart, which is precisely the silent zero this census exists to
// remove.
func TestBM25DegradeCensusRoundTripsDurably(t *testing.T) {
	const gt = kgtypes.GraphCode
	cacheDir := t.TempDir()
	m := NewManager(cacheDir, 0)

	rep := func(census map[string]int) searchengine.BuildReport {
		return searchengine.BuildReport{Degraded: census}
	}

	// A CLEAN BUILD WRITES NOTHING, so an empty census and no census stay one state.
	m.RecordBuildDegrade(gt, "graph-a", rep(nil))
	require.Nil(t, m.BM25DegradeCounts(gt, "graph-a"), "a clean build must leave no record at all")

	// Record then read.
	m.RecordBuildDegrade(gt, "graph-a", rep(map[string]int{"tokenize_panic": 2}))
	require.Equal(t, map[string]int{"tokenize_panic": 2}, m.BM25DegradeCounts(gt, "graph-a"))

	// ACCUMULATION ACROSS BUILDS, not replacement: two builds that each drop one
	// document have dropped two, and a record that replaced would report one.
	m.RecordBuildDegrade(gt, "graph-a", rep(map[string]int{"tokenize_panic": 3, "other_class": 1}))
	require.Equal(t, map[string]int{"tokenize_panic": 5, "other_class": 1},
		m.BM25DegradeCounts(gt, "graph-a"))

	// PER-GRAPH ISOLATION.
	require.Nil(t, m.BM25DegradeCounts(gt, "graph-b"), "one graph's drops must not appear on another's row")

	// THE RETURNED MAP IS A COPY, so a caller cannot mutate the record through it.
	got := m.BM25DegradeCounts(gt, "graph-a")
	got["tokenize_panic"] = 999
	require.Equal(t, 5, m.BM25DegradeCounts(gt, "graph-a")["tokenize_panic"],
		"mutating the returned map must not reach the record")

	// THE CENSUS IS BYTES IN A FILE, WHICH IS THE LEG NO IN-MEMORY TALLY PASSES.
	// A second Manager alone does not pin durability: both Managers live in THIS
	// process, so a package-global map would serve m2 the record m never wrote to
	// disk. Reading the record file is what separates the two — it is the same
	// bytes a restarted daemon would find, and nothing held in this process's heap
	// can produce it.
	path := bm25DegradeStatePathFor(cacheDir, gt, "graph-a")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the census must be a record on disk, not a tally in this process")
	var onDisk bm25DegradeStateRecord
	require.NoError(t, json.Unmarshal(raw, &onDisk), "the record on disk must decode as the census shape")
	require.Equal(t, map[string]int{"tokenize_panic": 5, "other_class": 1}, onDisk.Degraded,
		"the accumulated census must be the content of the file, not merely what a live Manager reports")

	// AND A SECOND MANAGER OVER THE SAME CACHE DIRECTORY READS IT BACK, which is
	// how the daemon actually recovers the census after a restart.
	m2 := NewManager(cacheDir, 0)
	require.Equal(t, map[string]int{"tokenize_panic": 5, "other_class": 1},
		m2.BM25DegradeCounts(gt, "graph-a"),
		"the census must survive the process that recorded it")

	// THE RESET CLEARS, AND IS IDEMPOTENT — a not-exist removal has done the job.
	require.NoError(t, m.ResetBM25DegradeCounts(gt, "graph-a"))
	require.Nil(t, m.BM25DegradeCounts(gt, "graph-a"))
	require.NoError(t, m.ResetBM25DegradeCounts(gt, "graph-a"),
		"resetting a record that is already gone is success, not an error")
	require.Nil(t, m2.BM25DegradeCounts(gt, "graph-a"), "the clear is durable too")
	require.NoFileExists(t, path, "the clear removes the record on disk, not just a live Manager's view of it")
}
