// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound_failure_test.go — the RESIDENT-BOUND'S FAILURE ARMS: what it
// does when it CANNOT consolidate, and what it must not do to the write that reached
// it.
//
// SPLIT OUT OF manager_bucket_bound_test.go when that file reached the repository's
// hard 500-line cap. The seam is the honest one: the sibling asserts the bound
// HOLDING — the sustained count, the intra-call excursion, the census rate, the
// classes of candidate — while everything here is about the two ways it can fail. A
// bound that cannot act must SAY SO, and a bound that fails must not take a write
// that already succeeded down with it.

package segmentdist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// TestResidentBoundDisclosesWhenItCannotAct is the arm that keeps a bound which has
// stopped working from reading like one that is holding.
//
// THE STATE IS REACHABLE AND IS NOT A CORNER. Every candidate constituent can span
// more than one partition at the current count — segments sealed before a doubling
// do exactly that (searchengine/bucket.go), and this set accumulates across many
// windows while the corpus grows. A per-partition swap must decline all of them, so
// the count stays over budget and only the drain's GROUP rebuild can consolidate
// them. Returning silently there would report a held bound that is not holding.
//
// The fixture builds that state directly on a mock engine: every segment holds two
// ids that hash to DIFFERENT partitions at the count the bound is evaluated under,
// so SegmentSpans reports two partitions for every one of them.
func TestResidentBoundDisclosesWhenItCannotAct(t *testing.T) {
	// MERGE-DISABLED, exactly as both production engines are constructed: the
	// default-options mock engine would consolidate the fixture down to a handful of
	// segments before the bound was ever asked anything.
	engine := searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
	})
	t.Cleanup(engine.Close)
	dm := newDistManager(engine, nil, graphSelector(boundGraphType, "spanning"), "mock")

	over := searchengine.ResidentSegmentFanoutBudget + 1
	for i := range over {
		docs := spanningPair(t, i)
		_, err := engine.AddSealAndSupersede(docs)
		require.NoError(t, err)
	}
	require.Greater(t, engine.ResidentSegmentCount(), searchengine.ResidentSegmentFanoutBudget,
		"PRECONDITION: the engine must be over budget, or the bound returns before it can disclose anything")
	spans := engine.SegmentSpans(spanCount)
	require.Empty(t, consolidatablePartitions(spans),
		"PRECONDITION: every segment must span both partitions, so nothing is consolidatable alone")

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	boundResidentSegments(dm, spanCount)
	slog.SetDefault(prev)

	require.Contains(t, buf.String(), "the resident segment count is over the fan-out budget and no partition could be consolidated alone",
		"a bound that could not act must SAY SO; silence here is indistinguishable from a bound that held")
	require.NotContains(t, buf.String(), "partitions_consolidated",
		"and it must not also claim a consolidation it never performed")
}

// spanCount is the partition count the disclosure fixture is evaluated under, and
// the number of partitions each of its segments spans. A const rather than a
// parameter: no case varies it.
const spanCount = 2

// spanningPair builds one segment's worth of ids that hash to DIFFERENT partitions
// under bucketCount, so the segment spans every one of them. It SEARCHES for the
// ids rather than assuming a pattern: BucketOf is a hash, and an assumed spread is
// the kind of fixture that goes vacuous when the hash changes.
func spanningPair(t *testing.T, seed int) []searchengine.Document {
	t.Helper()
	byBucket := make(map[int]searchengine.Document, spanCount)
	for i := 0; len(byBucket) < spanCount; i++ {
		if i > 1000 {
			t.Fatalf("spanningPair(%d): no id spread across %d partitions after 1000 tries", seed, spanCount)
		}
		id := fmt.Sprintf("span-%05d-%04d", seed, i)
		b := searchengine.BucketOf(id, spanCount)
		if _, taken := byBucket[b]; taken {
			continue
		}
		byBucket[b] = doc(id, "alpha beta")
	}
	out := make([]searchengine.Document, 0, spanCount)
	for b := range spanCount {
		out = append(out, byBucket[b])
	}
	return out
}

// failingMergeBM25 is the real BM25 format with ONE behaviour changed: its MergeTo
// refuses. It is the package's own double idiom — embed the working format and
// override the single call under test — and it is the REAL format type so this
// engine can be installed into a Manager's own bm25 arm and driven through the real
// write entry point.
type failingMergeBM25 struct{ bm25.Format }

var errMergeRefused = errors.New("failingMergeBM25 refuses to merge")

