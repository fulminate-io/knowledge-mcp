// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound_equivalence_test.go — SAME DOCUMENTS, SAME ORDER, SAME
// SCORES; ONLY THE SEGMENT GROUPING CHANGED.
//
// Every other row about the resident-growth bound asserts a COUNT: the sustained
// count stays inside the budget, the excursion is bounded, the census is rare, a
// document is still findable. None of them asserts that the index still ANSWERS THE
// SAME WAY. That is the question an operator actually has about a mechanism that
// rewrites the physical layout of a live corpus underneath a running search, and a
// consolidation is exactly such a rewrite: it merges several sealed segments into
// one, which changes how many segments the fan-out visits, which segment answers for
// a given id, and — if anything about the corpus statistics were disturbed by it —
// the IDF term in every BM25 score.
//
// THE FIXTURE ISOLATES THE CONSOLIDATION FROM EVERYTHING ELSE. The corpus is sealed
// straight onto engines installed in the Manager's own arms, so no bound runs while
// it is built; the query set is then recorded; boundResidentSegments is driven
// directly, WITHOUT adding or removing a single document; and the same query set is
// re-run. Any difference between the two readings is attributable to the
// consolidation and to nothing else — which is why the corpus is not grown between
// them, as more documents would legitimately move every IDF.
//
// BOTH ARMS, because they fail differently: BM25 carries corpus-wide statistics
// through the publish path and would show a disturbance as a SCORE change with the
// order possibly intact, while HNSW carries none and would show one as a changed
// neighbor order or a missing id.

package segmentdist

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

const (
	// equivDocs is past the fan-out budget, so driving the bound once consolidates.
	equivDocs = searchengine.ResidentSegmentFanoutBudget + 512
	// equivTerms is how many shared terms the corpus spreads over, so a query
	// returns a LIST to compare rather than a single hit. 97 over equivDocs gives
	// roughly fifty documents per term.
	equivTerms   = 97
	equivQueries = 24
	equivK       = 25
	// equivSpanSegments and equivSpanDocs seal a tail of MULTI-DOCUMENT segments on
	// top of the single-document ones.
	//
	// WITHOUT THEM THIS ROW CANNOT REACH THE CONSTITUENCY-CLOSURE GUARD, and would
	// stay green while a consolidation that drops spanning segments silently lost
	// their documents. A segment holding ONE id occupies one partition at every count
	// it will ever be read under, so a corpus built only from those contains nothing
	// for the guard to decline. These do span, and the fixture asserts that they do.
	equivSpanSegments = 64
	equivSpanDocs     = 8
)

// equivDoc is one corpus document, and EVERY DOCUMENT IS DISTINCT ON BOTH AXES on
// purpose.
//
// A FIXTURE OF EXACT TIES WOULD MAKE THIS ROW ASSERT SOMETHING FALSE, and the first
// draft of it did. Top-k over a tied score set has no defined membership beyond the
// tie: fifty documents at the identical BM25 score, cut to twenty-five, return
// whichever twenty-five the fan-out happened to reach first — which legitimately
// changes when the segment grouping changes, without any document, order or score
// having moved. The same held for the vector arm, where a short-period vector
// generator produced identical vectors and therefore identical distances.
//
// So the content length varies per document within a term's matching set (BM25
// length normalisation then separates them), and the vector is mixed from i in a way
// that does not repeat across the corpus. What the row asserts is then a real
// property rather than an artifact of arbitrary tie-breaking.
func equivDoc(i int) searchengine.Document {
	vec := make([]byte, 32)
	for b := range vec {
		h := uint64(i+1)*0x9E3779B97F4A7C15 + uint64(b+1)*0xC2B2AE3D27D4EB4F
		h ^= h >> 29
		h *= 0xBF58476D1CE4E5B9
		vec[b] = byte(h >> 33)
	}
	// The documents matching one term are i, i+equivTerms, i+2*equivTerms, ... so
	// this gives each of them its OWN filler length and therefore its own score.
	var filler strings.Builder
	for f := range i / equivTerms {
		filler.WriteString(fmt.Sprintf(" filler%03d", f))
	}
	return searchengine.Document{
		ID:     fmt.Sprintf("equiv-%05d", i),
		Vector: vec,
		Fields: map[string]string{
			searchengine.FieldContent: fmt.Sprintf("shared term%02d equiv%05d%s", i%equivTerms, i, filler.String()),
			searchengine.FieldSummary: fmt.Sprintf("term%02d", i%equivTerms),
		},
	}
}

