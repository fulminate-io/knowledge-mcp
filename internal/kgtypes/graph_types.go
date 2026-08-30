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
	// GraphTransformers is the first-class graph that stores Transformer
	// recipes — human/agent-authored executable bodies in the graph-agnostic
	// recipe DSL. Each node's Content carries the recipe source; metadata
	// carries source_graph_type, target_graph_type, and target_name. Names
	// are recipe identifiers (e.g. "eip-to-design-patterns"). Storage:
	// ~/.knowledge/transformers/<name>.bin.
	//
	// SkipsLLMProcessing(GraphTransformers) is false — recipe bodies are
	// authored content that benefits from BM25 indexing so operators can
	// discover recipes via query(graph:"transformers", text:"...").
	GraphTransformers GraphType = "transformers"
	// GraphWebRaw is a per-source graph of typed raw-graph records emitted
	// by the web collector (page / section / paragraph / code_block /
	// list / list_item / table / link / image / blockquote nodes with
	// contains/references edges). The content is raw HTML-derived text
	// that a downstream stage-2 translator consumes to synthesize higher-
	// level knowledge nodes — the raw graph itself must NEVER hit the
	// summarizer or embedder. SkipsLLMProcessing enforces this the same
	// way GraphLogs is enforced. Storage: ~/.knowledge/web/<source>.bin
	// where <source> is a slug identifying the crawl (e.g. "hohpe-eip",
	// "go101-go-details-and-tips").
	GraphWebRaw GraphType = "web"
	// GraphPDFRaw is a per-source graph of typed raw-graph records emitted
	// by the PDF collector (document / section / paragraph / code_block /
	// list_item / table / block nodes with contains edges). The content is
	// raw text extracted from a PDF that a downstream stage-2 translator
	// consumes to synthesize higher-level knowledge nodes — the raw graph
	// itself must NEVER hit the summarizer or embedder. SkipsLLMProcessing
	// enforces this the same way GraphWebRaw and GraphLogs are enforced.
	// Storage: ~/.knowledge/pdf/<source>.bin where <source> is a slug
	// derived from the input file basename + content hash so re-collecting
	// the same document is idempotent.
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
// eight are sync-eligible; the trailing three (logs/web/pdf) are the raw/
// LLM-skipped graphs SyncEligible filters out. Ordering is load-bearing:
// SyncEligibleGraphTypes filters this slice in place, so the eligible-set
// order is {knowledge, code, cloud, cicd, practice, linkage, transformers,
// checks}.
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
	GraphTransformers,
	GraphChecks,
	GraphLogs,
	GraphWebRaw,
	GraphPDFRaw,
}

// SyncEligible reports whether a graph of type gt may be pushed to Fulminate
// Cloud. It is the complement of the server's store.SkipsLLMProcessing
// (cmd/knowledge-server/internal/store/db_policy.go:21): every type EXCEPT the
// raw/LLM-skipped graphs (logs, web, pdf) is sync-eligible — CEO-locked, "raw
// graphs and logs are the only ones we don't want to sync".
//
// This is a DELIBERATE client-side DUPLICATE of the server predicate: the OSS
// client cannot import the server-internal predicate across the module
// boundary (and the cloud read happens via the login-routed GraphCaller RPC,
// not by importing cloud code). Written in complement form (not a hardcoded
// inclusion list) so it stays correct when a new non-raw type is added — only
// allGraphTypes needs the new constant.
//
// BI-DIRECTIONAL CHANGE-DETECTOR CONTRACT: a new raw/LLM-skipped/sync-ineligible
// graph type added to store.SkipsLLMProcessing MUST be reflected here too (add
// it to the exclusion set below AND append it to allGraphTypes after the
// eligible prefix). No cross-module compiler/test enforces this; the server
// site carries the reciprocal pointer back to this function.
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
func SyncEligible(gt GraphType) bool {
	return gt != GraphLogs && gt != GraphWebRaw && gt != GraphPDFRaw
}

// SyncEligibleGraphTypes returns the ordered set of sync-eligible graph types:
// {knowledge, code, cloud, cicd, practice, linkage, transformers}. Filters the
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
// The embeddable builtins {knowledge, code, cloud, cicd, practice, checks} have
// rebuildable segments; the non-embeddable types {linkage, transformers, logs,
// web, pdf} do not (transformers is Summarizable-but-not-Embeddable on the
// server, so the embedding-gated rebuild scan yields nothing for it).
//
// CHECKS IS ADMITTED AT THE GRAPH LEVEL AND NARROWED AT THE NODE LEVEL. The
// server embeds its check findings and refuses its fixture example nodes through
// a per-graph node-type allow-list, so the graph genuinely carries segments and
// belongs here; the fixture exclusion lives one level down and is not this
// predicate's business.
//
// TRANSFORMERS GETS NO SEGMENTS OF ANY KIND — not BM25 ones either, and this note
// exists because the sentence above used to say it was BM25-indexed. THIS function
// is what decides that: pipeline.bm25ArmEnabledFor (collector_bm25.go) gates the
// BM25 collector arm on HasRebuildableSegments, so excluding transformers here
// excludes it from the BM25 index as well as from the vector one. The false clause
// read as a license — "the index is already there, ranked search just needs
// wiring" — over a graph where a ranked query can only ever come back empty.
//
// DELIBERATE client-side DUPLICATE of the server predicate, for the same
// module-boundary reason as SyncEligible: the client cannot import the
// server-internal table. Written as the embeddable subset of SyncEligible (which
// already drops logs/web/pdf) minus the three sync-eligible-but-non-embeddable types,
// so a newly-added raw type stays excluded automatically. BI-DIRECTIONAL CONTRACT:
// a new builtin set Embeddable=false on the server MUST be excluded here too.
func HasRebuildableSegments(gt GraphType) bool {
	return SyncEligible(gt) && gt != GraphLinkage && gt != GraphTransformers
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
// transformers / checks / logs / web / pdf). It is the registration-time collision
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
// The complement (knowledge / practice / linkage / transformers) is authored by
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
