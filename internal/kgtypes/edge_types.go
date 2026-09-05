// SPDX-License-Identifier: Apache-2.0

package kgtypes

// Code edge types (uppercase, from static analysis).
const (
	EdgeCalls    EdgeType = "CALLS"
	EdgeImports  EdgeType = "IMPORTS"
	EdgeContains EdgeType = "CONTAINS"
	EdgeUsesType EdgeType = "USES_TYPE"

	// EdgeImplements records that a concrete type satisfies an interface,
	// derived syntactically from the declaration index rather than from a type
	// checker. It is emitted at TWO LEVELS: interface type declaration → concrete
	// type declaration, and interface method spec → the method satisfying it.
	//
	// DIRECTION IS FROM THE INTERFACE OUTWARD, which is what makes the two-hop
	// model work: a caller standing on a call's target — the interface method —
	// reaches the implementers with one outbound traversal, instead of the call
	// itself fanning out across every type declaring a method of that name.
	//
	// IT HAS TWO DERIVATIONS, and Edge.Method is what tells them apart. Go
	// METHOD-SET MATCHING infers satisfaction the language leaves implicit, by
	// comparing resolved signatures, and stamps EdgeMethodMethodSet. DECLARED
	// CONFORMANCE reads a supertype clause the source WROTE — an implements, an
	// extends, a mixin, a behavior, a trait — and stamps
	// EdgeMethodDeclaredConformance followed by that clause's kind. Both levels
	// and the direction above are identical for either derivation; only the
	// question each answers differs, so a consumer that cares reads the prefix
	// rather than the edge type.
	//
	// EdgeType is a defined string type and the vocabulary is open, so this
	// constant needs no proto change; TEST_CALLS is the in-tree precedent. The
	// producer mirrors it as treesitter.EdgeImplements, and the two are pinned in
	// lockstep by TestImplementsVocabularyLockstep.
	EdgeImplements EdgeType = "IMPLEMENTS"

	// EdgeTestCalls is a CALLS edge whose SOURCE is test code: the body of a
	// test_block chunk, or a declaration lexically inside one. It is a distinct
	// type rather than a flag on EdgeCalls so that every existing CALLS
	// consumer — centrality, blast radius, the god-object metrics — keeps
	// seeing production call structure only, and so a consumer that WANTS test
	// traffic opts into it explicitly. EdgeType is a defined string type and
	// the vocabulary is open, so this constant needs no proto change.
	//
	// IT MEANS "test-origin AND identifiable as such", NOT "all test-origin".
	// Identifiability is range containment inside a test_block chunk, so the 18
	// languages with no TestBlocks query have no test_block range for their
	// test declarations to sit inside and their edges stay EdgeCalls. A
	// consumer that opts out of TEST_CALLS still sees that residue as CALLS.
	//
	// The producer mirrors this constant as treesitter.EdgeTestCalls (the
	// chunker carries its own EdgeType vocabulary); the two are pinned in
	// lockstep by TestTestCallsConsumerCensus.
	EdgeTestCalls EdgeType = "TEST_CALLS"

	// EdgeLanguage links a code symbol to its per-language hub node
	// (NodeLanguage with deterministic ID lang:<repo>:<lang>). Emitted
	// once per non-comment chunk during indexing so topology analyzers
	// can roll up PageRank / fan-in / etc. per language via a single
	// outbound traversal from each symbol.
	//
	// File → symbol membership is NOT duplicated as a reverse edge:
	// callers reverse-walk the existing EdgeContains (file → symbol)
	// with direction="in" instead of materializing a separate
	// "defined-in" edge type.
	EdgeLanguage EdgeType = "LANGUAGE"

	// EdgeFlowsToReturn, EdgeFlowsToArg and EdgeFlowsToField are the collector's
	// MODEL-FREE FLOW FACTS: syntactic, per-declaration statements that a
	// parameter or receiver reaches a return position, a call argument, or a
	// field, derived from the declaration's own tree by a per-language flow-step
	// arm plus a language-agnostic alias closure — no type checker, no
	// interprocedural model, no taint configuration.
	//
	// THREE TYPES, NOT FOUR. The unresolved-callee fact is a FLOWS_TO_ARG edge in
	// self-edge SHAPE, not a fourth type. The uppercase spelling is what the
	// COLLECTOR WRITES and is therefore what the graph stores; nothing forces it
	// at the client any more. A caller naming a lowercase spelling still reaches
	// these edges, because the client resolves an edge-type spelling against the
	// graph's own stored vocabulary and a unique case-insensitive match adopts
	// the stored one.
	//
	// ENDPOINTS. FLOWS_TO_RETURN is a SELF-EDGE on the declaration. FLOWS_TO_ARG
	// targets the callee's declaration — the same target the sibling CALLS edge
	// names — or, where the callee resolves to nothing in-repo, is a SELF-EDGE
	// carrying the callee spelling in Evidence. FLOWS_TO_FIELD targets the
	// declaration owning the field, with the field name in Evidence. Self-edges
	// are first-class on the collect path: store/graph_edges.go:272-297 keys edge
	// membership on (type, toId, groupKey=Evidence) with no from==to guard, and
	// cloud/store/collect_edge_decline_integration_test.go:136-146 drives them
	// through a real collect.
	//
	// WHAT A FACT PROVES, AND WHAT ITS ABSENCE DOES NOT:
	//
	//   - PRESENCE IS PROOF. A fact on argument j proves that argument is
	//     param-derived, hence not a constant literal.
	//   - ABSENCE IS STRONG EVIDENCE, NOT PROOF. Alias closure is IN, so
	//     `a := p; sink(a)` does record. Absence still does not prove a constant
	//     where a flow shape no arm models is in play (a value crossing a
	//     channel, a closure capture an arm declines, a container element), or
	//     where the callee-spelling parity rule made an arm decline a call whose
	//     spelling it could not reproduce byte-identically. Any consumer check
	//     must say which of the two it relies on.
	//   - NO ARGUMENT KIND OR TEXT, EVER, at any widening. The fact is about the
	//     PARAM, not the argument's syntax; a check needing a literal's content
	//     goes through the ast route instead.
	//   - AN ALL-CONSTANT CALL PRODUCES NO FACT. These edges are anchored on
	//     param flows rather than call sites and are not a call-site inventory,
	//     which is also why a flow arm's callee set is a SUBSET of
	//     extractCallEdges' set over a general fixture.
	//
	// TEST-ORIGIN FACTS CARRY NO DISTINCT EDGE TYPE. No FLOWS_TO_*_TEST variant
	// exists, and this is NOT symmetric with CALLS → TEST_CALLS retyping. A
	// consumer filters on the SOURCE NODE'S IsTest/TestKind instead: a non-empty
	// TestBlocks query — which TEST_CALLS retyping depends on — covers 14
	// languages and includes NEITHER Go NOR Python, while testKindClassifiers
	// covers 17 including both, so the node flag is the only route that works
	// there.
	EdgeFlowsToReturn EdgeType = "FLOWS_TO_RETURN"
	EdgeFlowsToArg    EdgeType = "FLOWS_TO_ARG"
	EdgeFlowsToField  EdgeType = "FLOWS_TO_FIELD"

	// EdgeEvidenceFlowPrefix opens the Evidence string every flow edge carries.
	// The full grammar, authoritative for producers and consumers alike:
	//
	//	<evidence> := "flow:" <source> ">" <sink> [ "@" <callee spelling> ] [ "|" <groupkey> ]
	//	<source>   := "p" <decimal param index>  |  "recv"
	//	<sink>     := "r" <decimal result index> |  "a" <decimal arg index> | "f:" <field name>
	//
	// Examples: flow:p0>r0 · flow:p1>a2 · flow:recv>f:cache ·
	// flow:p0>a0@exec.Command (unresolved callee) ·
	// flow:p1>a2|svc.helper:FLOWS_TO_ARG:svc.handler:0 (ambiguous-callee group member).
	//
	// THE UNRESOLVED-CALLEE TEST IS STRUCTURAL, NEVER TEXTUAL. An edge is an
	// unresolved-callee self-edge exactly when Type == FLOWS_TO_ARG &&
	// FromId == ToId, and NOT when its Evidence contains an @.
	//
	// THE CALLEE SPELLING MAY CONTAIN @ AND >, measured rather than assumed:
	// calleeIsNameable admits "_$@#-" as callee-name characters in every
	// language, and calleeSeparators is ".:>", so > is an admitted qualifier
	// separator in every language too. Ruby composes
	// `@logger.info`, C++ composes `p->method`, and C# verbatim identifiers spell
	// `@class.Method` — three ORDINARY RESOLVED callees the textual rule
	// misclassifies.
	//
	// PARSE LEFT-TO-RIGHT, NEVER BY LastIndex. Both optional components stay
	// recoverable because every component to their left is drawn from a CLOSED
	// vocabulary: strip "flow:"; consume <source>, which is "recv" or "p" plus a
	// decimal run and can contain neither > nor @; the NEXT byte is the ">"
	// separator, the FIRST > in the string, which is why a > inside a callee
	// spelling further right is harmless; consume <sink>, "r"/"a" plus a decimal
	// run or "f:" plus a field name; a leading @ after an "a"<digits> sink then
	// opens the callee spelling — FLOWS_TO_FIELD and FLOWS_TO_RETURN have no
	// callee — running to the FIRST "|" or to end-of-string; a leading "|" opens
	// the group key, running to end-of-string.
	//
	// "|" IS THE ONE SEPARATOR NO SPELLING CAN CONTAIN: it is absent from
	// calleeSeparators (".:>"), from the admitted name set ("_$@#-"), and from
	// every NameExtra and ChainOps value in chunker_callee_profile.go. A field
	// name is a single grammar identifier and cannot contain one either.
	//
	// A CONSUMER MUST PARSE BOTH OPTIONAL COMPONENTS. Dropping the group-key
	// suffix silently misreads an ambiguous-callee edge (Confidence 1/N) as an
	// exact bind; dropping the @ component loses the callee spelling that makes
	// an unresolved fact readable at all.
	//
	// DETERMINISM, required because Evidence is part of the
	// (from,to,type,evidence) GC identity: every component derives from the
	// declaration's own shape — ordinal positions, a written field name, a
	// written callee spelling — and NONE from a byte offset, line number or walk
	// order. treesitter/types.go records the measured defect a position-derived
	// key caused on a live graph.
	EdgeEvidenceFlowPrefix = "flow:"
)

