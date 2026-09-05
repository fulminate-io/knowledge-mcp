// SPDX-License-Identifier: Apache-2.0

package kgtypes

const (
	GraphKnowledge GraphType = "knowledge"
	GraphCode      GraphType = "code"
	GraphCloud     GraphType = "cloud"
	GraphPractice  GraphType = "practice"
	GraphLinkage   GraphType = "linkage"
	GraphCICD      GraphType = "cicd"
	// GraphLogs is an ephemeral per-query graph built from structured log
	// entries. A single query can produce millions of nodes (templates,
	// streams, chunks, labels). Log graphs are MUST NEVER be sent to the
	// summarizer or embedder — doing so would cost thousands of dollars
	// and take hours. SkipsLLMProcessing enforces this at the store layer.
	// Storage: ~/.knowledge/logs/
	GraphLogs GraphType = "logs"
	// GraphWebRaw is a per-source graph of typed raw-graph records emitted
	// by the web collector (page / section / paragraph / code_block /
	// list / list_item / table / link / image / blockquote nodes with
	// contains/references edges). The content is raw HTML-derived text
	// that a downstream stage-2 translator consumes to synthesize higher-
	// level knowledge nodes. The raw graph is ENROLLED EMBED-ONLY on the
	// server: never summarized, but its chunks carry vectors and BM25
	// documents so the raw content is hybrid-searchable, which is how a
	// translator finds what to extract. It still NEVER SYNCS — see
	// SyncEligible below, which is now an independent rule rather than the
	// complement of the server's SkipsLLMProcessing.
	// Storage: ~/.knowledge/web/<source>.bin
	// where <source> is a slug identifying the crawl (e.g. "hohpe-eip",
	// "go101-go-details-and-tips").
	GraphWebRaw GraphType = "web"
	// GraphPDFRaw is a per-source graph of typed raw-graph records emitted
	// by the PDF collector (document / section / paragraph / code_block /
	// list_item / table / block nodes with contains edges). The content is
	// raw text extracted from a PDF that a downstream stage-2 translator
	// consumes to synthesize higher-level knowledge nodes. Like GraphWebRaw
	// it is ENROLLED EMBED-ONLY on the server — never summarized, chunks
	// carry vectors and BM25 documents — and like GraphWebRaw it never syncs.
	// Storage: ~/.knowledge/pdf/<source>.bin where <source> is the sanitized
	// basename of the input file, so re-collecting the same document is
	// idempotent and two documents sharing a basename are separated by the
	// collect-time collision refusal rather than by the name.
	GraphPDFRaw GraphType = "pdf"
	// GraphChecks is the SINGLE graph of deterministic corpus checks and the
	// fixture example nodes that validate them, across every language. It is a
	// singleton like GraphLinkage — there is one of it, addressed with no
	// instance field. Storage: ~/.knowledge/checks/default.bin
	//
	// LANGUAGE IS A NODE LABEL, NOT A GRAPH SELECTOR. Every check already carries
	// `language` as one of the contract's mandated keys, so a scan for one
	// language narrows WITHIN this graph via a metadata predicate. A graph per
	// language would have duplicated that key as a second source for one fact.
	//
	// SEPARATE FROM GraphPractice BY DESIGN. Practice graphs hold prose guidance
	// and model entries — corpus an LLM reads. Checks are executable assertions
	// whose fixtures are DELIBERATELY-BAD CODE authored to make a check fire, so
	// co-locating them would put a snippet written specifically to be wrong into
	// the ranked corpus that answers questions about good practice. Keeping them
	// apart also makes the model/check boundary a graph boundary rather than a
	// metadata convention.
	//
	// CHECK NODES RANK; FIXTURE NODES DO NOT, and the split is enforced by node
	// type rather than by the graph's absence from any table. The server enrolls
	// this graph as Embeddable-but-not-Summarizable — a check's summary is
	// author-supplied, so the summarizer would only overwrite authored intent —
	// and its per-graph node-type allow-list admits check findings while REFUSING
	// the fixture example nodes. That allow-list is what now keeps a snippet
	// written specifically to be wrong out of ranked and semantic search.
	GraphChecks GraphType = "checks"
)

