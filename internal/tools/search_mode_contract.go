// SPDX-License-Identifier: Apache-2.0

package tools

import "encoding/json"

// search_mode_contract.go is the SINGLE definition of what each declared search
// mode DOES on the client segment arms. Both segment arms (the knowledge/default
// arm and the registered custom-graph arm) and both claim predicates read their
// behavior from here, so the two tools cannot disagree about what a caller's
// mode means.
//
// The DECLARATION of this vocabulary lives in SearchToolDef (firstclass_schema.go)
// and helpSearchCode (help_content2.go). This file is the implementation of that
// declaration; when the two disagree, one of them is a bug rather than a
// preference.
//
// Every function here is pure — no config reads, no I/O, no globals. That is
// deliberate: the rerank decision in particular used to read the Voyage key
// inline, which left tests inheriting whatever the developer's environment held
// and made the "this mode issues no rerank call" guarantee unfalsifiable.

// normalizeSegmentSearchMode maps the caller's declared mode onto the execution
// vocabulary the arms run. Two inputs are DECLARED EQUIVALENCES rather than
// conveniences, which is why they are resolved in exactly one place:
//
//   - "" resolves to "hybrid" because hybrid IS the declared default; an absent
//     mode is a caller asking for the default arm, not for no arm.
//   - "temporal" resolves to "recent" because the tool schema publishes
//     recent/temporal as one recency boost. Honoring only one spelling would
//     make the declaration false for the other.
//
// Every other value passes through verbatim, including values this package does
// not recognize: an unknown mode keeps its existing served-as-hybrid behavior.
// Refusing unknown modes by name would be a new rejection surface and is not
// this file's decision to make.
func normalizeSegmentSearchMode(mode string) string {
	switch mode {
	case "":
		return "hybrid"
	case "temporal":
		return "recent"
	default:
		return mode
	}
}

// segmentSearchClaimMode is the claim predicate BOTH segment arms share: given
// the caller's raw mode and the two shape signals, it reports the execution mode
// and whether this arm claims the call at all.
//
// ORDERING SUBTLETY, and it is load-bearing: normalizeSegmentSearchMode turns ""
// into "hybrid", so the DEFAULT-mode shape must be recognized from the RAW mode
// BEFORE normalizing. Branch the other way round and an explicit mode:hybrid
// carrying an id-selector starts declining — landed claim behavior, asserted by
// the registered-graph parity fence, silently changed by an ordering slip.
//
// The default-mode shape is the only one that inspects the payload: an
// id-selector means a lookup rather than a search, so it stays excluded. An
// EXPLICIT mode claims unconditionally, which is what lets an id-plus-mode
// payload be refused by name upstream instead of being served as a search with
// the selector quietly dropped.
func segmentSearchClaimMode(mode string, hasText, hasIDSelector bool) (execMode string, claimed bool) {
	if mode == "" {
		if hasText && !hasIDSelector {
			return "hybrid", true
		}
		return "", false
	}
	switch norm := normalizeSegmentSearchMode(mode); norm {
	case "recent", "hybrid", "text":
		return norm, true
	default:
		// Deliberately NOT widened to "vector": this predicate governs the
		// QUERY-tool claim, whose claimed set is unchanged by this contract. The
		// search tool routes by graph rather than through here, so mode:vector
		// still reaches the segment arm on that path.
		return "", false
	}
}

// segmentSearchEngineArms resolves which retrieval arms actually run, by
// deciding what reaches the segment engine. The two suppressions are SYMMETRIC
// and each works by starving one arm of its input rather than by adding a
// branch inside the engine:
//
//   - "text" nils the vector. The HNSW arm is gated on len(queryVec) > 0
//     (segmentdist/manager_search.go:86), so an empty vector skips it entirely,
//     and reciprocal-rank fusion over the single remaining BM25 list is the
//     identity ranking.
//   - "vector" empties the engine text. bm25Segment.Search returns nil for a
//     zero-token query (searchengine/formats/bm25/segment.go:51), so the BM25
//     arm contributes nothing and the fusion reduces to the vector ranking.
//
// Those two citations ARE the mechanism — there is no third place where a mode
// is consulted during retrieval. Every other execution mode passes both inputs
// through unchanged.
func segmentSearchEngineArms(execMode, query string, vec []byte) (engineText string, engineVec []byte) {
	switch execMode {
	case "text":
		return query, nil
	case "vector":
		return "", vec
	default:
		return query, vec
	}
}

