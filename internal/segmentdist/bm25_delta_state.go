// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// bm25DeltaStateFile is the per-graph BM25 arm cursor record, written beside the
// rebuild-state, merge-horizon and repair records (same per-graph directory, same
// rebuildStateFormat root) but in its OWN file.
//
// A SEPARATE FILE FOR THE SAME REASON merge_state.go gives: the records have
// DIFFERENT WRITERS. The rebuild driver replaces {watermark, tombstones}
// wholesale, the delta-merge arm advances the merge horizon, and these cursors are
// advanced by the BM25 arm — so sharing one file would make each writer's
// wholesale replace clobber the others' fields.
const bm25DeltaStateFile = "bm25_delta.json"

// bm25DeltaStateRecord is the on-disk shape of the per-graph BM25 arm position.
type bm25DeltaStateRecord struct {
	// Cursors are the per-layer keyset positions the arm has drained to, stored as
	// the WIRE type rather than a local twin.
	//
	// THE WIRE TYPE IS THE RECORD, DELIBERATELY. A CorpusDelta position is a SET of
	// per-layer keyset cursors, not a scalar horizon like the merge record's — and
	// the generated knowledgev1.LayerCursor already spells exactly that. Declaring a
	// parallel local struct would be a second spelling of a generated type, which is
	// what the repo's shared-contract rule exists to prevent; this package already
	// imports gen/knowledge/v1 for the same reason elsewhere.
	Cursors []*knowledgev1.LayerCursor `json:"cursors"`
}

// bm25DeltaStatePathFor returns the record path for one graph. Rooted under the L2
// cache like its three siblings, and THE COUPLING IS LOAD-BEARING RATHER THAN
// TIDY: wiping the cache drops the cursors too, so a cache with no BM25 blobs left
// re-drains from zero. A cursor that survived a blob wipe would leave the corpus
// permanently short of every node behind it — silently, which is the failure class
// this whole ticket exists to remove.
func bm25DeltaStatePathFor(base string, gt kgtypes.GraphType, name string) string {
	return filepath.Join(graphCacheDirFor(base, gt, name, rebuildStateFormat), bm25DeltaStateFile)
}

// LoadBM25Cursors reads the graph's persisted BM25 arm cursors.
//
// A MISSING RECORD IS NOT AN ERROR — it returns nil cursors with a nil error,
// which the arm reads as "start from zero". That is the CORRECT default here and
// the opposite of the merge record's rule: LoadMergeWatermark's zero means "pull
// nothing until a horizon is seeded", because a zero-watermark scan of the
// vector-gated axis is the whole corpus. This feed is different — a zero cursor is
// an ordinary cold drain, it is bounded by the page limit, and refusing to drain
// would leave a graph unindexed forever.
//
// A Manager with no cache directory keeps no durable state at all and reports the
// same nil with a nil error.
func (m *Manager) LoadBM25Cursors(gt kgtypes.GraphType, name string) ([]*knowledgev1.LayerCursor, error) {
	if m.cacheDir == "" {
		return nil, nil
	}
	m.bm25DeltaStateMu.Lock()
	defer m.bm25DeltaStateMu.Unlock()

	raw, err := os.ReadFile(bm25DeltaStatePathFor(m.cacheDir, gt, name)) //nolint:gosec // path is derived from the sanitized per-graph cache root.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read bm25 delta state for %s/%s: %w", gt, name, err)
	}
	var rec bm25DeltaStateRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("decode bm25 delta state for %s/%s: %w", gt, name, err)
	}
	return rec.Cursors, nil
}

// SaveBM25Cursors persists the graph's BM25 arm cursors, replacing them wholesale.
// The write is atomic (temp file in the same directory, then rename) through the
// SHARED writer, so a crash mid-write leaves the previous cursors rather than a
// truncated record — which would decode as no record and cost one re-drain.
//
// THE CALLER ADVANCES ONLY AFTER A SUCCESSFUL SHIP. This function does not know
// that and cannot enforce it; the ordering is the arm's, and the reason it matters
// is that a cursor advanced past an unshipped page converts a ship failure into
// permanently unindexed nodes.
//
// A Manager with no cache directory has nowhere to put the record and silently
// keeps none.
func (m *Manager) SaveBM25Cursors(gt kgtypes.GraphType, name string, cursors []*knowledgev1.LayerCursor) error {
	if m.cacheDir == "" {
		return nil
	}
	raw, err := json.Marshal(bm25DeltaStateRecord{Cursors: cursors})
	if err != nil {
		return fmt.Errorf("encode bm25 delta state for %s/%s: %w", gt, name, err)
	}

	m.bm25DeltaStateMu.Lock()
	defer m.bm25DeltaStateMu.Unlock()
	return atomicWriteStateFile(bm25DeltaStatePathFor(m.cacheDir, gt, name), raw)
}

// ResetBM25Cursors returns the graph to the never-drained state by REMOVING the
// record.
//
// IT IS A FIRST-CLASS OPERATION, NOT A SAVE OF EMPTY, and the distinction is the
// point: a reader must not have to tell "reset" apart from "never written", and
// with the record removed both resolve to the same next behaviour — a full drain
// from zero. Saving an empty cursor list instead would leave a record on disk
// asserting a position, which is a third state nobody needs.
//
// A NOT-EXIST ERROR IS SUCCESS. An absent record is the intended end state, so
// removing a record that is already gone has done the job — the same disposition
// removePersistedCorpusRecord takes in the thought loop.
//
// Its caller is the rebuild path, which resets BEFORE swapping a rebuilt BM25
// layer in; see the Phase 5 step for why the trigger is staged BM25 work rather
// than the finalize's swapped flag.
func (m *Manager) ResetBM25Cursors(gt kgtypes.GraphType, name string) error {
	if m.cacheDir == "" {
		return nil
	}
	m.bm25DeltaStateMu.Lock()
	defer m.bm25DeltaStateMu.Unlock()

	if err := os.Remove(bm25DeltaStatePathFor(m.cacheDir, gt, name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reset bm25 delta state for %s/%s: %w", gt, name, err)
	}
	return nil
}