// Knowledge edge types (lowercase, for knowledge graph relationships).
const (
	EdgeKGContains   EdgeType = "contains"     // parent → child (plan → phase, phase → step)
	EdgeDependsOn    EdgeType = "depends-on"   // must complete before
	EdgeVerifies     EdgeType = "verifies"     // criterion → step
	EdgeInformedBy   EdgeType = "informed-by"  // decision ← finding/research
	EdgeSupports     EdgeType = "supports"     // evidence → decision
	EdgeAnswers      EdgeType = "answers"      // finding → research question
	EdgeRelatesTo    EdgeType = "relates-to"   // general association
	EdgeKGImplements EdgeType = "implements"   // step → code resource
	EdgeReferences   EdgeType = "references"   // finding → reference (paper/URL)
	EdgeUses         EdgeType = "uses"         // agent/skill → tool_guide it relies on
	EdgeAudits       EdgeType = "audits"       // ticket/plan → language_pattern (defensive — "audit the implementation against this anti-pattern")
	EdgeConstrains   EdgeType = "constrains"   // rule → agent/skill it governs
	EdgeInstantiates EdgeType = "instantiates" // project pattern → library pattern (concrete instantiation of a canonical pattern)
	EdgeAppliesWhen  EdgeType = "applies-when" // pattern → use_case condition under which the pattern should be applied
	EdgeAvoidWhen    EdgeType = "avoid-when"   // pattern → use_case condition under which the pattern should be avoided
	// EdgeTranslatedFrom records provenance from a node in a target domain graph
	// (e.g. practice/design-patterns) back to the source node it was synthesized
	// from (in any source graph type — web, code, cloud, knowledge, …) during a
	// transformer run. Evidence carries the source slug.
	EdgeTranslatedFrom EdgeType = "translated-from" // target-domain node → source node (transformer provenance)

	// EdgeMetaValue links a node to a shared value-node that holds the
	// actual content for one of its metadata keys. Emitted by the
	// self-tuning metadata storage layer (plan T1) when the per-graph
	// promotion registry decides a key should be stored as edges instead
	// of inline scalars in Node.Metadata. The metadata key itself is
	// stored on Edge.Method so a single edge type can carry every
	// promoted key without polluting the EdgeType enum.
	//
	// Reads via Node.Value(key) traverse outgoing EdgeMetaValue edges
	// whose Method matches key; writes via Node.SetValue dedupe the
	// value-node by deterministic ID (see valueNodeID). Distinct from
	// EdgeReferences (generic node references) so traverse filters can
	// isolate metadata edges cleanly.
	EdgeMetaValue EdgeType = "meta_value" // node → value-node (metadata key on Edge.Method)

	// EdgeMethodAmbiguousName and EdgeMethodDynamic are Edge.Method VALUES, not
	// edge types. Every member of one group also shares a key in Edge.Evidence.
	//
	// Edge.Method POPULATIONS ARE KEYED BY EDGE TYPE.
	//
	// The field is not one vocabulary with a fixed number of meanings; it is a
	// per-edge-type slot, and which population a value belongs to is decided by
	// the edge that carries it. THE RULE IS SCOPED TO THE CODE COLLECTOR'S OWN
	// EDGES — Edge.Method carries unrelated values elsewhere in this same file,
	// where EdgeMetaValue puts a promoted metadata KEY on it — so nothing below
	// is a claim about every edge in every graph.
	//
	// The populations the code collector emits today:
	//
	//  1. GROUP KIND — one of the two constants declared here, on every member
	//     of a multi-candidate group. Emitted for reference edges (CALLS,
	//     TEST_CALLS, USES_TYPE, EMBEDS) AND for the ambiguous Go-receiver
	//     containment case, so it is NOT exclusive to reference edges: a CONTAINS
	//     edge whose receiver type resolved to several candidates carries a group
	//     kind too.
	//  2. RESOLVING RUNG — the name of the resolution rule that bound the
	//     reference, on a BOUND reference edge, so a surprising edge is
	//     attributable at read time. The vocabulary is the collector's own RefRule
	//     constant set in cmd/knowledge/internal/collector/parser/resolve_walk.go,
	//     which is the single authority for it; the values are deliberately NOT
	//     restated here, because a copy of a vocabulary is a copy that goes stale.
	//     Bound edges only: a single-candidate containment arm emits no Method.
	//  3. METHOD-SET CARDINALITY — on an IMPLEMENTS edge, the EdgeMethodMethodSet
	//     prefix followed by the interface's expanded method-set size. A DERIVED
	//     edge rather than a resolved reference: it never enters the resolution
	//     walk, so it carries neither a group kind nor a rung.
	//  4. DECLARED CLAUSE KIND — on an IMPLEMENTS edge derived from a supertype
	//     clause the source WROTE, the EdgeMethodDeclaredConformance prefix
	//     followed by that clause's kind. Also a DERIVED edge, and never a
	//     cardinality: the clause states the relationship outright, so no method
	//     set was measured and publishing a number would be a false statement in
	//     the one field consumers weight on.
	//  5. FLOW ATTRIBUTION — on a FLOWS_TO_* edge, introducing NO new vocabulary:
	//     an exact bind carries population 2's resolving rung, a multi-candidate
	//     group member carries population 1's group kind, and an unresolved-callee
	//     self-edge carries an EMPTY Method because no rung fired. That empty is a
	//     FACT rather than missing attribution, and it is recognized STRUCTURALLY
	//     (FromId == ToId), never by scanning Evidence.
	//
	// THE SET IS OPEN, AND ADDING TO IT NEEDS NO EDIT HERE beyond a new member.
	// Do not restate a total: a count of populations is exactly the sentence a new
	// one falsifies, in every file that repeats it.
	//
	// THE TWO GROUP KINDS ARE NOT INTERCHANGEABLE. An EdgeMethodAmbiguousName
	// group is CLOSED — the reference means exactly one of these candidates, and
	// a consumer that later learns which may collapse the group to it. An
	// EdgeMethodDynamic group is OPEN — the reference dispatches to one of these
	// candidates OR to something no static enumeration can reach, so a consumer
	// must never read it as closed and must never collapse it.
	//
	// EMPTY METHOD ON A BOUND EDGE.
	//
	// On a graph collected since bound-edge attribution landed, EVERY bound edge
	// carries its rung: the server's edge-meta comparison is Method-aware, so a
	// resident edge whose incoming twin differs only in Method is rewritten
	// rather than skipped. An empty Method on a bound edge there is a REAL
	// SIGNAL — that edge is unattributed — and not an artifact of when it was
	// first written. A graph NOT collected since is the one exception: it still
	// holds pre-stamp bound edges, and on that graph alone an empty Method is
	// ambiguous. One collect ends that state; it is transitional and never a
	// property of the field.
	//
	// They live here rather than in the collector because Edge.Method is a
	// persisted wire field: a reader deciding whether a group may be collapsed
	// needs the vocabulary without importing the producer.
	EdgeMethodAmbiguousName = "ambiguous-name" // CLOSED group: exactly one of the members is the referent
	EdgeMethodDynamic       = "dynamic"        // OPEN group: one of the members, or something beyond static reach

	// EdgeMethodMethodSet is the PREFIX of the Edge.Method value an IMPLEMENTS
	// edge carries: the prefix followed by the decimal cardinality of the
	// interface's expanded method set, e.g. "method-set:3".
	//
	// IT IS THE SURFACE A CONSUMER READS TO WEIGHT A ONE-METHOD EDGE AS
	// LOW-INFORMATION. A single-method interface is legitimately satisfied by a
	// great many types — that is correct Go, not a defect to suppress — so the
	// cardinality is published rather than used to filter.
	//
	// THE CARDINALITY IS NOT CARRIED ON Weight, DELIBERATELY. The weighted
	// topology analyzers normalize a zero weight to the 1.0 baseline, so putting
	// the size on Weight would INVERT the intent: the low-information
	// single-method edges would enter weighted centrality at exactly an ordinary
	// edge's strength, while a large interface's edge took many times an ordinary
	// edge's random-walker mass. No weighted analyzer reads Method, which is why
	// it is the right home — the same reason the two group-kind values above live
	// here rather than in the collector.
	EdgeMethodMethodSet = "method-set:" // IMPLEMENTS: prefix + expanded method-set cardinality

	// EdgeMethodDeclaredConformance is the PREFIX of the Edge.Method value an
	// IMPLEMENTS edge derived from a DECLARED supertype clause carries: the
	// prefix followed by the kind of clause the source wrote, e.g.
	// "declared-conformance:mixin".
	//
	// IT CARRIES A CLAUSE KIND RATHER THAN A CARDINALITY, and the distinction is
	// the point. The method-set derivation INFERS satisfaction the language
	// leaves implicit, so the size of the set it matched is the honest measure
	// of how much the edge says. A declared clause states the relationship
	// outright, so there is no measured set — the informative fact is WHICH
	// clause was written, which is what lets a consumer tell a module include
	// from an implements clause without knowing the producing language.
	//
	// THE MEMBER-LEVEL EDGE CARRIES THE SAME VALUE AS ITS TYPE-LEVEL PARENT,
	// byte-for-byte, mirroring the method-set derivation's own contract: one
	// value is computed per pair and stamped on the type-level edge and on every
	// member edge under it.
	EdgeMethodDeclaredConformance = "declared-conformance:" // IMPLEMENTS: prefix + the declared clause kind

	// EdgeMethodSlotBind is the PREFIX of the Edge.Method value an IMPLEMENTS
	// edge derived from a C COMPOSITE-LITERAL SLOT carries: the prefix followed
	// by the capture shape that produced it, either "slot-bind:designated" or
	// "slot-bind:positional".
	//
	// IT IS C'S CONFORMANCE, WRITTEN THE ONLY WAY C CAN WRITE ONE. The language
	// declares no supertype and has no clause to read; what it has is a struct
	// of function pointers filled by a literal, and that field-to-function pair
	// states the same relationship a declared conformance states outright.
	//
	// THE SUFFIX IS THE SHAPE RATHER THAN THE SLOT NAME. A reader judging how
	// much the edge says needs to know whether the source named the field
	// outright or whether the field was derived from the declaration's field
	// ORDER — the second is exact but rests on one more inference — and the
	// slot name is already recoverable from the edge's own endpoint.
	EdgeMethodSlotBind = "slot-bind:" // IMPLEMENTS: prefix + the capture shape

	// Thought graph edge types.
	EdgeNext            EdgeType = "next"             // sequential thought chain
	EdgeBranchesFrom    EdgeType = "branches-from"    // new direction after invalidation
	EdgeChargedBy       EdgeType = "charged-by"       // thought → charge
	EdgeEvidencedBy     EdgeType = "evidenced-by"     // charge → evidence artifact
	EdgeProduced        EdgeType = "produced"         // thought → artifact it created
	EdgeSynthesizedFrom EdgeType = "synthesized-from" // finding → original thoughts it was synthesized from
	// EdgeBecause: from=consequence, to=cause. A —because→ B reads
	// "A is true/happens because B is true/happens." Distinct from
	// EdgeRelatesTo (general association) and EdgeInformedBy (evidential support).
	EdgeBecause EdgeType = "because" // consequence → cause (causal/explanatory link)
)