// requireDistinctScores is the anti-tie control the fixture's own claim needs: if
// the corpus ever drifts back into producing equal scores, THAT is what a reader must
// be told, rather than the equality below failing for a reason it does not name.
func requireDistinctScores(t *testing.T, label string, hits []searchengine.Hit) {
	t.Helper()
	seen := make(map[float64]int, len(hits))
	for _, h := range hits {
		seen[h.Score]++
	}
	require.Len(t, seen, len(hits),
		"%s: the fixture must produce DISTINCT scores — top-k over a tie has no defined membership beyond the "+
			"tie, so an equality asserted over one would be asserting an artifact of the fan-out order", label)
}

// TestResultsAreIdenticalAcrossAConsolidation is the result-equivalence pin.
func TestResultsAreIdenticalAcrossAConsolidation(t *testing.T) {
	t.Run("the_field_engine_bm25v2", func(t *testing.T) {
		t.Parallel()
		const name = "equiv-bm25"
		m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		engine := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
			MinSegmentDocs:     1,
			SegmentCountTarget: searchengine.MergeDisabledCountTarget,
			DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		})
		t.Cleanup(engine.Close)
		installBM25Arm(m, name, engine)
		seedEquivCorpus(t, func(batch []searchengine.Document) {
			_, err := engine.AddSealAndSupersede(batch)
			require.NoError(t, err)
		})

		queries := make([]string, 0, equivQueries)
		for q := range equivQueries {
			queries = append(queries, fmt.Sprintf("shared term%02d", q))
		}
		before := bm25Results(t, engine, queries)

		consolidateEquivEngine(t, m.bm25ManagerFor(boundGraphType, name), engine)

		require.Equal(t, before, bm25Results(t, engine, queries),
			"a consolidation rewrites the physical layout of the corpus and must change NOTHING a searcher sees: "+
				"same documents, same order, same scores")
	})

	t.Run("the_vector_engine_hnswv3", func(t *testing.T) {
		t.Parallel()
		const name = "equiv-hnsw"
		m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		engine := searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{
			MinSegmentDocs:     1,
			SegmentCountTarget: searchengine.MergeDisabledCountTarget,
			DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		})
		t.Cleanup(engine.Close)
		installHNSWArm(m, name, engine)
		seedEquivCorpus(t, func(batch []searchengine.Document) {
			_, err := engine.AddSealAndSupersede(batch)
			require.NoError(t, err)
		})

		probes := make([][]byte, 0, equivQueries)
		for q := range equivQueries {
			probes = append(probes, equivDoc(q*13).Vector)
		}
		before := hnswResults(t, engine, probes)

		consolidateEquivEngine(t, m.managerFor(boundGraphType, name), engine)

		require.Equal(t, before, hnswResults(t, engine, probes),
			"the vector arm carries no corpus statistics, so a difference here is a changed neighbor order or a "+
				"missing id — either way a document a caller would no longer find where it was")
	})
}

// equivQuarantineBuckets is the partition count the quarantined-constituent arm is
// evaluated under. TWO rather than the corpus-derived count, and the reason is the
// row's own arithmetic: a consolidation stops at the low-water mark, so at the
// derived count (64 partitions of ~72 segments) the walk reaches the mark after a
// handful of partitions and the fixture cannot say whether the failing one was
// offered. At two partitions the failing one is half the corpus and is offered
// first. The segments sealed in batches still SPAN both partitions, so the
// constituency-closure guard is reached exactly as it is in the sibling arms.
const equivQuarantineBuckets = 2

