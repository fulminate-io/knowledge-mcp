// SPDX-License-Identifier: Apache-2.0

// operation_admission.go partitions the closed operation vocabulary into terms
// whose RPCs may admit a graph into this process's working set and terms that
// must never admit one.
//
// THE RULE: there must not be any background process in the client process that
// requests or interacts with graphs in any way unless some kind of mcp query
// like search, mutate, collect has interacted with it directly. Management
// operations do not count towards interaction.
//
// THE DECIDING PRINCIPLE, applied to every term: does the USER's call interact
// with that SPECIFIC graph directly? This partition is HALF the gate — the other
// half is that the request's Target must resolve a concrete graph INSTANCE,
// enforced by the recorder in Router.Execute. An operation naming only a graph
// TYPE, which is exactly the catalog-enumeration shape, resolves no instance key
// and admits nothing by construction, for every family whose instance key is
// repo / account / language. The knowledge family is the named exception: it is
// single-instance, so a type-only knowledge target IS the default instance and
// does admit. For knowledge the partition below therefore stays load-bearing
// rather than being backstopped by structure.

package graphclient

// admittingOperations is the direct-interaction set: the operations whose RPCs
// carry a user's own interaction with a named graph.
//
// Reads count. The rule names "some kind of mcp query like search, mutate,
// collect", and a user reading a named graph has interacted with that graph as
// directly as one writing it.
var admittingOperations = map[Operation]struct{}{
	// Reads.
	OpSearch:                    {},
	OpQuery:                     {},
	OpTraverse:                  {},
	OpTraverseGraphWide:         {},
	OpFileSymbols:               {},
	OpFileSymbolsSuffixFallback: {},
	OpAst:                       {},
	OpAstHydrate:                {},
	OpAssemble:                  {},

	// Writes.
	OpMutate:         {},
	OpDelete:         {},
	OpThoughts:       {},
	OpRecordDecision: {},
	OpCreatePlan:     {},
	OpCreateTicket:   {},
	OpCreateProject:  {},
	OpCreateResearch: {},
	OpCreateTestPlan: {},

	// Collect.
	OpCollect:              {},
	OpCollectChunk:         {},
	OpCollectFinalize:      {},
	OpCollectFetchSubgraph: {},
	OpCustomComputer:       {},

	// Worker. worker(trigger) names the code graph its scan payload narrows to,
	// so the user's call interacts with that graph directly.
	OpWorker: {},
}

// AdmitsWorkingSet reports whether an RPC stamped with op may admit its target
// graph into the working set.
//
// DEFAULT-DENY: an operation absent from the admitting set returns false, and so
// does an unstamped context (the caller gets ok=false from OperationFromContext
// and must not admit). Three categories are denied, and each is denied for its
// own reason:
//
//   - MANAGEMENT OPERATIONS {OpManage, OpSync} — excluded by the rule's own
//     clause, "manage operations do not count towards interaction". This is the
//     one place the deciding principle is overridden by name rather than by
//     structure: both members address concrete graph instances, so the
//     instance-key half of the gate does not stop them. manage(status) fans a
//     per-graph read across EVERY account graph, so without this exclusion one
//     status call would admit the entire account and the fix would be inert. A
//     user who pulls a graph and then uses it is admitted by the use.
//
//   - SELF-ADMISSION VECTORS — the background terms that would make the working
//     set feed itself. OpPipelineEmbedWriteback is the load-bearing one: pipeline
//     writeback is NOT an admission, and admitting it would make the working set
//     self-admitting and the whole gate a no-op. The recorder sits on the routed
//     call chokepoint that background and user traffic share, so this partition
//     is the SOLE mechanism keeping writeback out — there is no second,
//     structural exclusion behind it.
//
//   - NO GRAPH INSTANCE ADDRESSED — hive coordination, static help text and the
//     local transcript cache address no graph, and the unknown/unstamped
//     fallbacks address nothing at all. Their classification is behaviourally
//     inert because the instance-key gate would refuse them anyway; they are
//     classified explicitly so the partition closes over the whole vocabulary
//     and a newly declared term cannot default silently into either half.
func AdmitsWorkingSet(op Operation) bool {
	_, ok := admittingOperations[op]
	return ok
}
