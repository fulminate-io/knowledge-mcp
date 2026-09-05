// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// --- fakes -------------------------------------------------------------------

// fakeRebuildScanner returns scripted segment_rebuild pages keyed by the after_id
// cursor. It records every requested after_id so the test can assert the cursor
// advanced to each page's last node_id and terminated on the empty page.
type fakeRebuildScanner struct {
	mu         sync.Mutex
	pages      [][]*knowledgev1.PipelineScanItem // returned in order
	calls      int
	cursors    []string
	graphTypes []string // the GraphType requested on each PipelineScan
	// afters records after_stamped_at_nanos per call — the field the server reads
	// as the client's reported retention position — and scanFroms records
	// scan_from_stamped_at_nanos, the bound the scan itself reads from. They are
	// appended in the SAME statement so index i of one always describes the same
	// request as index i of the other; a test that read the floor of one call beside
	// the scan bound of another would assert a request nobody sent.
	afters    []int64
	scanFroms []int64
	pageIter  int
}

func (f *fakeRebuildScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.cursors = append(f.cursors, req.GetAfterId())
	f.graphTypes = append(f.graphTypes, req.GetGraphType())
	f.afters, f.scanFroms = append(f.afters, req.GetAfterStampedAtNanos()), append(f.scanFroms, req.GetScanFromStampedAtNanos())
	if f.pageIter >= len(f.pages) {
		return &knowledgev1.PipelineScanResponse{Items: nil}, nil
	}
	page := f.pages[f.pageIter]
	f.pageIter++
	return &knowledgev1.PipelineScanResponse{Items: page}, nil
}

func (f *fakeRebuildScanner) Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// fakeRebuildState is the in-memory stand-in for the durable per-graph rebuild
// record, shared by every shipper fake so they all satisfy the state half of the
// SegmentShipper seam without each re-implementing it. It records what the driver
// saved and what it handed the engines, which is what the watermark tests assert
// against.
type fakeRebuildState struct {
	stateMu sync.Mutex

	// watermark/tombstoned are what a load returns AND what a save overwrites, so a
	// test reads the post-run values straight off the fake.
	watermark  int64
	tombstoned []searchengine.ExternalID

	saves   int
	loadErr error
	saveErr error

	// seeded records every SetGraphTombstones call, so a test can prove the driver
	// hands the engines the ids that must be seeded dead at Import.
	seeded [][]searchengine.ExternalID

	// noted records every NoteDeletedIDs call, so a test can prove a caller stamped
	// the ids ITS OWN window reported rather than the merged set.
	noted [][]searchengine.ExternalID

	// mergeWatermark is the OTHER consumer's durable position, which the retention
	// floor is taken against. Its zero value means that consumer has confirmed
	// nothing, which correctly holds the reported floor at zero.
	mergeWatermark    int64
	mergeWatermarkErr error

	// bm25Resets counts ResetBM25Cursors. It is the BM25 arm's feed position being
	// cleared because this run is about to replace the BM25 layer the arm's cursor
	// describes; bm25ResetErr scripts the failure the driver must abort on.
	bm25Resets   int
	bm25ResetErr error

	// degradeCensus is a REAL census rather than a stub returning nil: the reset
	// CLEARS this field and the read RETURNS it, so a test can observe the
	// difference between a driver that cleared and one that did not.
	degradeCensus map[string]int
	// finalizeCensus is what THE FINALIZE'S OWN BUILDS drop, applied by
	// applyFinalizeCensus. Modeling that is what makes the ordering assertion mean
	// anything: production clears the record and the finalize's builds then fill
	// it, so a fake that merely HELD a census would pass whether the driver cleared
	// before or after the finalize.
	finalizeCensus map[string]int
	degradeResets  int
}

// ResetBM25DegradeCounts clears the drop census, as the durable reset does.
func (s *fakeRebuildState) ResetBM25DegradeCounts(kgtypes.GraphType, string) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.degradeResets++
	s.degradeCensus = nil
	return nil
}

// BM25DegradeCounts returns a COPY, nil when empty — the seam's own contract.
func (s *fakeRebuildState) BM25DegradeCounts(kgtypes.GraphType, string) map[string]int {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if len(s.degradeCensus) == 0 {
		return nil
	}
	out := make(map[string]int, len(s.degradeCensus))
	for class, n := range s.degradeCensus {
		out[class] = n
	}
	return out
}

// applyFinalizeCensus models the finalize's builds populating the census. BOTH
// shipper fakes call it from their FinalizeRebuild, which is what lets a test
// script a STALE pre-existing census and prove the driver cleared it FIRST.
func (s *fakeRebuildState) applyFinalizeCensus() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if len(s.finalizeCensus) == 0 {
		return
	}
	if s.degradeCensus == nil {
		s.degradeCensus = make(map[string]int, len(s.finalizeCensus))
	}
	for class, n := range s.finalizeCensus {
		s.degradeCensus[class] += n
	}
}

// LoadMergeWatermark reports the delta-merge consumer's durable position.
func (s *fakeRebuildState) LoadMergeWatermark(kgtypes.GraphType, string) (int64, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.mergeWatermark, s.mergeWatermarkErr
}