// allGraphTypes is the canonical ordered list of every GraphType. The first
// seven are sync-eligible; the trailing three (logs/web/pdf) are the raw/
// LLM-skipped graphs SyncEligible filters out. Ordering is load-bearing:
// SyncEligibleGraphTypes filters this slice in place, so the eligible-set
// order is {knowledge, code, cloud, cicd, practice, linkage, checks}.
//
// Position does NOT decide eligibility — SyncEligible is a complement
// predicate, so any type absent from its exclusion set is eligible wherever it
// sits in this slice. checks sits in the eligible run because it IS eligible:
// see SyncEligible's note and TestSyncEligible_ChecksAreEligibleDeliberately.
var allGraphTypes = []GraphType{
	GraphKnowledge,
	GraphCode,
	GraphCloud,
	GraphCICD,
	GraphPractice,
	GraphLinkage,
	GraphChecks,
	GraphLogs,
	GraphWebRaw,
	GraphPDFRaw,
}

// SyncEligible reports whether a graph of type gt may be pushed to Fulminate
// Cloud. Every type EXCEPT logs and the raw graphs (web, pdf) is sync-eligible —
// CEO-locked, "raw graphs and logs are the only ones we don't want to sync".
// The reason for those three is RESIDENCY, not processing: a raw graph is a
// temporary scratch corpus expected to be dropped once a golden graph is
// produced, so pushing it would ship bytes nobody will keep.
//
// IT IS NO LONGER THE COMPLEMENT OF store.SkipsLLMProcessing, and that is the
// resolution of a contradiction this doc used to carry against itself. It said
// "complement of SkipsLLMProcessing" here and "sync and LLM processing are
// independent, do not reason from one to the other" a few paragraphs below. The
// second statement was the correct one, and the raw-graph enrollment made the
// first one false in fact: web and pdf are now LLM-PROCESSED (embed-only) and
// still NEVER SYNC, a combination the complement form could not express. This is
// an independent rule that happens to have overlapped with another one for a
// while.
//
// It is still written as an EXCLUSION rather than a hardcoded inclusion list, so
// it stays correct when a new syncable type is added — only allGraphTypes needs
// the new constant.
//
// CHANGE-DETECTOR CONTRACT, now one-directional rather than a mirror: a new graph
// type whose bytes must not leave the machine is excluded HERE, on its own
// residency argument, whatever it does on the summarize and embed axes. The
// server's store.SkipsLLMProcessing carries a pointer to this function, but a
// change there no longer implies a change here. No cross-module compiler or test
// bridges the two.
//
// GraphChecks IS ELIGIBLE, AND ITS ABSENCE FROM THE EXCLUSION SET IS THE
// DECISION rather than an oversight. Checks are the compiled half of the
// practice corpus, derived from practice example nodes, and practice is already
// eligible — excluding the derived artifact while its source syncs would be
// anomalous and would protect nothing. Portability is also the point of the
// corpus: compiling prose into a deterministic check pays off only if the check
// travels, or every machine re-pays the compile.
//
// DO NOT "FIX" THIS BY REASONING FROM LLM PROCESSING, in either direction. What
// a graph does on the summarize and embed axes governs LLM spend and
// ranked-search hygiene; sync governs where the bytes live. They are independent,
// and the argument held equally when checks was not embedded at all and now that
// it is. GraphLinkage is the standing precedent for the combination: absent from
// the per-graph opt-in table, absent from store.SkipsLLMProcessing, and
// sync-eligible. TestSyncEligible_ChecksAreEligibleDeliberately pins this.
// The RAW GRAPHS are now the precedent for the opposite combination — enrolled
// on the embed axis and deliberately not sync-eligible — which is the concrete
// case that retired the complement claim at the top of this doc.
func SyncEligible(gt GraphType) bool {
	return gt != GraphLogs && gt != GraphWebRaw && gt != GraphPDFRaw
}