// TestResultsAreIdenticalAcrossAConsolidationWithAQuarantinedConstituent is the
// result-equivalence pin with the RC's own wrinkle in it: one constituent of the
// partition being consolidated is CORRUPT, so the consolidation fails, the segment
// is withdrawn, and the partition is consolidated on the next crossing without it.
//
// TWO THINGS MUST BOTH BE TRUE, and the row would be half a row with either alone.
// The survivors must answer identically across that second consolidation — same
// documents, same order, same score bits — and the withdrawn segment's documents
// must be REPORTED MISSING rather than silently dropped: the engine's own resident
// accounting falls by exactly what that segment held, and every one of its ids comes
// back from UncoveredFrom, which is the arithmetic the coverage surface and the heal
// arms read.
func TestResultsAreIdenticalAcrossAConsolidationWithAQuarantinedConstituent(t *testing.T) {
	// Serial for the reason the corrupt file's rows state: the disposition logs
	// through the process-global slog default.
	const name = "equiv-quarantine"
	_, engine, dm, _, format := corruptBoundFixture(t, name)
	seedEquivCorpus(t, func(batch []searchengine.Document) {
		_, err := engine.AddSealAndSupersede(batch)
		require.NoError(t, err)
	})

	incident, err := bm25.New().Decode(incidentPayload(t))
	require.NoError(t, err)
	lost := incident.IDs()
	require.NotEmpty(t, lost, "PRECONDITION: the corrupt segment must index documents, or there is no loss to account for")
	require.Empty(t, engine.UncoveredFrom(lost),
		"PRECONDITION: while it is published, every document it holds is covered — the known positive for the "+
			"accounting assertion below, which would otherwise pass against an engine that never held them")
	docsBefore := engine.DistinctResidentDocCount()

	// The FAILING partition is the fuller of the two, so the most-populated-first
	// order offers it first; the marker is any document resident in it.
	spans := engine.SegmentSpans(equivQuarantineBuckets)
	candidates := consolidatablePartitions(spans)
	require.Len(t, candidates, equivQuarantineBuckets, "PRECONDITION: both partitions are consolidatable")
	failing := 0
	if len(candidates[1]) > len(candidates[0]) {
		failing = 1
	}
	format.marker = equivMarkerIn(t, failing)

	boundResidentSegments(dm, equivQuarantineBuckets)

	require.Positive(t, format.injections.Load(),
		"ANTI-VACUITY: the corrupt segment must have reached the merge of the failing partition")
	require.NotContains(t, engine.ResidentSegmentIDs(), incidentCorruptID,
		"the corrupt constituent must be withdrawn by the consolidation that found it")
	require.Equal(t, docsBefore-len(lost), engine.DistinctResidentDocCount(),
		"the withdrawal must cost EXACTLY the documents that segment held (%d of %d): an accounting that did not "+
			"see the loss would leave the corpus permanently short while every coverage gate reported it whole",
		len(lost), docsBefore)
	require.Len(t, engine.UncoveredFrom(lost), len(lost),
		"and every one of them must be REPORTED missing — silently dropping them is the failure this row exists for")

	// Cross the budget again so the next crossing censuses, THEN record the answers:
	// nothing is added or removed between the two readings below, so any difference
	// is the consolidation and nothing else.
	for i := range equivRefillDocs {
		_, err := engine.AddSealAndSupersede([]searchengine.Document{equivDoc(equivRefillFrom + i)})
		require.NoError(t, err)
	}
	require.Greater(t, engine.ResidentSegmentCount(), searchengine.ResidentSegmentFanoutBudget,
		"PRECONDITION: over budget again, or the second crossing returns at the gate and the equality is vacuous")

	queries := make([]string, 0, equivQueries)
	for q := range equivQueries {
		queries = append(queries, fmt.Sprintf("shared term%02d", q))
	}
	before := bm25Results(t, engine, queries)
	constituentsBefore := len(engine.BucketConstituents(failing, equivQuarantineBuckets))
	docsHeld := engine.DistinctResidentDocCount()

	boundResidentSegments(dm, equivQuarantineBuckets)

	require.Less(t, len(engine.BucketConstituents(failing, equivQuarantineBuckets)), constituentsBefore,
		"ANTI-VACUITY: the partition the corrupt segment was failing (%d constituents) must now consolidate WITHOUT "+
			"it, or this row asserts that doing nothing changes nothing", constituentsBefore)
	require.Equal(t, int64(1), format.injections.Load(),
		"and the corrupt segment must not have been offered again: withdrawn means out of every later candidate set")
	require.Equal(t, docsHeld, engine.DistinctResidentDocCount(),
		"the consolidation moved no documents: the corpus is held constant so any difference below is the "+
			"consolidation rather than a changed corpus")
	require.Equal(t, before, bm25Results(t, engine, queries),
		"a consolidation that followed a withdrawal must change NOTHING a searcher sees about the SURVIVORS: "+
			"same documents, same order, same scores")
}