func (s *fakeRebuildState) LoadRebuildState(kgtypes.GraphType, string) (int64, []searchengine.ExternalID, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.loadErr != nil {
		return 0, nil, s.loadErr
	}
	return s.watermark, append([]searchengine.ExternalID(nil), s.tombstoned...), nil
}

func (s *fakeRebuildState) SaveRebuildState(_ kgtypes.GraphType, _ string, watermarkNanos int64, tombstoned []searchengine.ExternalID) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saves++
	s.watermark = watermarkNanos
	s.tombstoned = append([]searchengine.ExternalID(nil), tombstoned...)
	return nil
}

func (s *fakeRebuildState) SetGraphTombstones(_ kgtypes.GraphType, _ string, ids []searchengine.ExternalID) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.seeded = append(s.seeded, append([]searchengine.ExternalID(nil), ids...))
}

func (s *fakeRebuildState) NoteDeletedIDs(_ kgtypes.GraphType, _ string, ids []searchengine.ExternalID) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.noted = append(s.noted, append([]searchengine.ExternalID(nil), ids...))
}

// ResetBM25Cursors records the BM25 arm's cursor reset. bm25ResetErr scripts the
// failure the driver must ABORT on rather than swap through.
func (s *fakeRebuildState) ResetBM25Cursors(kgtypes.GraphType, string) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.bm25ResetErr != nil {
		return s.bm25ResetErr
	}
	s.bm25Resets++
	return nil
}

// resetCount reports how many times the driver reset the BM25 feed cursor.
func (s *fakeRebuildState) resetCount() int {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.bm25Resets
}

// savedWatermark reads the persisted watermark under the fake's lock.
func (s *fakeRebuildState) savedWatermark() int64 {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.watermark
}

// saveCount reports how many times the driver persisted the record.
func (s *fakeRebuildState) saveCount() int {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.saves
}

// fakeRebuildShipper records every Stage/Finalize/Invalidate call. It is the T1 proof
// surface: it must record ZERO per-partition ships (staging never ships), exactly ONE
// FinalizeRebuild, and that finalize must happen AFTER every partition was staged.
type fakeRebuildShipper struct {
	mu sync.Mutex

	fakeRebuildState

	// stageCalls counts StageRebuildPartition, which carries BOTH formats' share of one
	// partition. It subsumes the four Add/Seal counters this fake used to keep: the
	// entry points collapsed into one precisely so a caller could not stage the vector
	// share and forget the field share, and counting them separately would advertise a
	// divergence that is no longer expressible.
	stageCalls    atomic.Int64
	finalizeCalls atomic.Int64
	invalidate    [][]searchengine.SegmentID

	hnswDocTotal int
	bm25DocTotal int

	// stagesAtFinalize records the staged-partition count observed at the moment the
	// finalize fires — proving it ran AFTER every partition was staged.
	stagesAtFinalize int64

	// bm25ResetsAtFinalize is the same device for the BM25 cursor reset: the reset
	// count observed AT the finalize. It is what distinguishes "reset before the
	// swap" from "reset after the swap" — a total count alone cannot, because both
	// orderings end the run at 1.
	bm25ResetsAtFinalize int

	// pruned / bm25Pruned are the retired sets the finalize reports PER FORMAT. They are
	// separate fields because the two corpora carry separate manifests and retire
	// independently: a live run read zero retired on the vector leg while all eight
	// field blobs retired, and a fake with one shared slice cannot model that at all.
	pruned     []searchengine.SegmentID
	bm25Pruned []searchengine.SegmentID
	stageErr   error // when set, StageRebuildPartition returns it (drives the abort path)
	// noSwap scripts the publish SKIP: FinalizeRebuild still returns a nil error
	// (a skip always does) but reports that no manifest swap landed. The zero value
	// is therefore the ordinary case — a rebuild whose publish went through.
	noSwap      bool
	finalizeErr error
	flushed     bool

	// Manifest read-back scripting. manifestConfigured gates the whole leg: a fake
	// that was never told what the server published cannot answer, so it reports an
	// error and the driver skips the cardinality check. That keeps every pre-existing
	// test — none of which model a published manifest — behaving exactly as before,
	// rather than having them all report a manifest of zero entries.
	manifestReads      atomic.Int64
	manifestFormats    []string
	manifestCount      int
	manifestConfigured bool
	manifestSeq        []int

	// Delta-path scripting. The ZERO VALUE is a landed, applicable delta at one
	// partition — the ordinary case — so a watermark-scoped run takes the delta path
	// and completes. Flipping deltaNotApplicable drives the from-scratch fallback,
	// deltaNoSwap drives the deferred-publish case (nil error, no swap), and
	// deltaBucketCount scripts the count the re-emit ran at, which is what the delta
	// cardinality read-back gates its equality assertion on.
	deltaCalls atomic.Int64

	deltaHNSWDocs       int
	deltaBM25Docs       int
	deltaBucketCount    int
	deltaNotApplicable  bool
	deltaNoSwap         bool
	deltaErr            error
	deltaAtManifestRead int64
}

