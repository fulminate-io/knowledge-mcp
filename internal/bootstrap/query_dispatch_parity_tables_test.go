// SPDX-License-Identifier: Apache-2.0

package bootstrap

// query_dispatch_parity_tables_test.go holds the DECLARED dispositions the
// parity harness drives. Split from query_dispatch_parity_test.go (which owns
// the harness, the partition guard and the two drive tests) only to keep both
// files inside the repo's file-length cap.

// dispositionKind is how a published query MODE is expected to be answered.
type dispositionKind int

const (
	// dispositionClaimed — some named client intercept answers, possibly with its
	// own legible argument-validation error. An error is still a claim.
	dispositionClaimed dispositionKind = iota
	// dispositionEngineReducible — the chain declines and engine.Dispatch compiles it.
	dispositionEngineReducible
	// dispositionStructuredRejection — claimed and refused with a locked message.
	dispositionStructuredRejection
)

// shapeKind is how a SHAPE (a combination of selectors and filters) is expected
// to be routed. Distinct from dispositionKind: modes ask "who answers this
// mode", shapes ask "which arm out of the ordered chain claims this payload".
type shapeKind int

const (
	// shapeKnowledgeSearchArm — the client knowledge search arm answers.
	shapeKnowledgeSearchArm shapeKind = iota
	// shapeEngineRead — the chain declines and the engine serves the read.
	shapeEngineRead
	// shapeRefusedByPrecheck — the chain declines and the ENGINE PRECHECK refuses,
	// because an earlier arm would otherwise silently ignore the supplied filter.
	shapeRefusedByPrecheck
	// shapeRecallArm — the reflect/recall arm claims it ahead of the engine.
	shapeRecallArm
	// shapeRejectedByAccounting — the claiming arm's per-arm param accounting
	// REFUSES the payload, naming a param that arm does not route. These rows were
	// declared deliberate-ignores until the query accounting gate went live; the
	// gap each justification described is now closed, and marker carries the param
	// the refusal must name.
	shapeRejectedByAccounting
	// shapeNoReadShape — the payload names no read at all; the generic deny is correct.
	shapeNoReadShape
)

// modeDisposition declares how one published mode must be answered. marker, when
// non-empty, is a substring the rendered body must contain — it is what makes a
// cell prove WHICH arm answered rather than merely that something did.
type modeDisposition struct {
	mode   string
	args   map[string]any
	kind   dispositionKind
	marker string
}

// shapeDisposition declares how one shape must be routed. justification is
// REQUIRED for a declared deliberate-ignore and is a data field rather than a
// nearby comment so the partition guard can assert it is non-empty — the same
// reason the write-side registry stores its ignore justifications as data.
type shapeDisposition struct {
	name          string
	args          map[string]any
	kind          shapeKind
	marker        string
	deliberate    bool
	justification string
}