// SyncEligibleGraphTypes returns the ordered set of sync-eligible graph types:
// {knowledge, code, cloud, cicd, practice, linkage, checks}. The
// set is UNCHANGED by the raw-graph enrollment — segment residency and sync
// residency are separate questions now, and only the former moved. Filters the
// canonical allGraphTypes slice through SyncEligible so the set and the
// predicate can never drift.
func SyncEligibleGraphTypes() []GraphType {
	out := make([]GraphType, 0, len(allGraphTypes))
	for _, gt := range allGraphTypes {
		if SyncEligible(gt) {
			out = append(out, gt)
		}
	}
	return out
}

// HasRebuildableSegments reports whether a graph of type gt carries search
// segments that manage(rebuild_segments) can regenerate from embedded nodes. It is
// the client-side mirror of the server's store.GraphType.Embeddable()
// (cmd/knowledge-server/internal/store/node_type_eligibility_table.go) — the SAME
// gate the server's segment_rebuild scan uses (embedGapEligible → gt.Embeddable()).
// The embeddable builtins {knowledge, code, cloud, cicd, practice, checks, web,
// pdf} have rebuildable segments; the non-embeddable types {linkage, logs} do
// not.
//
// CHECKS IS ADMITTED AT THE GRAPH LEVEL AND NARROWED AT THE NODE LEVEL. The
// server embeds its check findings and refuses its fixture example nodes through
// a per-graph node-type allow-list, so the graph genuinely carries segments and
// belongs here; the fixture exclusion lives one level down and is not this
// predicate's business.
//
// THIS PREDICATE GOVERNS BM25 AS WELL AS VECTORS, which is wider than its name
// suggests: pipeline.bm25ArmEnabledFor (collector_bm25.go) gates the BM25
// collector arm on it, so a type excluded here is excluded from the BM25 index as
// well as from the vector one. An author excluding a new type is deciding both.
//
// IT NO LONGER DERIVES FROM SyncEligible, and the derivation was the accident
// rather than the design. Written as SyncEligible-minus-two, this predicate could
// not express "carries segments and never leaves the machine" — so a raw graph
// could not be given BM25 segments without also being made cloud-sync-eligible,
// and the two independent questions were welded together by an implementation
// convenience. It is now an INDEPENDENT EXCLUSION over the three types that have
// nothing for an embedding-gated rebuild scan to find: logs (never embedded) and
// linkage (proxy edges, no text). Every other builtin's answer is byte-identical
// to before; web and pdf flip to true, which is the point.
//
// DELIBERATE client-side DUPLICATE of the server predicate, for the same
// module-boundary reason as SyncEligible: the client cannot import the
// server-internal table. BI-DIRECTIONAL CONTRACT: a new builtin set
// Embeddable=false on the server MUST be excluded here too — and because the
// SyncEligible derivation is gone, that exclusion is now something an author has
// to write rather than something they inherit.
func HasRebuildableSegments(gt GraphType) bool {
	return gt != GraphLogs && gt != GraphLinkage
}

// BuiltinGraphTypeNames returns the canonical built-in graph-type names in
// allGraphTypes order, as a fresh slice the caller may not mutate into the
// package's own state.
//
// It exists so a REFUSAL can list the accepted vocabulary — the client-side
// graph-selector validation (tools/registered_graph_selector.go) names every
// built-in when it rejects an unknown selector, and the standing bad-input rule
// is that an error names the offending value AND the vocabulary that would have
// worked. Deriving that list from allGraphTypes rather than restating it at the
// error site is what keeps the message correct when a built-in is added: a
// second hand-written list is a second source of truth that rots silently,
// because a stale vocabulary in an error string fails no test.
func BuiltinGraphTypeNames() []string {
	out := make([]string, 0, len(allGraphTypes))
	for _, gt := range allGraphTypes {
		out = append(out, string(gt))
	}
	return out
}

// IsBuiltinGraphType reports whether name matches one of the canonical built-in
// GraphType constants (knowledge / code / cloud / cicd / practice / linkage /
// checks / logs / web / pdf). It is the registration-time collision
// predicate for user-registered graph types: a GraphTypeDef whose Name collides
// with a built-in is rejected so a registered type can never shadow a built-in.
// A predicate is exported alongside BuiltinGraphTypeNames (which projects the
// names) so allGraphTypes itself stays package-private and unaliasable.
func IsBuiltinGraphType(name string) bool {
	for _, gt := range allGraphTypes {
		if string(gt) == name {
			return true
		}
	}
	return false
}

