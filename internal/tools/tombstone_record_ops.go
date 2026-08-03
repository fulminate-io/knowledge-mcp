// SPDX-License-Identifier: Apache-2.0

// tombstone_record_ops.go — the read-modify-write operations over the persisted
// per-graph tombstone record that are NOT the rebuild driver's. The driver's own merge
// stays where it is because it carries a different contract: it advances the watermark
// on a landed publish and trims via retainTombstones. These operate on the record
// alone, in both directions — ids going in as deletes are learned, and ids coming out
// as writes re-create them.

package tools

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// mergeTombstonesIntoRecord folds ids into the persisted per-graph tombstone record
// and hands the MERGED set to the engines, reporting the ids this call saw for the
// FIRST time and the merged set itself.
//
// IT MERGES, IT NEVER REPLACES. SetGraphTombstones REPLACES the graph's set and an
// EMPTY set DELETES the entry outright, so handing over a caller's window-scoped slice
// would ERASE the accumulated set and re-open the import-resurrection window the
// seeding exists to close.
//
// IT NEVER ADVANCES THE PERSISTED WATERMARK — that value is the rebuild's durability
// contract and may move only when a publish LANDED.
//
// TWO CALLERS, ONE DEFINITION, arriving from opposite directions. The delta consumer
// learns SOFT deletes from a scan that keeps reporting them until the record catches
// up. The prune handler learns HARD deletes that NO scan can ever report again — the
// rows are gone server-side — so for that caller this is the ONLY route into the
// record, and skipping it makes a crash before the re-emit ships resurrect the pruned
// documents permanently. The rebuild driver's own merge is deliberately NOT folded in:
// it advances the watermark on a landed publish and trims via retainTombstones, a
// different contract this helper must never acquire.
//
// IT DOES NOT STAMP. NoteDeletedIDs is driven by the CALLER, because only the caller
// knows which ids the current window actually reported; this helper only ever sees the
// merged union, and stamping that would re-date deletes whose ids are old and suppress
// writes that legitimately followed them.
func mergeTombstonesIntoRecord(
	shipper SegmentShipper, gt kgtypes.GraphType, name string, ids []string,
) (fresh, carried []searchengine.ExternalID, err error) {
	watermark, retained, lerr := shipper.LoadRebuildState(gt, name)
	if lerr != nil {
		// Merging into a set we could not read would DROP the ids it held, which is the
		// exact erasure this exists to avoid, so the pass declines rather than guessing.
		return nil, nil, fmt.Errorf("tombstone delta: rebuild state unreadable, declining to merge into an unknown set: %w", lerr)
	}
	known := make(map[searchengine.ExternalID]struct{}, len(retained))
	for _, id := range retained {
		known[id] = struct{}{}
	}
	for _, id := range ids {
		if _, seen := known[id]; !seen {
			fresh = append(fresh, id)
		}
	}

	carried = unionTombstones(retained, ids)
	// Persist FIRST, with the watermark untouched. A crash after this point leaves a
	// record that knows about the delete, which is the safe direction; a crash before
	// it re-reads the same window next pass.
	if serr := shipper.SaveRebuildState(gt, name, watermark, carried); serr != nil {
		return nil, nil, fmt.Errorf("tombstone delta: could not persist the merged set: %w", serr)
	}
	// Hand the engines the MERGED set — never the caller's window, never `fresh`.
	shipper.SetGraphTombstones(gt, name, carried)
	return fresh, carried, nil
}

// UntombstoneWrittenIDs clears the record's tombstone for ids a WRITE has re-created,
// then re-seeds the engines from what remains. It reports how many ids actually left
// the record.
//
// THE RE-SEED IS UNCONDITIONAL FOR ANY NON-EMPTY CALL, and that is the fix rather than
// a belt-and-braces flourish. The caller's reporter reads the ENGINE's set while this
// writes the RECORD, and the two reachably diverge with ENGINE strictly larger: the
// rebuild driver seeds the engines with the full carried union, while the finalize
// persists only the retained subset and never re-seeds. So a re-created id can be
// absent from the record and still present in the engines, producing no record
// intersection at all — and returning early there would leave the drain's filter still
// dropping the fresh document, which is the whole defect this exists to close.
// Re-seeding from the surviving set converges the engines DOWN to the record, which is
// safe by the record's own membership rule: an id may leave once its partition was
// re-emitted without it.
//
// THE WATERMARK IS NEVER MOVED — that value is the rebuild's durability contract and
// may advance only when a publish landed. IT PERSISTS BEFORE IT SEEDS, so a crash
// between the two leaves a record that already knows the id is live again.
func UntombstoneWrittenIDs(
	shipper SegmentShipper, gt kgtypes.GraphType, name string, ids []searchengine.ExternalID,
) (cleared int, err error) {
	if len(ids) == 0 {
		return 0, nil
	}
	watermark, retained, lerr := shipper.LoadRebuildState(gt, name)
	if lerr != nil {
		return 0, fmt.Errorf("untombstone: rebuild state unreadable, declining to rewrite a set we could not read: %w", lerr)
	}
	kept := dropTombstones(retained, ids)
	if len(kept) != len(retained) {
		if serr := shipper.SaveRebuildState(gt, name, watermark, kept); serr != nil {
			return 0, fmt.Errorf("untombstone: could not persist the cleared set: %w", serr)
		}
	}
	// An empty set DELETES the graph's entry, which is right: nothing is left to seed
	// dead or to time.
	shipper.SetGraphTombstones(gt, name, kept)
	return len(retained) - len(kept), nil
}
