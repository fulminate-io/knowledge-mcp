// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// Fixture constants. probePerBucket is sized so BucketCountFor(probeCorpusN) lands
// on probeBuckets with groups of production-like size (~680 documents), and
// priorBuckets makes the stage-C ship diff a STRICT SUBSET — which is the whole
// point: a fixture that happens to cache everything makes the gate vacuous, and
// that vacuity is the shape that let the defect through.
const (
	probePerBucket = 680
	probeBuckets   = 16
	probeCorpusN   = probeBuckets * probePerBucket
	priorBuckets   = 12
	newBuckets     = probeBuckets - priorBuckets
	// probeGraphType is the one graph type every fixture here uses. The helpers read
	// it directly rather than taking it as a parameter: a parameter that only ever
	// receives one value reads as configurable when it is not.
	probeGraphType = kgtypes.GraphCode
	// completenessGraphName is the class-fix fixture's graph.
	completenessGraphName = "completeness-gate"
)

// probeDocs builds a corpus carrying both a vector and a content field, so the same
// documents drive the HNSW and the BM25 arm.
func probeDocs(n int) []searchengine.Document {
	docs := make([]searchengine.Document, n)
	for i := range docs {
		vec := make([]byte, 32)
		for b := range vec {
			vec[b] = byte((i*31 + b*7) % 251)
		}
		id := fmt.Sprintf("probe-%07d", i)
		docs[i] = searchengine.Document{
			ID:     id,
			Vector: vec,
			Fields: map[string]string{searchengine.FieldContent: "alpha beta gamma " + id},
		}
	}
	return docs
}

// groupByBucket partitions a corpus exactly as buildAndAddRebuildSegments does.
func groupByBucket(corpus []searchengine.Document, bucketCount int) (map[int][]searchengine.Document, []int) {
	groups := map[int][]searchengine.Document{}
	for _, d := range corpus {
		b := searchengine.BucketOf(d.ID, bucketCount)
		groups[b] = append(groups[b], d)
	}
	buckets := make([]int, 0, len(groups))
	for b := range groups {
		buckets = append(buckets, b)
	}
	sort.Ints(buckets)
	return groups, buckets
}

// driveRebuild runs the rebuild driver's serial per-bucket staging loop verbatim
// (rebuild_segments_scan_build.go buildAndAddRebuildSegments) over the named buckets,
// then the single serial finalize.
//
// `built` IS COUNTED AFTER THE FINALIZE, and under staging it has to be: nothing is
// written to an engine until the finalize builds every staged partition at once, so
// before it there is no resident set to count. Counting it afterwards reads exactly this
// run's layer, because the finalize replaces the layer whole rather than adding to it.
func driveRebuild(
	t *testing.T, mgr *Manager, name string,
	groups map[int][]searchengine.Document, buckets []int,
) (built int, superseded []searchengine.SegmentID, swapped bool) {
	t.Helper()
	ctx := context.Background()
	gt := probeGraphType
	for _, b := range buckets {
		group := groups[b]
		hdocs := make([]searchengine.Document, 0, len(group))
		bdocs := make([]searchengine.Document, 0, len(group))
		for _, d := range group {
			hdocs = append(hdocs, searchengine.Document{ID: d.ID, Vector: d.Vector})
			bdocs = append(bdocs, searchengine.Document{ID: d.ID, Fields: d.Fields})
		}
		if err := mgr.StageRebuildPartition(ctx, gt, name, hdocs, bdocs); err != nil {
			t.Fatalf("StageRebuildPartition bucket %d: %v", b, err)
		}
	}
	res, err := mgr.FinalizeRebuild(ctx, gt, name)
	if err != nil {
		t.Fatalf("FinalizeRebuild: %v", err)
	}
	built = len(mgr.managerFor(gt, name).engine.Export())
	return built, res.HNSWSuperseded, res.Swapped
}

// l2Files counts the .seg files in the completeness fixture's graph+format L2 cache
// root. The graph is fixed rather than a parameter for the same reason the type is:
// only one fixture inspects the cache directory, and a parameter with one possible
// value advertises a flexibility that does not exist.
func l2Files(t *testing.T, base, format string) int {
	t.Helper()
	entries, err := os.ReadDir(graphCacheDirFor(base, probeGraphType, completenessGraphName, format))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read L2 dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".seg" {
			n++
		}
	}
	return n
}

// quarantineL2 removes both format roots, modeling the operator's
// default.quarantine-<epoch> move. The rebuild-state directory (which holds the
// manifest fingerprint) is deliberately LEFT ALONE — the operator moved the blob
// pools aside, not the record, and that asymmetry is exactly the incident.
func quarantineL2(t *testing.T, base, name string) {
	t.Helper()
	for _, f := range []string{hnsw.New().Name(), bm25.New().Name()} {
		if err := os.RemoveAll(graphCacheDirFor(base, probeGraphType, name, f)); err != nil {
			t.Fatalf("quarantine %s: %v", f, err)
		}
	}
}

