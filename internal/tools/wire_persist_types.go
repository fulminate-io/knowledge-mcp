// SPDX-License-Identifier: Apache-2.0

package tools

// wire_persist_types.go holds the create_batch wire-shape structs PersistBatch
// marshals. They are a sibling of wire_persist.go rather than part of it because
// that file sits against the repo's per-file length ceiling; the split is by
// bytes, not by concern, and the structs are meaningful only to PersistBatch.

// persistBatchNode mirrors server-side nodeCreateItem
// (tools_mutate_create_batch.go:52) plus the source carrier. The struct uses the
// wire tags the server expects. Source is mapped from store.Node.Source so a
// client-stamped provenance (e.g. buildFindingNode's 'llm:claude') survives onto
// the create_batch carrier and through to the engine NodeBody.source field — the
// Gap-2 fix (Source was previously lossy on the batch wire).
type persistBatchNode struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Summary     string            `json:"summary"`
	Content     string            `json:"content"`
	Status      string            `json:"status"`
	Metadata    map[string]string `json:"metadata"`
	// ID carries a caller-supplied node id onto the create_batch node body —
	// empty → store auto-gen, non-empty → honored verbatim by the engine
	// CREATE/UPSERT decode. omitempty is load-bearing rather than stylistic:
	// every existing caller passes an empty Id, and omitting the key keeps
	// their marshaled bytes identical to the pre-id shape.
	ID     string `json:"id,omitempty"`
	Source string `json:"source,omitempty"`
}

// persistBatchEdge mirrors server-side edgeCreateItem
// (tools_mutate_create_batch.go:69). The from_idx / to_idx fields are
// emitted explicitly so the server's UnmarshalJSON sentinel (-1 ==
// "use string ID") applies even when both endpoints reference an
// existing node by ID.
//
// It also carries the five edge-metadata fields
// (weight/confidence/method/evidence/last_validated) with omitempty so a
// Method/Weight/… set on a kgwire.BatchEdge survives the PersistBatch
// projection onto the engine create_batch edgeBody (the json keys match the
// engine's edgeBody decode tags so engine.Compile threads them onto the
// BatchEdgeSpec). last_validated is the RFC3339 STRING shape the edgeBody
// decodes — NOT the int64 unix-nanos proto carrier. omitempty makes an
// all-unset edge marshal byte-identically to the pre-metadata shape, so every
// existing PersistBatch caller is unaffected.
type persistBatchEdge struct {
	FromIdx       int     `json:"from_idx"`
	ToIdx         int     `json:"to_idx"`
	FromID        string  `json:"from_id,omitempty"`
	ToID          string  `json:"to_id,omitempty"`
	Type          string  `json:"type"`
	Weight        float64 `json:"weight,omitempty"`
	Confidence    float64 `json:"confidence,omitempty"`
	Method        string  `json:"method,omitempty"`
	Evidence      string  `json:"evidence,omitempty"`
	LastValidated string  `json:"last_validated,omitempty"`
}

// persistBatchArgs is the wire-shape envelope sent to mutate(create_batch).
type persistBatchArgs struct {
	Operation string             `json:"operation"`
	Nodes     []persistBatchNode `json:"nodes"`
	Edges     []persistBatchEdge `json:"edges"`
	BundleID  string             `json:"bundle_id,omitempty"`
}
