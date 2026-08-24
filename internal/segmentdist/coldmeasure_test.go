// SPDX-License-Identifier: Apache-2.0

//go:build unix

package segmentdist

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// Environment levers for the cold measurement. Inert unless both are set.
const (
	coldCorpusEnv = "BM25_COLD_CORPUS"
	coldTraceEnv  = "BM25_COLD_TRACE"

	hnswColdCorpusEnv  = "HNSW_COLD_CORPUS"
	hnswColdQueriesEnv = "HNSW_COLD_QUERIES"
)

// hnswColdVecBytes is the query-vector width the HNSW arm accepts, mirroring the
// hnsw package's own defaultVecBytes (256-bit ubinary). It is restated here
// because that constant is unexported and this test lives in another package; a
// query of any other width is rejected rather than padded.
//
// THIS IS A MIRROR, AND MIRRORS DRIFT. The authoritative vector width lives with
// the segment format, not here: when the offset-addressed layout lands its own
// constant block, that block is the source of truth and this value must still
// agree with it. A disagreement means this arm is measuring a query shape the
// format no longer indexes.
const hnswColdVecBytes = 32

// TestColdTimeToFirstResult measures what a restart actually costs: with the
// corpus evicted, how long until the first query can be answered.
//
// COLD IS MEASURED, NEVER ASSUMED — but the guarantee here is WEAKER than the
// design work's, and the difference is worth stating rather than glossing. That
// session verified 0.00% residency per run by calling mincore(2) through cgo
// before every measurement, so its zero was an observation. This test has no
// such probe: mincore is not reachable from x/sys/unix on darwin, and adding cgo
// to this package to get it would change what the package requires to build for
// the sake of a benchmark. So residency here rests on the METHOD instead — a
// detachable sparse disk image, where detaching reclaims the volume's vnodes so
// the buffer cache for those files is empty on re-attach — and on the process's
// own fault counters, which the caller reads with /usr/bin/time -l.
//
// That means: the elapsed times below are measurements, and the claim that the
// cache was cold is an argument from the method rather than a per-run check.
//
// Everything here is a darwin/unix measurement of this machine. No number it
// prints describes another platform's read-ahead behaviour.
func TestColdTimeToFirstResult(t *testing.T) {
	corpusDir, tracePath := os.Getenv(coldCorpusEnv), os.Getenv(coldTraceEnv)
	if corpusDir == "" || tracePath == "" {
		t.Skipf("%s and %s must both be set to run the cold measurement", coldCorpusEnv, coldTraceEnv)
	}
	raw, err := os.ReadFile(tracePath) //nolint:gosec // operator-supplied measurement input
	require.NoError(t, err)
	var queries []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			queries = append(queries, line)
		}
	}
	require.NotEmpty(t, queries)

	entries, err := os.ReadDir(corpusDir)
	require.NoError(t, err)

	// DERIVED, NEVER HARDCODED — same reason as the HNSW arm below: a measurement
	// that maps with a different advice than production maps with is measuring a
	// configuration nobody runs, and a cold fault-bound number is exactly what
	// read-ahead advice moves.
	advice, adviceErr := adviceForFormat(bm25.New().Name())
	require.NoError(t, adviceErr)

	start := time.Now()
	var maps []*mappedBlob
	var segs []searchengine.Segment[bm25.Query, *bm25.CorpusStats]
	var corpusBytes int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".seg" {
			continue
		}
		m, err := mapBlobFile(filepath.Join(corpusDir, e.Name()), advice)
		require.NoError(t, err)
		maps = append(maps, m)
		corpusBytes += len(m.data)
		seg, err := bm25.Format{}.Decode(m.data)
		require.NoError(t, err)
		segs = append(segs, seg)
	}
	require.NotEmpty(t, segs, "no segments to measure")
	openDone := time.Since(start)

	stats := bm25.Format{}.AggregateStats(segs)
	q := bm25.NewQuery(queries[0])
	hits := 0
	for _, seg := range segs {
		hits += len(seg.Search(q, stats, 10, nil))
	}
	firstResult := time.Since(start)

	t.Logf("COLD MEASUREMENT (%d segments, %d corpus bytes, %d-byte pages)",
		len(segs), corpusBytes, os.Getpagesize())
	t.Logf("  open+map all segments:      %s", openDone.Round(time.Millisecond))
	t.Logf("  time to FIRST RESULT:       %s (%d hits)", firstResult.Round(time.Millisecond), hits)
	t.Logf("  faults are counted by the CALLER via /usr/bin/time -l; this process does not probe residency")

	for _, m := range maps {
		require.NoError(t, m.release())
	}
	require.Positive(t, hits, "the first query matched nothing, so the timing above measures an empty search")
}