// TestReconcileFetchesManifestEntriesMissingFromL2 is the class-fix gate. It is the
// promoted investigation rig: the same five stages, with STAGE D INVERTED — the rig
// asserted the partial value (24.7% resident) to characterize the defect; this
// asserts COMPLETENESS and keeps the partial value only as the failure-message
// contrast.
//
// THE PRODUCTION CALL THAT HAS TO BE WRONG is loadResidentFromL2 accepting a partial
// cache as final with nothing later repairing it. The repair is therefore driven
// through ReconcileManifestCompleteness — the off-hot-path entry the boot-delay
// one-shot and the periodic reconcile call — never by loading and never by calling
// List directly. A test that triggered the fetch either of those ways would pass
// against an implementation that reads the manifest every tick AND against one that
// never gates at all.
//
// THE ASSERTION IS ONE-DIRECTIONAL: the resident set must CONTAIN every manifest id.
// It is deliberately not equality and not trimming — the un-reclaimed merge window
// is a live producer of legitimate supersets (manager_load.go:232-240), so an
// equality gate would fail correct work.
func TestReconcileFetchesManifestEntriesMissingFromL2(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := probeGraphType, completenessGraphName
	base := t.TempDir()
	svc, _ := newSegmentHarness(t)

	corpus := probeDocs(probeCorpusN)
	bc := searchengine.BucketCountFor(len(corpus))
	if bc != probeBuckets {
		t.Fatalf("fixture: bucketCount=%d want %d", bc, probeBuckets)
	}
	groups, buckets := groupByBucket(corpus, bc)
	target := graphSelector(gt, name)

	// The LIVE CLOUD SHAPE: List(0) reads the published manifest (as the GCS source
	// does), and the agent verifies completeness server-side so the client-side
	// subset gate is off — the combination the manifest-swap paths actually run under.
	newMgr := func() (*Manager, *fakeSegmentSource) {
		view := svc.viewFor(target, "")
		view.listFromManifest = true
		view.verifies = true
		return closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, base, 0, withSegmentSource(view))), view
	}

	// ---- STAGE A: the PRIOR corpus — a rebuild that emitted only 12 of 16 buckets.
	mgrA, _ := newMgr()
	if _, _, swapped := driveRebuild(t, mgrA, name, groups, buckets[:priorBuckets]); !swapped {
		t.Fatal("stage A: the prior rebuild did not land a manifest swap")
	}
	hnswBeforeQuarantine := l2Files(t, base, hnsw.New().Name())
	t.Logf("STAGE A (prior state): manifestRefs=%d | L2 hnsw=%d bm25=%d",
		len(svc.manifestMetas(target, "")), hnswBeforeQuarantine, l2Files(t, base, bm25.New().Name()))

	// ---- STAGE B: the operator quarantines the L2 blob pools. Server untouched.
	// KNOWN-POSITIVE FIRST: the want-zero below is only meaningful if the same
	// measurement reads NON-zero before the quarantine. A count taken against a
	// directory that does not exist also returns 0, so without this the assertion
	// would pass identically whether the quarantine worked or the format name had
	// drifted out from under l2Files.
	if hnswBeforeQuarantine == 0 {
		t.Fatalf("stage A: L2 hnsw=0 before the quarantine — the want-zero at stage B "+
			"would pass vacuously; l2Files is looking at the wrong tree (format %q)", hnsw.New().Name())
	}
	quarantineL2(t, base, name)
	if got := l2Files(t, base, hnsw.New().Name()); got != 0 {
		t.Fatalf("stage B: L2 hnsw=%d, want 0 after the quarantine", got)
	}

	// ---- STAGE C: the FULL rebuild. Ships only the buckets the server lacks.
	mgrC, _ := newMgr()
	builtC, supC, swappedC := driveRebuild(t, mgrC, name, groups, buckets)
	hnswL2, bm25L2 := l2Files(t, base, hnsw.New().Name()), l2Files(t, base, bm25.New().Name())
	t.Logf("STAGE C (full rebuild): built=%d swapped=%v superseded=%d | manifest=%d per format | L2 hnsw=%d bm25=%d",
		builtC, swappedC, len(supC), len(svc.manifestMetas(target, ""))/2, hnswL2, bm25L2)

	// Stage C's predictions, kept from the rig: the publish is COMPLETE while the
	// cache is not, which is the whole mechanism in four numbers.
	if builtC != probeBuckets {
		t.Fatalf("stage C: built %d segments, want %d", builtC, probeBuckets)
	}
	if len(supC) != 0 {
		t.Fatalf("stage C: superseded=%d, want 0 (the incident reported 0 pruned)", len(supC))
	}
	if got := len(svc.manifestMetas(target, "")) / 2; got != probeBuckets {
		t.Fatalf("stage C: manifest holds %d per format, want the FULL %d — "+
			"item 1 exonerates the manifest, so a short one here means the fixture is not the incident", got, probeBuckets)
	}
	if hnswL2 != newBuckets || bm25L2 != newBuckets {
		t.Fatalf("stage C: L2 hnsw=%d bm25=%d, want %d each — the fixture must cache a STRICT SUBSET",
			hnswL2, bm25L2, newBuckets)
	}

	// ---- STAGE D (INVERTED): the restart. A fresh Manager over the PARTIAL cache.
	mgrD, viewD := newMgr()
	partial, err := mgrD.LoadResidentDocCount(ctx, gt, name)
	if err != nil {
		t.Fatalf("stage D load: %v", err)
	}
	listsBefore := viewD.listCalls.Load()

	if err := mgrD.ReconcileManifestCompleteness(ctx, gt, name); err != nil {
		t.Fatalf("stage D completeness reconcile: %v", err)
	}

	// The mismatch must have been what paid for the read — not a per-tick List.
	if got := viewD.listCalls.Load(); got <= listsBefore {
		t.Errorf("stage D: listCalls %d -> %d — the cache/fingerprint mismatch did not trigger the manifest read",
			listsBefore, got)
	}

	// COMPLETENESS, ONE-DIRECTIONAL: every published id is resident.
	for _, format := range []string{hnsw.New().Name(), bm25.New().Name()} {
		assertResidentContainsManifest(t, mgrD, svc, target, gt, name, format)
	}
	converged := mgrD.ResidentDocCount(gt, name)
	if converged != probeCorpusN {
		t.Errorf("stage D: resident hnsw docs=%d, want the full corpus %d "+
			"(pre-convergence contrast: %d docs = %.1f%% — the partial-cache reading the rig characterized)",
			converged, probeCorpusN, partial, 100*float64(partial)/float64(probeCorpusN))
	}

	// ---- STAGE D2: the cache-LARGER case must take NO action and pay NO read.
	// Recording a fingerprint SMALLER than the cache is the documented superset
	// condition stated in the gate's own inputs (an un-reclaimed merge window leaves
	// more on disk than the manifest names).
	if err := mgrD.saveManifestFingerprint(gt, name, hnsw.New().Name(), manifestFingerprint{Count: 3, Hash: "stale"}); err != nil {
		t.Fatalf("stage D2 seed: %v", err)
	}
	if err := mgrD.saveManifestFingerprint(gt, name, bm25.New().Name(), manifestFingerprint{Count: 3, Hash: "stale"}); err != nil {
		t.Fatalf("stage D2 seed: %v", err)
	}
	listsQuiet := viewD.listCalls.Load()
	if err := mgrD.ReconcileManifestCompleteness(ctx, gt, name); err != nil {
		t.Fatalf("stage D2 completeness reconcile: %v", err)
	}
	if got := viewD.listCalls.Load(); got != listsQuiet {
		t.Errorf("stage D2: listCalls %d -> %d — a cache LARGER than the recorded manifest is the documented "+
			"superset and must cost ZERO reads; firing there destroys the zero-RPC healthy arm", listsQuiet, got)
	}

	// ---- STAGE E: CONTROL. A cold cache recovers the full corpus from the server,
	// which is what proves the server state was intact all along and that stage D
	// measured a client-side shortfall rather than a lost corpus.
	quarantineL2(t, base, name)
	mgrE, _ := newMgr()
	residentE, err := mgrE.LoadResidentDocCount(ctx, gt, name)
	if err != nil {
		t.Fatalf("stage E load: %v", err)
	}
	if residentE != probeCorpusN {
		t.Errorf("stage E CONTROL: cold-L2 resident=%d, want the full %d — the server corpus is NOT intact, "+
			"so stage D was not measuring a client-side shortfall", residentE, probeCorpusN)
	}
}