// queryModeDispositions declares one entry per value in the LIVE published mode
// enum. The enum itself is read from the schema by the partition guard, never
// restated here, so a mode added to the schema lands in no entry and fails.
//
// The markers for the self-validating arms are the messages those arms actually
// return on the knowledge graph — an argument-validation error is a CLAIM, and
// recording the real text keeps the table honest about what was observed.
var queryModeDispositions = []modeDisposition{
	{mode: "hybrid", args: map[string]any{"graph": "knowledge", "mode": "hybrid", "text": "auth"},
		kind: dispositionClaimed, marker: knowledgeSearchArmMarker},
	{mode: "text", args: map[string]any{"graph": "knowledge", "mode": "text", "text": "auth"},
		kind: dispositionClaimed, marker: knowledgeSearchArmMarker},
	{mode: "recent", args: map[string]any{"graph": "knowledge", "mode": "recent", "text": "auth"},
		kind: dispositionClaimed, marker: knowledgeSearchArmMarker},
	{mode: "stats", args: map[string]any{"graph": "knowledge", "mode": "stats"}, kind: dispositionClaimed},
	{mode: "examine", args: map[string]any{"graph": "knowledge", "mode": "examine", "id": "n1"}, kind: dispositionClaimed},
	{mode: "file_symbols", args: map[string]any{"graph": "knowledge", "mode": "file_symbols"},
		kind: dispositionClaimed, marker: "file_path"},
	{mode: "modules", args: map[string]any{"graph": "knowledge", "mode": "modules"}, kind: dispositionEngineReducible},
	{mode: "personality", args: map[string]any{"graph": "knowledge", "mode": "personality"}, kind: dispositionClaimed},
	{mode: "influence", args: map[string]any{"graph": "knowledge", "mode": "influence"}, kind: dispositionClaimed},
	{mode: "tensions", args: map[string]any{"graph": "knowledge", "mode": "tensions"}, kind: dispositionClaimed},
	{mode: "blind_spots", args: map[string]any{"graph": "knowledge", "mode": "blind_spots"}, kind: dispositionClaimed},
	{mode: "evolution", args: map[string]any{"graph": "knowledge", "mode": "evolution"}, kind: dispositionClaimed},
	{mode: "summary", args: map[string]any{"graph": "knowledge", "mode": "summary"}, kind: dispositionClaimed},
	{mode: "simulate", args: map[string]any{"graph": "knowledge", "mode": "simulate"}, kind: dispositionClaimed},
	{mode: "timeline", args: map[string]any{"graph": "knowledge", "mode": "timeline"},
		kind: dispositionClaimed, marker: "requires time_field"},
	{mode: "charges", args: map[string]any{"graph": "knowledge", "mode": "charges"}, kind: dispositionClaimed},
	{mode: "clusters", args: map[string]any{"graph": "knowledge", "mode": "clusters"}, kind: dispositionClaimed},
	// With graph supplied the topology arm validates the NEXT required argument,
	// so this marker is arm-identifying rather than merely "something claimed it".
	{mode: "topology", args: map[string]any{"graph": "knowledge", "mode": "topology"},
		kind: dispositionClaimed, marker: `requires "algorithm"`},
	{mode: "pivot", args: map[string]any{"graph": "knowledge", "mode": "pivot"},
		kind: dispositionClaimed, marker: "requires rows and cols"},
	{mode: "correlations", args: map[string]any{"graph": "knowledge", "mode": "correlations"}, kind: dispositionClaimed},
	{mode: "explain", args: map[string]any{"graph": "knowledge", "mode": "explain"},
		kind: dispositionClaimed, marker: "requires id="},
	{mode: "resolver", args: map[string]any{"graph": "knowledge", "mode": "resolver"},
		kind: dispositionStructuredRejection, marker: `requires graph="logs"`},
	{mode: "lineage", args: map[string]any{"graph": "knowledge", "mode": "lineage", "id": "n1"}, kind: dispositionClaimed},
	{mode: "evidence", args: map[string]any{"graph": "knowledge", "mode": "evidence", "id": "n1"}, kind: dispositionClaimed},
	{mode: "plan_tree", args: map[string]any{"graph": "knowledge", "mode": "plan_tree", "id": "n1"}, kind: dispositionClaimed},
	{mode: "metadata_stats", args: map[string]any{"graph": "knowledge", "mode": "metadata_stats"}, kind: dispositionClaimed},
}