// equivRefillFrom and equivRefillDocs re-cross the budget after the first census
// has consolidated a partition, one single-document segment each. The offset is
// past every index seedEquivCorpus uses, so no document is written twice.
const (
	equivRefillFrom = 100_000
	equivRefillDocs = 2_048
)

// equivMarkerIn returns the id of a corpus document that lives in the named
// partition at equivQuarantineBuckets. It SEARCHES the corpus's own ids rather than
// synthesizing one, because the marker has to be LIVE in a constituent of that
// partition for the accept predicates to name it.
func equivMarkerIn(t *testing.T, bucket int) searchengine.ExternalID {
	t.Helper()
	for i := range equivDocs {
		id := equivDoc(i).ID
		if searchengine.BucketOf(id, equivQuarantineBuckets) == bucket {
			return id
		}
	}
	t.Fatalf("equivMarkerIn(%d): no corpus document landed in partition %d of %d",
		bucket, bucket, equivQuarantineBuckets)
	return ""
}

// seedEquivCorpus builds the corpus: mostly one document per segment, so the engine
// crosses the budget on segment COUNT without needing a large document population,
// then a tail of multi-document segments so the set contains segments that SPAN
// several partitions (see equivSpanSegments).
func seedEquivCorpus(t *testing.T, seal func([]searchengine.Document)) {
	t.Helper()
	for i := range equivDocs {
		seal([]searchengine.Document{equivDoc(i)})
	}
	for sg := range equivSpanSegments {
		batch := make([]searchengine.Document, 0, equivSpanDocs)
		for d := range equivSpanDocs {
			batch = append(batch, equivDoc(equivDocs+sg*equivSpanDocs+d))
		}
		seal(batch)
	}
}

// consolidateEquivEngine drives the bound ONCE, adding and removing no documents,
// and proves it actually consolidated something.
func consolidateEquivEngine[Q, S any](
	t *testing.T, dm *distManager[Q, S], engine *searchengine.SegmentedIndex[Q, S],
) {
	t.Helper()
	bucketCount := searchengine.BucketCountFor(engine.DistinctResidentDocCount())
	segmentsBefore := engine.ResidentSegmentCount()
	docsBefore := engine.DistinctResidentDocCount()
	require.Greater(t, segmentsBefore, searchengine.ResidentSegmentFanoutBudget,
		"PRECONDITION: the engine must be over budget, or the bound returns without consolidating and the "+
			"equality below compares a reading with itself")

	// AND THE SET MUST CONTAIN SEGMENTS THE GUARD HAS TO DECLINE. Without them the
	// constituency-closure guard is never consulted and this row would stay green
	// while a consolidation that drops spanning segments lost every document they
	// hold for the partitions it is not rebuilding.
	spanning := 0
	for _, buckets := range engine.SegmentSpans(bucketCount) {
		if len(buckets) > 1 {
			spanning++
		}
	}
	require.Positive(t, spanning,
		"ANTI-VACUITY: no resident segment spans more than one partition at count %d, so the closure guard is "+
			"never reached and the equality below cannot observe a consolidation that loses data", bucketCount)
	t.Logf("equivalence fixture: %d resident segments, %d spanning, bucket_count %d",
		segmentsBefore, spanning, bucketCount)

	boundResidentSegments(dm, bucketCount)

	require.Less(t, engine.ResidentSegmentCount(), segmentsBefore,
		"ANTI-VACUITY: the consolidation must actually have merged segments (%d before), or this row asserts "+
			"that doing nothing changes nothing", segmentsBefore)
	require.Equal(t, docsBefore, engine.DistinctResidentDocCount(),
		"and it must have moved no DOCUMENTS: the corpus is held constant so any score difference is "+
			"attributable to the consolidation rather than to a changed corpus")
}