// assertResidentContainsManifest is the one-directional completeness assertion for
// one format arm: every id in the published manifest is resident in the engine.
func assertResidentContainsManifest(
	t *testing.T, mgr *Manager, svc *sharedServerFake, target *knowledgev1.GraphSelector,
	gt kgtypes.GraphType, name, format string,
) {
	t.Helper()
	var arm completenessArm
	if format == bm25.New().Name() {
		arm = mgr.bm25ManagerFor(gt, name)
	} else {
		arm = mgr.managerFor(gt, name)
	}
	resident := map[searchengine.SegmentID]struct{}{}
	for _, id := range arm.armResidentIDs() {
		resident[id] = struct{}{}
	}
	var missing []searchengine.SegmentID
	published := 0
	for _, m := range svc.manifestMetas(target, "") {
		if m.GetFormat() != format {
			continue
		}
		published++
		if _, live := resident[m.GetId()]; !live {
			missing = append(missing, m.GetId())
		}
	}
	if published == 0 {
		t.Fatalf("%s: the manifest names no segments — the fixture published nothing to converge toward", format)
	}
	if len(missing) > 0 {
		t.Errorf("%s: %d of %d published segments are NOT resident after the completeness reconcile: %v",
			format, len(missing), published, missing)
	}
}

// TestDegradedSourceStillServesL2WithWarning gates the ONLY detector on this
// failure mode. The read-side coverage ratio cannot see a partial cache that still
// clears the floor, so if the reconcile's read fails or times out silently, nothing
// anywhere reports the shortfall.
//
// THE PRODUCTION CALL THAT HAS TO BE WRONG is the reconcile's manifest read failing
// without a WARN reaching the handler. The assertion is therefore over the real
// slog default (installCapturingSlog), driven through the real Manager, with a
// source whose List genuinely errors — not over a test logger and not over a mock.
//
// DELIBERATELY NOT PARALLEL. Shared resource: the process-global default slog
// logger, which this test swaps for a capturing handler to assert over the
// records the path emits. Concurrent peers would both install and restore that
// one global, so the handler this test reads could be a peer's, and a peer's
// unrelated records would land in this test's capture.
func TestDegradedSourceStillServesL2WithWarning(t *testing.T) {
	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "completeness-degrade"
	base := t.TempDir()
	svc, _ := newSegmentHarness(t)
	target := graphSelector(gt, name)

	corpus := probeDocs(probeCorpusN)
	groups, buckets := groupByBucket(corpus, searchengine.BucketCountFor(len(corpus)))

	newMgr := func() (*Manager, *fakeSegmentSource) {
		view := svc.viewFor(target, "")
		view.listFromManifest = true
		view.verifies = true
		return closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, base, 0, withSegmentSource(view))), view
	}

	// Prior state + quarantine + full rebuild: the same partial-cache shape as the
	// class-fix gate, so the reconcile below genuinely has a shortfall to repair.
	mgrA, _ := newMgr()
	driveRebuild(t, mgrA, name, groups, buckets[:priorBuckets])
	quarantineL2(t, base, name)
	mgrC, _ := newMgr()
	driveRebuild(t, mgrC, name, groups, buckets)

	// The restart, with a source whose manifest read FAILS.
	logs := installCapturingSlog(t)
	mgrD, viewD := newMgr()
	if _, err := mgrD.LoadResidentDocCount(ctx, gt, name); err != nil {
		t.Fatalf("degraded load: %v", err)
	}
	servedBefore := mgrD.ResidentDocCount(gt, name)
	viewD.listErr = errors.New("segment registry unreachable")

	// Never fail closed: the pass reports the error upward for logging, and the
	// engine keeps serving whatever L2 gave it.
	_ = mgrD.ReconcileManifestCompleteness(ctx, gt, name)

	if got := mgrD.ResidentDocCount(gt, name); got != servedBefore {
		t.Errorf("a failed manifest read changed the served set: %d -> %d — the degrade path must leave L2 serving",
			servedBefore, got)
	}
	if servedBefore == 0 {
		t.Fatal("fixture: the degraded arm served nothing, so 'still serves L2' is vacuous")
	}

	warns := logs.warnsContaining("manifest completeness read FAILED")
	if len(warns) == 0 {
		t.Fatalf("no WARN for the failed manifest read — this is the only detector on this failure mode; got %d records",
			len(logs.records))
	}
	// N and M both, or an operator cannot tell a small shortfall from a total one.
	for _, w := range warns {
		if !containsAll(w, []string{"resident=", "manifest_expected=", "graph_type=", "name=", "format="}) {
			t.Errorf("the degrade WARN is missing N, M or the graph identity: %q", w)
		}
	}

	// IT RE-EMITS WHILE THE SHORTFALL PERSISTS. A detector that announces itself once
	// and goes quiet is how a degraded graph survives a week of ticks unnoticed.
	before := len(logs.warnsContaining("manifest completeness read FAILED"))
	_ = mgrD.ReconcileManifestCompleteness(ctx, gt, name)
	if after := len(logs.warnsContaining("manifest completeness read FAILED")); after <= before {
		t.Errorf("the degrade WARN did not re-emit on the next tick (%d -> %d) while the shortfall persisted",
			before, after)
	}
}

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
