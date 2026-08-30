// SPDX-License-Identifier: Apache-2.0

package graphclient

// operation_vocab.go is the CLOSED client-side query-origin vocabulary.
//
// This file is the only thing bounding the cardinality of the operation metrics
// dimension. That is a deliberate split, not an oversight: the server validates
// a term's SHAPE (a grammar + length cap) but keeps no closed list, because
// required-ness exists to force clients to upgrade, not to police vocabulary —
// and a server-side list would couple every new client operation to a server
// release. So the bound lives here, enforced two ways: Operation is a DEFINED
// TYPE (a bare string literal at a call site is a compile error), and
// TestOperationVocabulary asserts every declared term is grammar-valid, unique,
// and enumerated in AllOperations.
//
// ADDING A TERM: declare a constant here, add it to AllOperations, and keep it
// specific enough to be actionable but general enough to stay bounded. Never
// interpolate user input, node ids, paths, or counts into a term.

// Tool-dispatch operations: one per advertised MCP tool. The catalog is a closed
// 22-entry set (tools.AllToolSchemas), so mapping tool name → operation keeps the
// dimension bounded by construction; OperationForTool does that lookup and
// TestOperationVocabulary pins that every catalog entry resolves.
const (
	OpQuery          Operation = "query"
	OpTraverse       Operation = "traverse"
	OpMutate         Operation = "mutate"
	OpDelete         Operation = "delete"
	OpManage         Operation = "manage"
	OpSync           Operation = "sync"
	OpThoughts       Operation = "thoughts"
	OpSearch         Operation = "search"
	OpFileSymbols    Operation = "file_symbols"
	OpCollect        Operation = "collect"
	OpCustomComputer Operation = "custom_collector"
	OpAst            Operation = "ast"
	OpHelp           Operation = "help"
	OpRecordDecision Operation = "record_decision"
	OpAnalyzeUsage   Operation = "analyze_usage"
	OpManageChecks   Operation = "manage_checks"
	OpCreatePlan     Operation = "create_plan"
	OpCreateTicket   Operation = "create_ticket"
	OpCreateProject  Operation = "create_project"
	OpCreateResearch Operation = "create_research"
	OpCreateTestPlan Operation = "create_test_plan"
	OpAssemble       Operation = "assemble"
)

// Refinements of a tool operation, stamped by a specific sub-path that is worth
// telling apart from its parent in the metrics. These exist because the whole
// point of the dimension is to stop reverse-engineering which code path burned
// the CPU: a suffix fallback that scans differently from the primary lookup is
// exactly the case that previously took a deep-dive to identify.
const (
	OpFileSymbolsSuffixFallback Operation = "file_symbols.suffix_fallback"
	OpAstHydrate                Operation = "ast.hydrate"
	OpTraverseGraphWide         Operation = "traverse.graph_wide"
)

// Collector ingest phases. These ride the IngestService RPCs rather than a tool
// dispatch, so they are stamped at the upload sink rather than by tool name.
const (
	OpCollectChunk         Operation = "collect.chunk"
	OpCollectFinalize      Operation = "collect.finalize"
	OpCollectFetchSubgraph Operation = "collect.fetch_subgraph"
)

// Background-loop operations. These have no originating tool call: they are
// client-side daemons draining work on a timer. They are stamped so their load
// is attributable rather than collapsing into an anonymous bucket.
const (
	OpPipelineGapScan Operation = "pipeline.gap_scan"
	// OpPipelineGraphDiscovery is the graph-CATALOG poll (which graphs exist),
	// kept distinct from OpPipelineGapScan (per-graph work discovery) because the
	// two are different load shapes: discovery fires one graph-names read per
	// eligible graph type every tick regardless of whether any work is pending,
	// and that fan-out is what the metrics need to tell apart from real drain.
	OpPipelineGraphDiscovery Operation = "pipeline.graph_discovery"
	OpPipelineGenPoll        Operation = "pipeline.gen_poll"
	OpPipelineEmbedWriteback Operation = "pipeline.embed_writeback"
	OpCorpusDeltaDrain       Operation = "corpus_delta.drain"
	OpRebuildSegments        Operation = "rebuild_segments"
	// OpSegmentDeltaMerge is the reconcile pass's per-graph BOUNDED delta read, which
	// carries BOTH halves of the window: the deletes it lands on the local pool and
	// the live items it merges into the local segments. It is kept distinct from
	// OpRebuildSegments for the same reason OpPipelineGraphDiscovery is kept distinct
	// from OpPipelineGapScan: the two are different load shapes over the SAME scan
	// axis. A rebuild pages the whole vectored corpus once, on demand; this fires per
	// segment-bearing graph on every reconcile tick and is bounded by the graph's
	// merge horizon. Folding them together would hide a delta read that had
	// degenerated into a full scan inside the rebuild bucket, which is exactly the
	// regression the bound exists to prevent.
	OpSegmentDeltaMerge Operation = "segment.delta_merge"
	OpSegmentHeal       Operation = "segment.heal"
	OpSegmentReconcile  Operation = "segment.reconcile"
	// OpSegmentRepair is the coverage-repair arm's full-corpus scan. It pages the
	// same segment_rebuild axis as OpRebuildSegments but with a different load
	// shape — it ships only the gap, never a whole rebuild — so folding it into
	// that term would hide a repair that had degenerated into rebuild-scale
	// traffic inside the rebuild's own numbers.
	OpSegmentRepair Operation = "segment.repair"
	// OpSegmentHorizonSeed is the declined-graph seed's single-row horizon read. It
	// pages the same segment_rebuild axis as OpSegmentRepair but reads ONE row rather
	// than the corpus, and mixing it into the repair term would pollute exactly the
	// bucket the backstop's load is measured in.
	OpSegmentHorizonSeed   Operation = "segment.horizon_seed"
	OpInstructionBootstrap Operation = "instruction.bootstrap"
	OpPropagationReflect   Operation = "propagation.reflect"
	// OpCrossGraphProbe attributes the cross-graph composer's foreign-node scan:
	// the by-id probe issued against each loaded foreign graph in turn, plus the
	// graph-name enumeration that feeds it. It fires while handling some other
	// tool call, so without its own term its fan-out would be attributed to
	// whichever user operation happened to be on the stack.
	OpCrossGraphProbe Operation = "crossgraph.probe"
	// OpPostCollectFanout attributes the post-collect tails — the postpopulate
	// hook and the linker run — whose all-graphs walk addresses every graph of a
	// family rather than the one graph that was collected. Kept distinct from
	// OpCrossGraphProbe for the same reason OpSegmentDeltaMerge is kept distinct
	// from OpRebuildSegments: a probe scan and a post-collect enrichment are
	// different load shapes against different graphs, and folding them would hide
	// one inside the other's numbers.
	OpPostCollectFanout Operation = "collect.postpopulate"
)