// retiredGraphTypes maps a graph-type name that WAS a builtin in an earlier
// release onto the sentence explaining its removal.
//
// IT IS FOR TWO THINGS, AND BOTH MATTER.
//
// (1) A RETIRED NAME IS NOT SIMPLY UNKNOWN. An unknown name is a typo, and the
// honest answer to one is "no such graph type". A retired name was VALID in a
// release an operator may still be upgrading from, and its bytes may still be
// sitting in a directory on their disk — so the honest answer names the removal
// and says what to do instead.
//
// (2) IT KEEPS THE FREED NAME UNREGISTRABLE. IsBuiltinGraphType no longer claims
// "transformers", so without this map the name would fall through to the
// registered-custom path and a user could register a graph type that adopts the
// leftover directory — a removed family silently degrading into a custom graph,
// which compiles and passes every vocabulary test.
//
// The client's sentence names the SURVIVING PATH as well as the removal, because
// its refusals reach an LLM caller who needs to know what to do instead.
var retiredGraphTypes = map[string]string{
	"transformers": "the transformers graph family was removed: it held only recipe nodes, " +
		"and recipes are ephemeral inline bodies now — pass a body as collect's " +
		"`recipe_body` with extract=true; see help(\"recipes\")",
}

// RetiredGraphTypeReason returns the removal sentence for a graph-type name that
// was a builtin in an earlier release, and reports ok=false for every name that
// was never one — so a caller can tell a RETIRED name apart from a merely
// unknown one and answer each honestly.
func RetiredGraphTypeReason(name string) (string, bool) {
	reason, ok := retiredGraphTypes[name]
	return reason, ok
}

// collectorOwnedGraphTypes is the subset of allGraphTypes that a COLLECTOR
// fills — graphs whose contents are produced wholesale by a collect run and
// swept by that run's epoch, so any writer that ships a PARTIAL node set into
// one removes everything it did not emit.
//
// Deliberately a static list rather than a collector.Lookup call: the collector
// registry is populated by init() in each collector subpackage, so a
// registry-based predicate reads "not collector-owned" for every type in any
// package that does not import the collectors — a fence built on it would be a
// silent no-op exactly where it is tested.
//
// The complement (knowledge / practice / linkage / checks) is authored by
// mutate-style writers rather than a collect epoch.
var collectorOwnedGraphTypes = []GraphType{
	GraphCode,
	GraphCloud,
	GraphCICD,
	GraphLogs,
	GraphWebRaw,
	GraphPDFRaw,
}

// IsCollectorOwnedGraphType reports whether name matches a built-in graph type
// that a collector owns and epoch-sweeps (code / cloud / cicd / logs / web /
// pdf). It is the target-side guard for non-collector writers that ship a
// CollectResult through the shared Sink: recipe.RunRecipe refuses a recipe whose
// target_graph_type names one of these, because a recipe's partial emission set
// would become a full-replace collect that wipes the rest of a real collector
// graph.
//
// This is NARROWER than IsBuiltinGraphType on purpose — practice is a built-in
// AND the only shipped recipe target, so a builtin-wide refusal would reject
// every working recipe.
func IsCollectorOwnedGraphType(name string) bool {
	for _, gt := range collectorOwnedGraphTypes {
		if string(gt) == name {
			return true
		}
	}
	return false
}

// Status values for work nodes. Internal vocabulary, NOT a closed enum —
// see mutateRequestArgs.Status comment for the open-string contract.
const (
	StatusPending   = "pending"
	StatusActive    = "active"
	StatusCompleted = "completed"
	StatusSkipped   = "skipped"
)

// Status values for project nodes.
const (
	StatusArchived = "archived"
)

// Status values for ticket nodes.
const (
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusClosed     = "closed"
)

// Status values for thought nodes.
const (
	StatusHypothesized = "hypothesized"
	StatusValidated    = "validated"
)
