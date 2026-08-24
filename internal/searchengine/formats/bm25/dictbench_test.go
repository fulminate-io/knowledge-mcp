// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// Environment levers for BenchmarkDictionaryChoice. Both must be set for the
// benchmark to run: it measures a dictionary encoding against a REAL query
// trace over a REAL corpus, and neither exists on CI. A synthetic query list
// would be exactly the invented input the decision must not rest on.
const (
	dictbenchTraceEnv  = "BM25_TRACE"
	dictbenchCorpusEnv = "BM25_CORPUS"
)

// dictbenchEnv is the resolved benchmark configuration.
type dictbenchEnv struct {
	// tracePath is a file of real queries, one per line.
	tracePath string
	// corpusDir holds the encoded segment blobs to search.
	corpusDir string
}

// resolveDictbenchEnv reads the benchmark's two environment levers. The second
// return is a human-readable reason the benchmark cannot run, and is EMPTY when
// the configuration is complete — callers skip on a non-empty reason. Returning
// the reason rather than calling b.Skip directly is what lets a plain test
// assert the not-configured path, since a skipped benchmark prints nothing that
// identifies it.
func resolveDictbenchEnv() (dictbenchEnv, string) {
	env := dictbenchEnv{
		tracePath: os.Getenv(dictbenchTraceEnv),
		corpusDir: os.Getenv(dictbenchCorpusEnv),
	}
	switch {
	case env.tracePath == "":
		return env, dictbenchTraceEnv + " is not set: no real query trace to measure against"
	case env.corpusDir == "":
		return env, dictbenchCorpusEnv + " is not set: no real corpus to measure against"
	}
	if _, err := os.Stat(env.tracePath); err != nil { //nolint:gosec // operator-supplied benchmark input
		return env, fmt.Sprintf("%s=%s is not readable: %v", dictbenchTraceEnv, env.tracePath, err)
	}
	info, err := os.Stat(env.corpusDir) //nolint:gosec // operator-supplied benchmark input
	if err != nil {
		return env, fmt.Sprintf("%s=%s is not readable: %v", dictbenchCorpusEnv, env.corpusDir, err)
	}
	if !info.IsDir() {
		return env, fmt.Sprintf("%s=%s is not a directory", dictbenchCorpusEnv, env.corpusDir)
	}
	return env, ""
}

// TestDictbenchSkipsWithoutTrace proves BenchmarkDictionaryChoice is present and
// inert when unconfigured. A skipped benchmark prints only "PASS / ok" without
// -v, so a gate on the benchmark alone cannot distinguish present-and-skipped
// from absent; this test produces a named PASS line that can.
//
// The not-configured assertion is paired with a known-positive in the same run:
// with both levers pointed at real paths, resolveDictbenchEnv must report
// configured. Without that control an implementation that always returned a
// reason — a benchmark that could never run — would pass the first half.
func TestDictbenchSkipsWithoutTrace(t *testing.T) {
	t.Setenv(dictbenchTraceEnv, "")
	t.Setenv(dictbenchCorpusEnv, "")
	_, reason := resolveDictbenchEnv()
	require.NotEmpty(t, reason, "unconfigured resolve must report a reason so the benchmark can skip")
	require.Contains(t, reason, dictbenchTraceEnv, "the reason must name the missing lever")

	// Known-positive control: a real trace file and a real corpus dir resolve clean.
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace.txt")
	require.NoError(t, os.WriteFile(trace, []byte("segment merge resident memory\n"), 0o600))
	corpus := filepath.Join(dir, "corpus")
	require.NoError(t, os.Mkdir(corpus, 0o750))
	t.Setenv(dictbenchTraceEnv, trace)
	t.Setenv(dictbenchCorpusEnv, corpus)
	env, reason := resolveDictbenchEnv()
	require.Empty(t, reason, "a complete configuration must resolve without a reason")
	require.Equal(t, trace, env.tracePath)
	require.Equal(t, corpus, env.corpusDir)
}

// loadDictbenchTrace reads the query trace, dropping blank lines. Order is
// preserved: the trace is a record of what was actually issued, and replaying it
// in issue order is what makes the latency distribution representative.
func loadDictbenchTrace(tb testing.TB, path string) []string {
	tb.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied benchmark input
	require.NoError(tb, err)
	var out []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	require.NotEmpty(tb, out, "trace %s held no queries", path)
	return out
}

