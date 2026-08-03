// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
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
	pageIter   int
}

func (f *fakeRebuildScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.cursors = append(f.cursors, req.GetAfterId())
	f.graphTypes = append(f.graphTypes, req.GetGraphType())
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
	manifestErr        error
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
	s.mu.Unlock()
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

// PublishedManifestCount models the manifest read-back. manifestCount defaults to
// -1 (not configured), which the driver treats as "unavailable" and skips the
// cardinality check for — so every pre-existing test that never configures it keeps
// its prior behavior. manifestErr models a failed read-back.
func (s *fakeRebuildShipper) PublishedManifestCount(_ context.Context, _ kgtypes.GraphType, _, format string) (int, error) {
	s.manifestReads.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifestFormats = append(s.manifestFormats, format)
	if s.manifestErr != nil {
		return 0, s.manifestErr
	}
	// manifestSeq scripts a DIFFERENT reading per call, which is what the delta
	// cardinality check needs: its whole subject is the before/after comparison, and a
	// fake answering one constant can only ever model "unchanged".
	if len(s.manifestSeq) > 0 {
		i := int(s.manifestReads.Load()) - 1
		if i >= len(s.manifestSeq) {
			i = len(s.manifestSeq) - 1
		}
		return s.manifestSeq[i], nil
	}
	if !s.manifestConfigured {
		return 0, errors.New("fake shipper: no published manifest scripted")
	}
	return s.manifestCount, nil
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