// TestHNSWColdTimeToFirstResult measures the same restart cost for the HNSW pool
// that the BM25 arm above measures for the text pool: with the corpus evicted,
// how long until the first vector query can be answered.
//
// COLD IS MEASURED, NEVER ASSUMED — but the guarantee here is WEAKER than the
// design work's, and the difference is worth stating rather than glossing. That
// session verified 0.00% residency per run by calling mincore(2) through cgo
// before every measurement, so its zero was an observation. This test has no
// such probe: mincore is not reachable from x/sys/unix on darwin, and adding cgo
// to this package to get it would change what the package requires to build for
// the sake of a benchmark. So residency here rests on the METHOD instead — a
// detachable sparse disk image, where detaching reclaims the volume's vnodes so
// the buffer cache for those files is empty on re-attach — and on the process's
// own fault counters, which the caller reads with /usr/bin/time -l.
//
// That means: the elapsed times below are measurements, and the claim that the
// cache was cold is an argument from the method rather than a per-run check.
//
// The absolute numbers carry the sparse image's own sequential penalty, which
// inflates BOTH the open+map and first-result columns. The caller measures that
// penalty separately and reports it beside these figures, so every cold number
// here is to be read as pessimistic by that factor.
//
// Queries arrive hex-encoded, one per line, because an HNSW query is a raw
// binary vector rather than the BM25 arm's free text. A line that decodes to any
// width other than the indexed one is rejected outright: silently padding a
// short vector would measure a query the operator never asked for.
//
// Everything here is a darwin/unix measurement of this machine. No number it
// prints describes another platform's read-ahead behaviour.
func TestHNSWColdTimeToFirstResult(t *testing.T) {
	corpusDir, queriesPath := os.Getenv(hnswColdCorpusEnv), os.Getenv(hnswColdQueriesEnv)
	if corpusDir == "" || queriesPath == "" {
		t.Skipf("%s and %s must both be set to run the cold measurement", hnswColdCorpusEnv, hnswColdQueriesEnv)
	}
	raw, err := os.ReadFile(queriesPath) //nolint:gosec // operator-supplied measurement input
	require.NoError(t, err)
	var queries [][]byte
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		vec, decErr := hex.DecodeString(line)
		require.NoError(t, decErr, "query line is not valid hex: %q", line)
		require.Len(t, vec, hnswColdVecBytes,
			"query vector decoded to %d bytes, want %d; a padded or truncated vector would measure a query the operator did not supply",
			len(vec), hnswColdVecBytes)
		queries = append(queries, vec)
	}
	require.NotEmpty(t, queries)

	entries, err := os.ReadDir(corpusDir)
	require.NoError(t, err)

	// THE ADVICE IS DERIVED, NEVER HARDCODED. A measurement that maps with a
	// different advice than production maps with is measuring a configuration
	// nobody runs — and read-ahead advice is precisely what a cold, fault-bound
	// number is sensitive to. adviceForFormat is the single source of truth that
	// production's managerFor reads, and its own error text names this hazard:
	// "rather than inheriting another format's measurement".
	advice, adviceErr := adviceForFormat(hnsw.New().Name())
	require.NoError(t, adviceErr)

	start := time.Now()
	var maps []*mappedBlob
	var segs []searchengine.Segment[[]byte, struct{}]
	var corpusBytes int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".seg" {
			continue
		}
		m, mapErr := mapBlobFile(filepath.Join(corpusDir, e.Name()), advice)
		require.NoError(t, mapErr)
		maps = append(maps, m)
		corpusBytes += len(m.data)
		seg, decodeErr := hnsw.Format{}.Decode(m.data)
		require.NoError(t, decodeErr)
		segs = append(segs, seg)
	}
	require.NotEmpty(t, segs, "no segments to measure")
	openDone := time.Since(start)

	stats := hnsw.Format{}.AggregateStats(segs)
	hits := 0
	for _, seg := range segs {
		hits += len(seg.Search(queries[0], stats, 10, nil))
	}
	firstResult := time.Since(start)

	t.Logf("HNSW COLD MEASUREMENT (%d segments, %d corpus bytes, %d-byte pages)",
		len(segs), corpusBytes, os.Getpagesize())
	t.Logf("  open+map all segments:      %s", openDone.Round(time.Millisecond))
	t.Logf("  time to FIRST RESULT:       %s (%d hits)", firstResult.Round(time.Millisecond), hits)
	t.Logf("  faults are counted by the CALLER via /usr/bin/time -l; this process does not probe residency")

	for _, m := range maps {
		require.NoError(t, m.release())
	}
	require.Positive(t, hits, "the first query matched nothing, so the timing above measures an empty search")
}