// dictbenchCorpus is a decoded corpus plus the on-disk size it was decoded from.
type dictbenchCorpus struct {
	segments []searchengine.Segment[Query, *CorpusStats]
	// blobBytes is the total encoded size of the corpus in THIS encoding. It is
	// the "blob bytes for the whole corpus" column of the decision table.
	blobBytes int64
}

// loadDictbenchCorpus decodes every .seg blob in dir through the shipped
// Format.Decode — the decision must be measured on the path that ships, not on
// a bespoke reader built for the benchmark.
func loadDictbenchCorpus(tb testing.TB, dir string) dictbenchCorpus {
	tb.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(tb, err)
	var corpus dictbenchCorpus
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".seg" {
			continue
		}
		blob, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // operator-supplied benchmark input
		require.NoError(tb, err)
		seg, err := Format{}.Decode(blob)
		// A corpus dir written before the offset layout holds version-1 blobs,
		// which Decode refuses by design. Say so plainly: an operator pointing
		// BM25_CORPUS at a pre-migration cache should read "rebuild it", not a
		// bare decode error from inside a benchmark.
		require.NoError(tb, err, "blob %s in %s is not readable by the shipped decoder; "+
			"a corpus written before the offset layout must be rebuilt before it can be measured",
			entry.Name(), dir)
		corpus.segments = append(corpus.segments, seg)
		corpus.blobBytes += int64(len(blob))
	}
	require.NotEmpty(tb, corpus.segments, "corpus dir %s held no .seg blobs", dir)
	return corpus
}

// dictbenchCandidate is one encoding under measurement. The label is what lands
// in the decision file, so it is the encoding's exact name.
type dictbenchCandidate struct {
	label string
	// load turns the corpus dir into searchable segments in this encoding.
	load func(tb testing.TB, dir string) dictbenchCorpus
	// pagesTouched reports the distinct pages one query touched in one segment,
	// counted in the same 16 KiB unit the design's baseline table uses, or -1
	// when the encoding is not mapped and the census does not apply. Left nil by
	// a heap-resident candidate: it has no pages to count, and reporting a 0
	// would read as "touched none" rather than "not applicable".
	pagesTouched func(seg searchengine.Segment[Query, *CorpusStats], q Query) int
}

// dictbenchCandidates is the encoding set the decision is made over: the three
// dictionary encodings a serialVersion-2 blob may declare, each measured through
// the SHIPPED reader over a corpus built in that encoding.
//
// There is deliberately no map-resident baseline candidate. Decode accepts only
// serialVersion 2, so no v1 blob is readable by anything in the tree and a
// candidate claiming to measure that form would be measuring the offset reader
// under a misleading label. The v1 numbers in the decision record are prototype
// measurements of code this work deleted: they are context for the ORDERING of
// the three candidates, not a row this harness can reproduce.
func dictbenchCandidates() []dictbenchCandidate {
	kinds := []struct {
		label string
		kind  byte
	}{
		{"flat", dictFlat},
		{"blocked", dictBlocked},
		{"hash", dictHash},
	}
	out := make([]dictbenchCandidate, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, dictbenchCandidate{
			label:        k.label,
			load:         func(tb testing.TB, dir string) dictbenchCorpus { return loadDictbenchCorpusAs(tb, dir, k.kind) },
			pagesTouched: pagesTouchedIn16KiB,
		})
	}
	return out
}

// loadDictbenchCorpusAs decodes the corpus and RE-ENCODES it into one dictionary
// encoding, so every candidate is measured over the same documents differing
// only in the dimension under test.
func loadDictbenchCorpusAs(tb testing.TB, dir string, kind byte) dictbenchCorpus {
	tb.Helper()
	src := loadDictbenchCorpus(tb, dir)
	out := dictbenchCorpus{segments: make([]searchengine.Segment[Query, *CorpusStats], 0, len(src.segments))}
	for _, seg := range src.segments {
		ms, ok := seg.(*mappedSegment)
		require.True(tb, ok, "corpus segment is %T, not the offset reader", seg)
		blob, err := encodeSegmentV2(accumulatorFrom(ms), kind)
		require.NoError(tb, err)
		reopened, err := openSegmentV2(blob)
		require.NoError(tb, err)
		out.segments = append(out.segments, reopened)
		out.blobBytes += int64(len(blob))
	}
	return out
}