// queryShapeDispositions declares the documented shape combinations on the
// knowledge graph — the FULL cross of the filter params against the earlier-arm
// selectors, because that cross is where the read-side silent drop lives.
var queryShapeDispositions = []shapeDisposition{
	// The client knowledge search arm: text, with every filter spelling.
	{name: "text", args: map[string]any{"graph": "knowledge", "text": "auth"}, kind: shapeKnowledgeSearchArm},
	{name: "type_text", args: map[string]any{"graph": "knowledge", "text": "auth", "type": "finding"},
		kind: shapeKnowledgeSearchArm},
	{name: "types_text", args: map[string]any{"graph": "knowledge", "text": "auth", "types": []string{"finding"}},
		kind: shapeKnowledgeSearchArm},
	{name: "meta_text", args: map[string]any{"graph": "knowledge", "text": "auth", "meta": map[string]any{"k": "v"}},
		kind: shapeKnowledgeSearchArm},
	{name: "type_meta_text", args: map[string]any{
		"graph": "knowledge", "text": "auth", "type": "finding", "meta": map[string]any{"k": "v"},
	}, kind: shapeKnowledgeSearchArm},
	{name: "status_text", args: map[string]any{"graph": "knowledge", "text": "auth", "status": "open"},
		kind: shapeRejectedByAccounting, marker: "status",
		justification: "WAS a declared deliberate-ignore: status is claimed by the knowledge search arm — " +
			"it appears in neither the claim gate nor the thought-filter gate — and neither the " +
			"query-to-search arg mapping nor the compose step ever applied it, while the BROWSE path does " +
			"honor it. The per-arm accounting gate now CLOSES that gap: status is classified rejected on " +
			"the knowledge search arm, so the caller is told rather than silently served an unfiltered " +
			"search. Applying it instead would still need a third post-filter and a status carrier."},

	// Hybrid tracks text exactly — that is what the hybrid claim buys.
	{name: "hybrid_text", args: map[string]any{"graph": "knowledge", "mode": "hybrid", "text": "auth"},
		kind: shapeKnowledgeSearchArm},
	{name: "hybrid_type_text", args: map[string]any{
		"graph": "knowledge", "mode": "hybrid", "text": "auth", "type": "finding",
	}, kind: shapeKnowledgeSearchArm},
	{name: "hybrid_id_text", args: map[string]any{
		"graph": "knowledge", "mode": "hybrid", "id": "n1", "text": "auth",
	}, kind: shapeRejectedByAccounting, marker: "id",
		justification: "WAS a declared deliberate-ignore: an explicit search mode claims regardless of id, " +
			"so id was not applied as a lookup and simply vanished. The accounting gate now classifies id " +
			"rejected on the knowledge search arm, which brings this shape into line with default-mode " +
			"id_plus_text (refused by the engine precheck) rather than leaving one ambiguity refused and " +
			"the other silently resolved. hybrid and mode:text still behave identically — both reject — so " +
			"the divergence the search-mode plan removed does not come back."},

	// Filter-only browses: no text, so the chain declines and the engine reads.
	{name: "id", args: map[string]any{"graph": "knowledge", "id": "n1"}, kind: shapeEngineRead},
	{name: "ids", args: map[string]any{"graph": "knowledge", "ids": []string{"n1"}}, kind: shapeEngineRead},
	{name: "type", args: map[string]any{"graph": "knowledge", "type": "finding"}, kind: shapeEngineRead},
	{name: "types", args: map[string]any{"graph": "knowledge", "types": []string{"finding"}}, kind: shapeEngineRead},
	{name: "meta", args: map[string]any{"graph": "knowledge", "meta": map[string]any{"k": "v"}}, kind: shapeEngineRead},

	// A filter alongside an id-selector: the engine precheck refuses pre-Compile.
	{name: "id_plus_type", args: map[string]any{"graph": "knowledge", "id": "n1", "type": "finding"},
		kind: shapeRefusedByPrecheck, marker: refusedByIDSelectorMarker},
	{name: "id_plus_text", args: map[string]any{"graph": "knowledge", "id": "n1", "text": "auth"},
		kind: shapeRefusedByPrecheck, marker: refusedByIDSelectorMarker},
	{name: "ids_plus_types", args: map[string]any{
		"graph": "knowledge", "ids": []string{"n1"}, "types": []string{"finding"},
	}, kind: shapeRefusedByPrecheck, marker: refusedByIDSelectorMarker},
	{name: "id_plus_meta", args: map[string]any{
		"graph": "knowledge", "id": "n1", "meta": map[string]any{"k": "v"},
	}, kind: shapeRefusedByPrecheck, marker: refusedByIDSelectorMarker},

	// The row that carries the status exclusion's other half: the recall arm
	// claims a status-bearing knowledge query BEFORE the engine ever sees it,
	// which is why status is absent from the by-id refusal set. An engine-level
	// test cannot observe this — it has no chain.
	//
	// It is declared as an ACCOUNTING REJECTION rather than a bare recall claim
	// because the recall arm's own accounting refuses it: recallParamsFromQuery
	// forwards status but has no carrier for id, so an id-bearing recall used to
	// drop the id silently. The row still proves the recall arm claimed ahead of
	// the engine — the refusal text names that arm's handler — while now also
	// pinning WHICH param it does not route, which the weaker recall assertion
	// could not distinguish from any other error.
	{name: "id_status", args: map[string]any{"graph": "knowledge", "id": "n1", "status": "open"},
		kind: shapeRejectedByAccounting, marker: "id",
		justification: "WAS observable only as arm selection: the recall arm claimed the payload and " +
			"dropped id with no signal, because queryReflectArgs carries id only for the thought-examine " +
			"route and recallParamsFromQuery never forwards it."},

	// The KNOWN POSITIVE for the three accounting-rejection rows above, and the
	// row that keeps shapeRecallArm exercised now that id_status has moved. A
	// session filter is a thought-graph property the recall arm genuinely routes,
	// so this payload must be CLAIMED AND SERVED. Without it, "the recall arm
	// refuses id" would be indistinguishable from a gate that refuses everything
	// that reaches that arm.
	{name: "session_recall", args: map[string]any{"graph": "knowledge", "session": "s1"},
		kind: shapeRecallArm},

	// The single declared generic-deny cell: a payload naming no read at all.
	// Declared rather than omitted so the invariant's scope is visible.
	{name: "no_read_shape", args: map[string]any{"graph": "knowledge"}, kind: shapeNoReadShape},
}
