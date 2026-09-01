// SPDX-License-Identifier: Apache-2.0

// vectorbyid.go — the by-id STORED-VECTOR read. Relocated from engine.go, whose
// remaining contents are the segment lifecycle (add/seal/publish/delete/search) and
// the residency aggregates; this is the one read that resolves a single member's
// payload rather than ranking or counting, and it is the only engine surface two
// packages outside searchengine consume by name.
//
// It sits beside its own vectorbyid_test.go, and its liveness consult is the
// subject of that file's TestVectorByIDDeclinesADeletedMember.

package searchengine

// VectorByID resolves a LIVE member's stored vector by external id, or (nil,false)
// when no sealed segment holds it live. It mirrors Delete's route-map walk
// (set.route → owning entry) for an O(1) lookup + O(#segments) entryByID scan — no
// full-corpus walk — then reads the vector off the segment's concrete payload via a
// runtime type-assert to the by-id accessor. The inline-interface assert keeps the
// method generic-safe across [Q,S]: the HNSW instantiation's payload (*hnswSegment)
// satisfies it; a payload without the accessor (e.g. bm25) fails the assert and
// yields (nil,false) — never a panic, never a wrong-type read. Vectors only exist
// on sealed segments, so the un-sealed active buffer is intentionally not consulted.
//
// IT ANSWERS FOR THE LIVE CORPUS, NOT THE RAW PAYLOAD, and the residentMemberIn
// consult below is what makes that true. Route presence alone is NOT membership —
// a deleted id keeps its route and members entries and loses only its live bit — so
// reading the payload straight off the routed entry resolves a vector for a
// document that has left the corpus. The two states where that bites are the ones
// where the blob is never rewritten: a tombstone-seeded import (a blob shipped
// BEFORE the delete, masked at load rather than rebuilt) and a superseded-but-still-
// resident copy. The ordinary delete path re-emits the bucket, so there the id is
// simply no longer routed and every by-id read misses regardless.
//
// WHY THE GATE IS HERE AND NOT IN THE CALLERS. Liveness is this engine's state and
// nothing above it can cheaply re-derive it — Manager exposes no is-this-id-live
// seam, so a caller-side check would mean building and threading one out to
// re-answer what one map lookup answers here. Both consumers want the same thing:
// the mode:"similar" query-vector source must not anchor a neighbor search on a
// node the corpus no longer has, and the propagation loop only ever asks about ids
// drawn from a live-node browse, so for it the consult is a no-op.
func (e *SegmentedIndex[Q, S]) VectorByID(externalID ExternalID) ([]byte, bool) {
	set := e.set.Load()
	sid, routed := set.route[externalID]
	if !routed {
		return nil, false
	}
	// residentMemberIn (engine.go) is the package's ONE searchability predicate, and
	// it absorbs the nil-entry case, so entry is non-nil below by construction.
	entry := set.entryByID(sid)
	if !residentMemberIn(entry, externalID) {
		return nil, false
	}
	vb, ok := entry.payload.(interface {
		VectorByID(string) ([]byte, bool)
	})
	if !ok {
		return nil, false
	}

	// A CORRUPT SEGMENT MUST NOT KILL THE PROCESS HERE EITHER. hnsw's VectorByID
	// binary-searches the id directory and resolves each candidate's stored id,
	// so it reaches the same raising accessors a search does — and this entry
	// point is reached by mode:"similar" and by the propagation loop, neither of
	// which is a search and neither of which sits behind the query boundary.
	//
	// A CONTAINED CORRUPTION REPORTS NOT-FOUND, and the signature is why: there
	// is no error to return here, so the choices are a miss or a crash. It is not
	// a SILENT miss — containCorrupt hands the corruption to the owner, which
	// quarantines the file and accounts for the documents it takes away, so the
	// miss arrives with the loudest signal this engine has. Returning a wrong
	// vector was never among the options.
	var (
		vec   []byte
		found bool
	)
	if err := e.containCorrupt(entry.meta.ID, func() error {
		vec, found = vb.VectorByID(externalID)
		return nil
	}); err != nil {
		return nil, false
	}
	return vec, found
}