// accumulatorFrom rebuilds the map-shaped accumulator from a mapped segment, so
// the encoder can re-emit it in another dictionary encoding. This is benchmark
// scaffolding only: the shipped path never turns a segment back into one.
func accumulatorFrom(ms *mappedSegment) *bm25Segment {
	acc := &bm25Segment{
		members:     make([]searchengine.ExternalID, ms.docCount),
		fields:      make([]*fieldData, 0, len(ms.fields)),
		fieldByName: make(map[string]*fieldData, len(ms.fields)),
		docFreq:     make(map[string]int64),
	}
	for i := range ms.docCount {
		acc.members[i] = strings.Clone(ms.member(i))
	}
	ms.docFreqEach(func(term string, df int64) { acc.docFreq[strings.Clone(term)] = df })
	for _, mf := range ms.fields {
		fd := &fieldData{
			config:      mf.config,
			totalTokens: mf.totalTokens,
			postings:    make(map[string][]posting),
			docLengths:  make([]int, ms.docCount),
		}
		for d, l := range mf.lengths {
			fd.docLengths[d] = int(l)
		}
		mf.eachTerm(func(term string, docIDs []uint32, tfs []uint16) {
			posts := make([]posting, len(docIDs))
			for i, d := range docIDs {
				posts[i] = posting{docID: d, tf: tfs[i]}
			}
			fd.postings[strings.Clone(term)] = posts
		})
		acc.fields = append(acc.fields, fd)
		acc.fieldByName[fd.config.Name] = fd
	}
	return acc
}

// CI runs it as a no-op while a machine with a live corpus can measure it.
func BenchmarkDictionaryChoice(b *testing.B) {
	env, reason := resolveDictbenchEnv()
	if reason != "" {
		b.Skip(reason)
	}
	trace := loadDictbenchTrace(b, env.tracePath)
	queries := make([]Query, len(trace))
	for i, text := range trace {
		queries[i] = NewQuery(text)
	}

	for _, cand := range dictbenchCandidates() {
		b.Run(cand.label, func(b *testing.B) {
			corpus := cand.load(b, env.corpusDir)
			stats := Format{}.AggregateStats(corpus.segments)
			b.ReportAllocs()
			latencies := make([]time.Duration, 0, len(queries))
			b.ResetTimer()
			for b.Loop() {
				for _, q := range queries {
					start := time.Now()
					for _, seg := range corpus.segments {
						_ = seg.Search(q, stats, 10, nil)
					}
					latencies = append(latencies, time.Since(start))
				}
			}
			b.StopTimer()
			reportDictbench(b, cand, corpus, queries, latencies)
		})
	}
}

// reportDictbench emits the decision table's columns for one candidate.
func reportDictbench(
	b *testing.B, cand dictbenchCandidate, corpus dictbenchCorpus,
	queries []Query, latencies []time.Duration,
) {
	b.Helper()
	if len(latencies) == 0 {
		return
	}
	sorted := slices.Clone(latencies)
	slices.Sort(sorted)
	var total time.Duration
	for _, d := range sorted {
		total += d
	}
	msOf := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	pct := func(p float64) time.Duration {
		idx := int(math.Ceil(p*float64(len(sorted)))) - 1
		return sorted[max(0, min(idx, len(sorted)-1))]
	}
	b.ReportMetric(msOf(total/time.Duration(len(sorted))), "ms/query")
	b.ReportMetric(msOf(pct(0.50)), "p50-ms")
	b.ReportMetric(msOf(pct(0.90)), "p90-ms")
	b.ReportMetric(msOf(pct(0.99)), "p99-ms")
	b.ReportMetric(float64(corpus.blobBytes), "corpus-bytes")
	if cand.pagesTouched == nil {
		return
	}
	var pages, samples int64
	for _, q := range queries {
		for _, seg := range corpus.segments {
			if n := cand.pagesTouched(seg, q); n >= 0 {
				pages += int64(n)
				samples++
			}
		}
	}
	if samples > 0 {
		b.ReportMetric(float64(pages)/float64(samples), "pages/query/segment")
	}
}