// AllOperations enumerates every declared term. TestOperationVocabulary walks
// it, so a constant declared above but omitted here is caught by the test rather
// than shipping unvalidated — the enumeration IS the closure mechanism.
//
// OpUnstamped (declared with the interceptor, since it is that code's
// default-deny fallback) is included: it is a real term that reaches the wire
// and must satisfy the same grammar as any other.
var AllOperations = []Operation{
	OpQuery, OpTraverse, OpMutate, OpDelete, OpManage, OpSync, OpThoughts,
	OpSearch, OpFileSymbols, OpCollect, OpCustomComputer, OpAst,
	OpHelp, OpRecordDecision, OpAnalyzeUsage, OpManageChecks, OpCreatePlan, OpCreateTicket,
	OpCreateProject, OpCreateResearch, OpCreateTestPlan, OpAssemble,

	OpFileSymbolsSuffixFallback, OpAstHydrate, OpTraverseGraphWide,

	OpCollectChunk, OpCollectFinalize, OpCollectFetchSubgraph,

	OpPipelineGapScan, OpPipelineGraphDiscovery,
	OpPipelineGenPoll, OpPipelineEmbedWriteback,
	OpCorpusDeltaDrain, OpRebuildSegments, OpSegmentDeltaMerge,
	OpSegmentHeal, OpSegmentReconcile, OpSegmentRepair, OpSegmentHorizonSeed,
	OpInstructionBootstrap, OpPropagationReflect,
	OpCrossGraphProbe, OpPostCollectFanout,

	OpToolUnknown, OpUnstamped,
}

// toolOperations maps an advertised MCP tool name to its operation. It is keyed
// by the catalog's tool names; TestOperationVocabulary asserts the two sets
// agree, so a tool added to the catalog without a term here fails the build
// rather than silently arriving as OpToolUnknown in production.
var toolOperations = map[string]Operation{
	"query":            OpQuery,
	"traverse":         OpTraverse,
	"mutate":           OpMutate,
	"delete":           OpDelete,
	"manage":           OpManage,
	"sync":             OpSync,
	"thoughts":         OpThoughts,
	"search":           OpSearch,
	"file_symbols":     OpFileSymbols,
	"collect":          OpCollect,
	"custom_collector": OpCustomComputer,
	"ast":              OpAst,
	"help":             OpHelp,
	"record_decision":  OpRecordDecision,
	"analyze_usage":    OpAnalyzeUsage,
	"manage_checks":    OpManageChecks,
	"create_plan":      OpCreatePlan,
	"create_ticket":    OpCreateTicket,
	"create_project":   OpCreateProject,
	"create_research":  OpCreateResearch,
	"create_test_plan": OpCreateTestPlan,
	"assemble":         OpAssemble,
}

// OpToolUnknown is stamped for a tool name with no declared term. It is a
// DECLARED term rather than the raw name, which is the point: passing the name
// through would make the dimension unbounded the moment anything issued an
// unrecognized tool call, and an unbounded label is worse than a coarse one.
// Seeing it in the metrics means the catalog gained a tool and this file did
// not — which TestOperationVocabulary is there to catch first.
const OpToolUnknown Operation = "tool.unknown"

// OperationForTool resolves an advertised MCP tool name to its operation,
// falling back to OpToolUnknown. Callers stamp the result on the request ctx at
// the dispatch entry point, so every covered RPC issued while handling that tool
// call inherits it without any per-call-site bookkeeping.
func OperationForTool(name string) Operation {
	if op, ok := toolOperations[name]; ok {
		return op
	}
	return OpToolUnknown
}