// bm25Results records the full ordered hit list, with scores, for every query.
func bm25Results(t *testing.T, e *searchengine.SegmentedIndex[bm25.Query, *bm25.CorpusStats], queries []string) []string {
	t.Helper()
	out := make([]string, 0, len(queries))
	nonEmpty := 0
	for _, q := range queries {
		hits := e.Search(bm25.NewQuery(q), equivK)
		if len(hits) > 1 {
			nonEmpty++
			requireDistinctScores(t, q, hits)
		}
		out = append(out, renderHits(q, hits))
	}
	require.Greater(t, nonEmpty, len(queries)/2,
		"ANTI-VACUITY: most queries must return a LIST, or an equality over empty results proves nothing")
	return out
}

// hnswResults is bm25Results for the vector arm, and it asserts a DIFFERENT SHAPE of
// the same property because the two arms tie differently.
//
// THE VECTOR KERNEL QUANTISES, so distance ties are not a fixture accident the way
// the BM25 ones were: scores land on multiples of 1/256, and over a corpus of random
// vectors many neighbors genuinely share a value. Top-k over a tie has no defined
// membership beyond the tie, so demanding a bit-identical list of twenty-five would
// be demanding something no search promises — and it would go red on a correct
// consolidation. Two things ARE promised and are asserted here:
//
//   - THE DISTANCES DO NOT MOVE. The full sorted multiset of scores is identical, so
//     a consolidation that changed what a vector compares as would be caught whether
//     or not it reordered anything.
//   - THE UNAMBIGUOUS PREFIX DOES NOT MOVE. Up to the first repeated score, order and
//     membership are defined, and those hits are compared as an ordered list.
func hnswResults(t *testing.T, e *searchengine.SegmentedIndex[[]byte, struct{}], probes [][]byte) []string {
	t.Helper()
	out := make([]string, 0, len(probes)*2)
	nonEmpty := 0
	for i, p := range probes {
		hits := e.Search(p, equivK)
		label := fmt.Sprintf("probe%02d", i)
		if len(hits) > 1 {
			nonEmpty++
		}
		prefix := tieFreePrefix(hits)
		require.NotEmpty(t, prefix,
			"%s: the nearest neighbor must be unambiguous, or this probe compares nothing that is defined", label)
		out = append(out, renderHits(label+"/prefix", prefix), renderScoreMultiset(label, hits))
	}
	require.Greater(t, nonEmpty, len(probes)/2,
		"ANTI-VACUITY: most probes must return a LIST, or an equality over empty results proves nothing")
	return out
}

// tieFreePrefix returns the leading hits whose scores are strictly decreasing, which
// is exactly the part of a ranking whose ORDER and MEMBERSHIP are defined.
func tieFreePrefix(hits []searchengine.Hit) []searchengine.Hit {
	for i := 1; i < len(hits); i++ {
		if hits[i].Score >= hits[i-1].Score {
			return hits[:i]
		}
	}
	return hits
}

// renderScoreMultiset renders the sorted score values of a whole answer, so a change
// in any DISTANCE shows up even where the tied membership is free to differ.
func renderScoreMultiset(label string, hits []searchengine.Hit) string {
	scores := make([]float64, 0, len(hits))
	for _, h := range hits {
		scores = append(scores, h.Score)
	}
	slices.Sort(scores)
	var s strings.Builder
	s.WriteString(label + "/scores:")
	for _, v := range scores {
		s.WriteString(fmt.Sprintf(" %v", v))
	}
	return s.String()
}

// renderHits formats one query's answer so a difference in membership, in ORDER or
// in any score bit shows up as a string difference a reader can diff by eye. The
// score is rendered with %v on a float64, which is its shortest round-trippable
// form: two scores render identically exactly when they are the same float.
func renderHits(label string, hits []searchengine.Hit) string {
	var s strings.Builder
	s.WriteString(label + ":")
	for _, h := range hits {
		s.WriteString(fmt.Sprintf(" %s=%v", h.ID, h.Score))
	}
	return s.String()
}

// installHNSWArm is installBM25Arm for the vector engine.
func installHNSWArm(m *Manager, name string, engine *searchengine.SegmentedIndex[[]byte, struct{}]) {
	dm := newDistManager(engine, nil, graphSelector(boundGraphType, name), hnsw.New().Name())
	gate := &constructionGate[[]byte, struct{}]{dm: dm, done: make(chan struct{})}
	close(gate.done)
	m.mu.Lock()
	m.managers[graphKey{graphType: boundGraphType, graphName: name}] = gate
	m.mu.Unlock()
}
