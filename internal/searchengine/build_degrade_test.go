package searchengine

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// build_degrade_test.go gates the CARRIER: a census a format produced must reach
// the engine's owner from EVERY path that builds, and a clean build must tell the
// owner nothing at all.
//
// THE INSTRUMENT INJECTS RATHER THAN POISONING A DOCUMENT, deliberately. On the
// fixed tree no document input reaches a degrade class, so a poisoned-document
// test could only assert that the count stays zero — which proves nothing about
// the carrier. The census SOURCE is proven separately, against the pre-fix
// behaviour, by the BM25 format's own per-document recovery test.

// censusFormat reports a SCRIPTED census on every build, so a test can drive the
// carrier without a format that actually fails.
type censusFormat struct {
	mockFormat
	census map[string]int
}

func (f censusFormat) Build(docs []Document) (Segment[mockQuery, mockStats], BuildReport, error) {
	seg, _, err := f.mockFormat.Build(docs)
	return seg, BuildReport{Degraded: f.census}, err
}

// degradeRecorder is the owner: it records every census the engine hands it.
type degradeRecorder struct {
	mu      sync.Mutex
	reports []BuildReport
}

func (r *degradeRecorder) record(rep BuildReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, rep)
}

func (r *degradeRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reports)
}

// degradeEngine builds a mock engine wired to an owner that records censuses,
// mirroring layerEngine's options so segment layout stays the test's to own.
func degradeEngine(
	t testing.TB, census map[string]int, rec *degradeRecorder,
) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	return closeOnCleanup(t, New[mockQuery, mockStats](censusFormat{census: census}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
		OnBuildDegrade:     rec.record,
	}))
}

// TestOnBuildDegradeFiresOnlyOnANonEmptyCensus pins the hook to being a SIGNAL.
//
// BOTH SUBTESTS ARE LOAD-BEARING and neither substitutes for the other: without
// the second, a hook that fired on every build — the shape an engineer writes
// when the nil-hook check reads as sufficient — passes the first.
func TestOnBuildDegradeFiresOnlyOnANonEmptyCensus(t *testing.T) {
	t.Run("a non-empty census reaches the owner", func(t *testing.T) {
		rec := &degradeRecorder{}
		e := degradeEngine(t, map[string]int{"tokenize_panic": 2}, rec)

		require.NoError(t, e.Add([]Document{doc("a", "alpha")}))
		require.NoError(t, e.Flush())

		require.GreaterOrEqual(t, rec.count(), 1, "the owner must be handed the census the format reported")
		require.Equal(t, map[string]int{"tokenize_panic": 2}, rec.reports[0].Degraded)
	})

	t.Run("a clean build does not fire the hook at all", func(t *testing.T) {
		rec := &degradeRecorder{}
		e := degradeEngine(t, nil, rec)

		require.NoError(t, e.Add([]Document{doc("a", "alpha")}))
		require.NoError(t, e.Flush())

		require.Equal(t, 0, rec.count(),
			"an engine that fired on every build would train an owner to ignore the hook")
	})
}

// TestEveryEngineBuildPathDeliversTheCensus drives EVERY engine path that calls
// format.Build and asserts the owner was handed the census on each.
//
// ONE SUBTEST PER DELIVERY PATH IS THE RIGHT INSTRUMENT COUNT, and the reason is
// measured rather than aesthetic: the four sites are separate lines that a uniform
// sweep rewrites independently, and each is reached from a different public entry
// point. On a tree where the three non-seal sites take the sweep template's
// two-value form and discard the report, the tree COMPILES and vets clean and
// three of these four go red while seal passes — so a seal-only or BuildLayer-only
// leg would leave the others ungated.
func TestEveryEngineBuildPathDeliversTheCensus(t *testing.T) {
	census := map[string]int{"tokenize_panic": 1}
	docs := []Document{doc("a", "alpha"), doc("b", "beta")}

	paths := []struct {
		name  string
		drive func(t *testing.T, e *SegmentedIndex[mockQuery, mockStats])
	}{
		{"Add reaches seal", func(t *testing.T, e *SegmentedIndex[mockQuery, mockStats]) {
			require.NoError(t, e.Add(docs))
			require.NoError(t, e.Flush())
		}},
		{"ReplaceBucket", func(t *testing.T, e *SegmentedIndex[mockQuery, mockStats]) {
			_, err := e.ReplaceBucket(0, 1, nil, nil, docs)
			require.NoError(t, err)
		}},
		{"ReplaceBucketGroup reaches harvestPartition", func(t *testing.T, e *SegmentedIndex[mockQuery, mockStats]) {
			_, _, err := e.ReplaceBucketGroup(1, nil, []BucketWork{{Bucket: 0, Docs: docs}})
			require.NoError(t, err)
		}},
		{"BuildLayer", func(t *testing.T, e *SegmentedIndex[mockQuery, mockStats]) {
			_, err := e.BuildLayer([]BucketWork{{Bucket: 0, Docs: docs}})
			require.NoError(t, err)
		}},
	}
	// The table is FLOORED so a path dropped from it fails here rather than
	// shrinking the assertion.
	require.Len(t, paths, 4, "every engine site that calls format.Build gets its own subtest")

	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			rec := &degradeRecorder{}
			e := degradeEngine(t, census, rec)
			p.drive(t, e)
			require.GreaterOrEqual(t, rec.count(), 1,
				"this delivery path built a segment from a format reporting a census and told the owner nothing")
		})
	}
}