// segmentSearchModeLabel is the footer marker the segment arms disclose, derived
// from what actually reached the engine rather than from what the caller asked
// for. Three outcomes over two signals.
//
// WHY TWO ARGUMENTS. An earlier design took only hasVector, on the premise that
// these arms always run BM25 and merely ADD the vector arm — so a bare "vector"
// label could never be accurate. That premise stopped holding the moment
// segmentSearchEngineArms gained the ability to empty the engine text: the arm
// CAN now run vector-only, and a one-argument label would announce a BM25
// contribution that never happened. That is the same class of dishonest footer
// this contract exists to remove, so the label takes both signals.
//
// Two of the three strings are shared with the engine's own render-side label
// helper, whose axis is (embedded, rerankRan) — a different question about a
// different stage, which is why it is not called from here. The fused label is
// this layer's own.
func segmentSearchModeLabel(hasText, hasVector bool) string {
	switch {
	case hasText && hasVector:
		return "vector+text"
	case hasVector:
		return "vector"
	default:
		return "BM25-only"
	}
}

// searchRerankActive is the rerank decision as a pure predicate: rerank runs
// only when a Voyage key is configured, the resolved mode is not BM25-only, and
// the caller did not explicitly opt out.
//
// The key arrives as a PARAMETER on purpose. Read inline from config, it
// resolves through the credential fallback to the process environment, so a
// test inherits whatever the developer's machine holds and cannot choose the
// keyed or keyless state. Passing it in is what makes "this mode issues no
// rerank call" a claim a test can actually falsify.
//
// rerankParam is the tri-state the wire args already carry: nil means the caller
// said nothing and the key-driven default stands.
func searchRerankActive(hasVoyageKey bool, rerankParam *bool, bm25Only bool) bool {
	if !hasVoyageKey || bm25Only {
		return false
	}
	if rerankParam != nil && !*rerankParam {
		return false
	}
	return true
}

// searchRerankParam decodes the caller's `rerank` as the TRI-STATE it is: a
// present true, a present false, or absent. searchRerankActive needs the
// distinction, and so does the conflict check below — rerank:false alongside a
// BM25-only mode agrees with itself and must serve normally, while rerank:true
// contradicts it. A malformed payload reads as absent.
func searchRerankParam(raw json.RawMessage) *bool {
	var probe struct {
		Rerank *bool `json:"rerank"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil
	}
	return probe.Rerank
}

// searchModeConflict returns the refusal message for a BM25-only payload that
// simultaneously asks for a vector operation, or "" when there is no conflict.
//
// WHY REFUSE RATHER THAN LET ONE SIDE WIN. The caller has asked for two things
// that cannot both happen, and there is no honest channel to disclose a silent
// resolution: a format:json response is rendered by a path that emits no footer
// at all, so a quietly-resolved contradiction would leave no trace anywhere the
// caller can read. Naming both params is the in-tree idiom for a
// self-contradictory payload.
//
// This is caller-payload validation, which is a different thing from the
// "search never fails" contract — that governs internal degrade on rerank or
// hydrate faults, not a request that contradicts itself.
func searchModeConflict(raw json.RawMessage) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	if r := searchRerankParam(raw); r != nil && *r {
		return "search: mode:text selects BM25-only retrieval, but rerank:true asks for a " +
			"vector rerank — drop rerank:true, or use mode:hybrid to rerank"
	}
	if qv, present := obj["query_vector"]; present && len(qv) > 0 && string(qv) != "null" && string(qv) != `""` {
		return "search: mode:text selects BM25-only retrieval, but query_vector supplies a " +
			"vector to search with — drop query_vector, or use mode:hybrid or mode:vector"
	}
	return ""
}