// StageRebuildPartition records ONE partition of a reset, both formats together. It
// stages and never ships, so the finalize assertions stay the ship-once proof.
func (s *fakeRebuildShipper) StageRebuildPartition(
	_ context.Context, _ kgtypes.GraphType, _ string, hnswDocs, bm25Docs []searchengine.Document,
) error {
	if s.stageErr != nil {
		return s.stageErr
	}
	s.stageCalls.Add(1)
	s.mu.Lock()
	s.hnswDocTotal += len(hnswDocs)
	s.bm25DocTotal += len(bm25Docs)
	s.mu.Unlock()
	return nil
}

func (s *fakeRebuildShipper) FinalizeRebuild(
	_ context.Context, _ kgtypes.GraphType, _ string,
) (RebuildFinalizeResult, error) {
	s.finalizeCalls.Add(1)
	s.mu.Lock()
	s.flushed = true
	s.stagesAtFinalize = s.stageCalls.Load()
	s.bm25ResetsAtFinalize = s.resetCount()
	s.mu.Unlock()
	s.applyFinalizeCensus()
	if s.finalizeErr != nil {
		return RebuildFinalizeResult{}, s.finalizeErr
	}
	return RebuildFinalizeResult{
		HNSWSuperseded: s.pruned,
		BM25Superseded: s.bm25Pruned,
		Swapped:        !s.noSwap,
	}, nil
}

func (s *fakeRebuildShipper) InvalidateLocal(_ kgtypes.GraphType, _ string, ids []searchengine.SegmentID) {
	s.mu.Lock()
	s.invalidate = append(s.invalidate, ids)
	s.mu.Unlock()
}

// ResidentSegmentCount models the engine's post-swap sealed-segment count.
//
// IT HAS NO ERROR PATH, AND THAT IS THE CONTRACT CHANGE RATHER THAN A SIMPLIFICATION
// OF THE FAKE. Its predecessor modeled a manifest read back from the server, which
// could fail; the real reader is now one atomic snapshot load and a slice length, so
// a fake offering a failure mode would let tests exercise a branch production cannot
// reach. manifestErr is gone for that reason.
//
// manifestSeq scripts a DIFFERENT reading per call, which is what the delta
// cardinality check needs: its whole subject is the before/after comparison, and a
// fake answering one constant can only ever model "unchanged".
//
// AN UNSCRIPTED FAKE ANSWERS ZERO, deliberately. There is no longer an "unavailable"
// reading for the driver to skip on, so a test that does not script a count is
// modeling an engine that holds nothing — which the gate reads as a short set and
// WARNs about. A test that does not want that must script a count.
func (s *fakeRebuildShipper) ResidentSegmentCount(_ kgtypes.GraphType, _, format string) int {
	s.manifestReads.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifestFormats = append(s.manifestFormats, format)
	if len(s.manifestSeq) > 0 {
		i := int(s.manifestReads.Load()) - 1
		if i >= len(s.manifestSeq) {
			i = len(s.manifestSeq) - 1
		}
		return s.manifestSeq[i]
	}
	if !s.manifestConfigured {
		return 0
	}
	return s.manifestCount
}

// ReEmitRebuiltDelta records the documents the delta path handed over and reports the
// scripted result. deltaAtManifestRead captures how many manifest reads had happened
// when it fired, which is what proves the driver captured its cardinality BASELINE
// before the re-emit rather than after it.
func (s *fakeRebuildShipper) ReEmitRebuiltDelta(
	_ context.Context, _ kgtypes.GraphType, _ string, hnswDocs, bm25Docs []searchengine.Document,
) (RebuildDeltaResult, error) {
	s.deltaCalls.Add(1)
	s.mu.Lock()
	s.deltaHNSWDocs += len(hnswDocs)
	s.deltaBM25Docs += len(bm25Docs)
	s.deltaAtManifestRead = s.manifestReads.Load()
	count := s.deltaBucketCount
	if count == 0 {
		count = 1
	}
	res := RebuildDeltaResult{
		Swapped:            !s.deltaNoSwap,
		Applicable:         !s.deltaNotApplicable,
		DerivedBucketCount: count,
	}
	err := s.deltaErr
	s.mu.Unlock()
	if err != nil {
		return RebuildDeltaResult{}, err
	}
	return res, nil
}

// --- helpers -----------------------------------------------------------------

// makeScanPage builds a page of n items with ascending ids prefixed by `prefix`
// so the cursor ordering is deterministic. Each item carries a 32-byte vector and
// a one-field BM25 map.
func makeScanPage(prefix string, start, n int) []*knowledgev1.PipelineScanItem {
	page := make([]*knowledgev1.PipelineScanItem, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s%08d", prefix, start+i)
		vec := make([]byte, 32)
		vec[0] = byte(i)
		page = append(page, &knowledgev1.PipelineScanItem{
			NodeId:       id,
			GraphName:    "myrepo",
			BinaryVector: vec,
			Bm25Fields:   &knowledgev1.Bm25Fields{SymbolName: id},
		})
	}
	return page
}