func (failingMergeBM25) MergeTo(
	_ searchengine.MergeSink,
	_ []searchengine.Segment[bm25.Query, *bm25.CorpusStats],
	_ []func(searchengine.ExternalID) bool,
) (int64, error) {
	return 0, errMergeRefused
}

// TestConsolidationFailureStillRecordsTheBatch is the error arm of the
// resident-segment bound, and the disposition it pins REVERSES an earlier one.
//
// THE BOUND IS A MEMORY AND LATENCY PROPERTY, NOT CORPUS CORRECTNESS. By the time it
// runs, the batch's segments are published, resident and searchable: the write has
// already succeeded. An earlier revision returned the consolidation's error from
// sealPerPartition, which failed that write — and worse, it skipped the caller's
// recordDirty, so the batch landed in NO backlog, was never drained to L2, and the
// pipeline stamped a per-node ship failure it does not retry. A bound that cannot
// converge is a count that stays high until the next touch; it is not a reason to
// strand data. The package's own precedent is manager_search.go: a failed
// optimisation must not fail a good operation.
//
// THREE THINGS ARE ASSERTED, and each fails a different way of getting this wrong:
// the write RETURNS NIL (an error return is the durability hole), the batch's tails
// ARE in the backlog and its documents searchable (the hole itself, observed rather
// than inferred), and the failure IS LOGGED AT ERROR (swallowing it silently leaves
// an engine permanently over budget with nothing to say so).
func TestConsolidationFailureStillRecordsTheBatch(t *testing.T) {
	ctx := context.Background()
	const name = "bound-merge-fails"
	m := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	// The engine is installed into the Manager's OWN bm25 arm, so the batch below
	// goes through the production entry point — sequence, seal, bound, recordDirty —
	// rather than through a re-implementation of its order.
	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](failingMergeBM25{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
	})
	t.Cleanup(engine.Close)
	installBM25Arm(m, name, engine)

	over := searchengine.ResidentSegmentFanoutBudget + 1
	for i := range over {
		_, err := engine.AddSealAndSupersede([]searchengine.Document{boundDoc(i)})
		require.NoError(t, err, "PRECONDITION: the seals must succeed — only the MERGE is refused")
	}
	require.Greater(t, engine.ResidentSegmentCount(), searchengine.ResidentSegmentFanoutBudget,
		"PRECONDITION: the engine must be over budget, or the bound never attempts a consolidation")

	batch := []searchengine.Document{boundDoc(900001), boundDoc(900002)}
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	err := m.AddAndMarkDirtyFields(ctx, boundGraphType, name, batch)
	slog.SetDefault(prev)

	require.NoError(t, err,
		"a consolidation that could not merge must NOT fail a write that already succeeded; returning its error "+
			"skips recordDirty and strands this batch outside every backlog, where nothing drains it to L2")

	_, backlog := m.snapshotDirty(boundGraphType, name)
	require.Len(t, backlog.pending, len(batch),
		"the batch's documents must be RECORDED for a later partitioned re-emit — this is the durability hole")
	require.NotEmpty(t, backlog.tails,
		"and so must the tails the seal created, or the drain has nothing to retire")

	require.Empty(t, engine.UncoveredFrom([]searchengine.ExternalID{batch[0].ID, batch[1].ID}),
		"the batch's documents are live-searchable in this process the moment the write returns")

	require.Contains(t, buf.String(), "a resident-bound consolidation failed",
		"a bound that could not converge must SAY SO at ERROR; swallowed silently it leaves an engine "+
			"permanently over its fan-out budget with nothing in the log to explain it")
	require.Contains(t, buf.String(), errMergeRefused.Error(),
		"and the line must carry the underlying failure, or an operator has a symptom and no cause")
}

// installBM25Arm puts a prepared engine into the Manager's own bm25 arm for one
// graph, so the production write entry point drives it.
func installBM25Arm(
	m *Manager, name string, engine *searchengine.SegmentedIndex[bm25.Query, *bm25.CorpusStats],
) {
	dm := newDistManager(engine, nil, graphSelector(boundGraphType, name), bm25.New().Name())
	gate := &constructionGate[bm25.Query, *bm25.CorpusStats]{dm: dm, done: make(chan struct{})}
	close(gate.done)
	m.mu.Lock()
	m.bm25Managers[graphKey{graphType: boundGraphType, graphName: name}] = gate
	m.mu.Unlock()
}
