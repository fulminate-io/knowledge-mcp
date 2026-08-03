// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// repairStateFile is the per-graph coverage-backstop record, written beside the
// rebuild-state, manifest-fingerprint and merge-horizon records (same per-graph
// directory, same rebuildStateFormat root) but in its OWN file — the same
// different-writers argument manifest_state.go makes.
const repairStateFile = "repair.json"

// RepairState is EXPORTED because the tools coverage reader reads it to decide
// whether a row's band has been verified. Residue is the calibrated structural gap
// the backstop settled at; Converged is the SNAPSHOT trust bit the backstop writes —
// it claims that everything embedded as of scan start was covered, not that the
// corpus has stopped moving, because a write landing after that instant belongs to
// the currency path rather than to the backstop; VerifiedAtNanos is when this record
// was last written, and it is DURABLE
// precisely so a restart does not reset the backstop clock and re-earn a scan the
// previous process already paid for.
//
// SCANNED IS THE FIELD THAT KEEPS THE COVERAGE COLUMN HONEST, and it exists
// because Converged alone cannot answer two different questions. The backstop
// writes a record on TWO paths: a completed pass that actually examined the corpus
// (the calibration), and the seed for a graph the arm DECLINED without looking at
// anything. Both are legitimately "converged" from the arm's point of view — it
// has nothing to do for either — which is exactly what the backstop gate wants to
// know. But the coverage column asks a different question: has the backstop
// VERIFIED this row's shortfall? For a seeded record the answer is no, and a graph
// that was seeded while converged and then grows new embedded nodes moves into the
// gap-repairing band while its seeded record is still fresh — so a column keyed on
// Converged would print "gap-repairing" for up to a full backstop interval about a
// row nothing had examined, during exactly the window the gate is skipping the
// graph. Scanned separates the two: the calibration sets it true, the seed leaves
// it false, and only the coverage column's verified formula reads it.
type RepairState struct {
	Residue         int   `json:"residue"`
	Converged       bool  `json:"converged"`
	Scanned         bool  `json:"scanned"`
	VerifiedAtNanos int64 `json:"verified_at_nanos"`
}

// repairStatePathFor returns the record path for one graph, rooted under the L2
// cache so wiping the cache drops the record — an absent record means "eligible
// for one backstop scan", which is exactly right for a cache that was just wiped.
func repairStatePathFor(base string, gt kgtypes.GraphType, name string) string {
	return filepath.Join(graphCacheDirFor(base, gt, name, rebuildStateFormat), repairStateFile)
}

// LoadRepairState reads the graph's persisted backstop record and fills the hot
// map from it.
//
// A MISSING RECORD IS NOT AN ERROR — it returns the zero record, which every
// caller reads as "no record": for the backstop gate that means eligible for one
// scan, and for the residue seeding it means nothing to seed from. Both
// degradations are safe and neither can lose data.
//
// A Manager with no cache directory keeps no durable state at all and reports the
// same zero record with a nil error.
func (m *Manager) LoadRepairState(gt kgtypes.GraphType, name string) (RepairState, error) {
	if m.cacheDir == "" {
		return RepairState{}, nil
	}
	m.repairStateMu.Lock()
	defer m.repairStateMu.Unlock()

	raw, err := os.ReadFile(repairStatePathFor(m.cacheDir, gt, name)) //nolint:gosec // path is derived from the sanitized per-graph cache root.
	if err != nil {
		if os.IsNotExist(err) {
			m.rememberRepairStateLocked(gt, name, RepairState{})
			return RepairState{}, nil
		}
		return RepairState{}, fmt.Errorf("read repair state for %s/%s: %w", gt, name, err)
	}
	var st RepairState
	if err := json.Unmarshal(raw, &st); err != nil {
		return RepairState{}, fmt.Errorf("decode repair state for %s/%s: %w", gt, name, err)
	}
	m.rememberRepairStateLocked(gt, name, st)
	return st, nil
}

// SaveRepairState persists what a pass settled at, replacing the record wholesale
// and refreshing the hot map. The write is atomic (temp file, then rename), so a
// crash mid-write leaves the previous record rather than a truncated one — which
// would decode as no record and cost one extra backstop pass.
//
// A Manager with no cache directory has nowhere to put the record and silently
// keeps none.
func (m *Manager) SaveRepairState(gt kgtypes.GraphType, name string, st RepairState) error {
	if m.cacheDir == "" {
		return nil
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode repair state for %s/%s: %w", gt, name, err)
	}

	m.repairStateMu.Lock()
	defer m.repairStateMu.Unlock()
	if err := atomicWriteManifestState(repairStatePathFor(m.cacheDir, gt, name), raw); err != nil {
		return err
	}
	m.rememberRepairStateLocked(gt, name, st)
	return nil
}

// RepairStateCached answers from the hot map ONLY and never touches disk, so the
// manage(status) coverage assembly — which walks every graph serially — pays a map
// read per row rather than a file read. ok is false when this process has not
// loaded or written that graph's record yet, which the coverage column renders as
// the honest "not verified this process" answer rather than as a degradation.
func (m *Manager) RepairStateCached(gt kgtypes.GraphType, name string) (RepairState, bool) {
	m.repairStateMu.Lock()
	defer m.repairStateMu.Unlock()
	st, ok := m.repairStateHot[graphKey{graphType: gt, graphName: name}]
	return st, ok
}

// rememberRepairStateLocked updates the hot map. Callers hold repairStateMu.
func (m *Manager) rememberRepairStateLocked(gt kgtypes.GraphType, name string, st RepairState) {
	if m.repairStateHot == nil {
		m.repairStateHot = make(map[graphKey]RepairState)
	}
	m.repairStateHot[graphKey{graphType: gt, graphName: name}] = st
}
