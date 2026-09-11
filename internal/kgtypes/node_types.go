// SPDX-License-Identifier: Apache-2.0

package kgtypes

import "slices"

// Pipeline node types — created by the indexer pipeline.
const (
	NodeFile     NodeType = "file"     // source file node (one per indexed file)
	NodePackage  NodeType = "package"  // directory/package node (created by hierarchy builder)
	NodeBranch   NodeType = "branch"   // branch metadata node (created by reindex pipeline)
	NodeLanguage NodeType = "language" // per-language hub node (one per (repo, language) — symbol → lang_node via EdgeLanguage)
)

// Knowledge node types — created by users/LLM.
const (
	NodeProject    NodeType = "project"     // long-lived container for tickets
	NodeTicket     NodeType = "ticket"      // unit of work within a project
	NodePlan       NodeType = "plan"        // body of work with phases
	NodePhase      NodeType = "phase"       // stage within a plan
	NodeStep       NodeType = "step"        // implementation task within a phase
	NodeCriterion  NodeType = "criterion"   // success criterion
	NodeDecision   NodeType = "decision"    // design choice with rationale
	NodeFinding    NodeType = "finding"     // discovery from research
	nodeMemory     NodeType = "memory"      // persistent fact or preference
	NodeResearch   NodeType = "research"    // research project (container for questions)
	NodeQuestion   NodeType = "question"    // research sub-question (open/investigating/answered)
	NodeReference  NodeType = "reference"   // external source: paper, URL, tool
	nodeResource   NodeType = "resource"    // code artifact: file, package, function
	NodeEvent      NodeType = "event"       // something that happened: commit, deploy
	NodeDocument   NodeType = "document"    // general document (plan, spec, notes)
	NodeGithubRepo NodeType = "github_repo" // root anchor for a materialized github (owner, repo, ref) — emitted by the web collector's github materializer
	NodeRule       NodeType = "rule"        // codebase constraint: lint rules, conventions, patterns

	// Workflow/instruction node types — agents, skills, test plans.
	NodeTestPlan  NodeType = "test_plan"  // structured test plan with steps
	NodeTestStep  NodeType = "test_step"  // individual test step within a test plan
	NodeTestRun   NodeType = "test_run"   // execution instance of a test plan or step
	NodeAgent     NodeType = "agent"      // AI agent definition with phases and tool guides
	NodeSkill     NodeType = "skill"      // reusable skill or capability an agent can invoke
	nodeToolGuide NodeType = "tool_guide" // guidance doc for using a specific tool

	// Thought graph node types.
	NodeThought        NodeType = "thought"         // unit of reasoning
	NodeCharge         NodeType = "charge"          // evidence charge on a thought
	NodeThoughtSession NodeType = "thought_session" // groups thoughts about one concern

	// Multi-root node types.
	NodeProxy NodeType = "proxy" // lightweight reference to a node in another graph

	// Pattern catalog node types.
	NodePattern    NodeType = "pattern"     // canonical design pattern (library or project instantiation)
	NodeReuseCheck NodeType = "reuse_check" // recorded proof a planner/implementer searched before authoring new code
	NodeUseCase    NodeType = "use_case"    // granular pattern applies-when / avoid-when condition (pattern → use_case via applies-when / avoid-when edge)
	NodeExample    NodeType = "example"     // pattern exemplar — code snippet or reference with language/attribution metadata

	// NodeIdiom is the practice node a language-idiom landing writes: one
	// convention of a language with an author-supplied summary. The wire string
	// mirrors the server vocabulary (cmd/knowledge-server/internal/store/
	// node_types_vocab.go NodeIdiom) verbatim, a deliberate per-module duplicate.
	NodeIdiom NodeType = "idiom" // one language convention, from a language-idiom landing

	// NodeSource is the HUB a practice node is grouped under in the combined
	// practice graph. One hub per origin — a language, a catalog, or a collected
	// run — and each practice node names exactly one of them, by a `source_hub`
	// metadata key carrying the hub's id and by a `sourced-from` edge pointing at
	// it. A `kind` metadata key on the hub itself says which of the three it is.
	//
	// IT REPLACED THE GRAPH NAME AS THE GROUPING. The practice family used to
	// hold one graph per language and the graph name WAS the grouping; the
	// combined graph holds one, so the grouping had to become a node.
	//
	// It is summarized and embedded on the same terms as every other practice
	// node type: NEVER auto-summarized, embedded from an author-supplied summary.
	// A hub that ranks is what lets "which sources exist" be answered by search
	// rather than by a second enumeration API.
	NodeSource NodeType = "source" // practice hub: the origin a set of practice nodes is grouped under

	// NodeGraphTypeDef is the user-registered graph-type configuration record.
	// It carries the combined collector + behavior definition for a
	// new arbitrary graph type, stored as a per-account, graph-resident config
	// node. It is a configuration record rather than corpus, so it opts out of
	// LLM summarization and embedding. The record body is persisted
	// as a single base64 serialized-proto blob under the "graph_type_def_pb"
	// metadata key (see cmd/knowledge/internal/graphtypecrud/codec.go), so both
	// the client and the server decode the SAME proto with one proto.Unmarshal.
	// The wire string is mirrored verbatim by the server store vocabulary
	// (cmd/knowledge-server/internal/store/node_types_vocab.go) — a deliberate
	// dual declaration across the two modules (no shared package).
	NodeGraphTypeDef NodeType = "graph_type_def"

	// Plan-part node types — the chunked plan shape.
	//
	// A plan in the chunked shape is a ROOT node carrying the goal plus one
	// NodePlanSection child per part, joined by positioned `contains` edges. Each
	// section body is written and read ALONE, so a planner revising one part
	// issues one write against one node and every other node is untouched.
	//
	// NodePlanAnnotation is a reviewer's note ON a section — correct, finding, or
	// needed change — joined to that section by the existing relates-to edge. The
	// NODE TYPE carries the meaning, which is why no new edge type was added: the
	// section tree traverses `contains` only, so annotations stay out of the tree
	// and cannot change a plan's rendered shape.
	//
	// THE LITERALS ARE QUALIFIED ("plan_section", not "section") because the bare
	// "section" is already a node type in the raw web and pdf graphs. The wire
	// strings are mirrored verbatim by the server store vocabulary
	// (cmd/knowledge-server/internal/store/node_types_vocab.go) — a deliberate
	// dual declaration across the two modules (no shared package) — and a
	// per-module drift-guard pair plus the cross-module vocabulary census pin
	// them.
	NodePlanSection    NodeType = "plan_section"    // one part of a chunked plan; body written and read alone
	NodePlanAnnotation NodeType = "plan_annotation" // a reviewer's note on one plan section

	// Self-tuning metadata storage node types.
	//
	// NodeMetaValue is a shared value-node holding the actual content for
	// one promoted metadata key. The dispatch layer (plan T1) creates one
	// NodeMetaValue per (graph, key, value) triple and links every owner
	// node to it via EdgeMetaValue (with the metadata key on Edge.Method).
	// Value-nodes are storage primitives, not user-facing knowledge — the
	// summarizer/embedder pipeline should skip them. Listed in
	// knowledgeTypes so the IsCodeType classifier doesn't mistakenly
	// route them through the code-graph summarization path.
	NodeMetaValue NodeType = "meta_value"
)

// knowledgeTypes is the set of node types created by users/LLM.
var knowledgeTypes = map[NodeType]bool{
	NodeProject: true, NodeTicket: true,
	NodePlan: true, NodePhase: true, NodeStep: true, NodeCriterion: true,
	NodeDecision: true, NodeFinding: true, nodeMemory: true, NodeResearch: true,
	NodeQuestion: true, NodeReference: true, nodeResource: true, NodeEvent: true,
	NodeDocument: true, NodeGithubRepo: true, NodeRule: true,
	NodeThought: true, NodeCharge: true, NodeThoughtSession: true,
	NodeProxy:    true,
	NodeTestPlan: true, NodeTestStep: true, NodeTestRun: true,
	NodeAgent: true, NodeSkill: true, nodeToolGuide: true,
	NodePattern: true, NodeReuseCheck: true,
	NodeUseCase: true, NodeExample: true, NodeSource: true, NodeIdiom: true,
	NodeMetaValue:   true,
	NodePlanSection: true, NodePlanAnnotation: true,
}

// enrolledNodeTypes is this module's copy of the server's ENROLLMENT set — the
// node types the server's eligibility table carries an entry for, which is what
// its authoritative refusal (NodeType.IsKnown) answers from.
//
// IT IS NOT knowledgeTypes ABOVE, and the two must not be folded together.
// knowledgeTypes is the set of user/LLM-authored types and exists to drive
// IsCodeType: it deliberately excludes file, package, branch and language, so
// widening it to the whole vocabulary would silently reclassify every code node.
// This set is the whole declared vocabulary, one member per NodeType const
// declared in this file.
//
// WHY A COPY AT ALL. The two binaries share no hand-written package, so every
// wire vocabulary is declared once per module by design. The drift this invites
// is closed by a census rather than by trust: one leg pins this set to the const
// declarations in THIS file, another pins it to the server's declaration file,
// and a third, in the server module, pins that file to the enrollment table. A
// client set that is a strict subset would otherwise refuse a legitimately
// enrolled type with a message telling the caller to use one of the types it
// just refused.
var enrolledNodeTypes = map[NodeType]bool{
	NodeFile: true, NodePackage: true, NodeBranch: true, NodeLanguage: true,
	NodeProject: true, NodeTicket: true, NodePlan: true, NodePhase: true,
	NodeStep: true, NodeCriterion: true, NodeDecision: true, NodeFinding: true,
	nodeMemory: true, NodeResearch: true, NodeQuestion: true, NodeReference: true,
	nodeResource: true, NodeEvent: true, NodeDocument: true, NodeGithubRepo: true,
	NodeRule: true, NodeTestPlan: true, NodeTestStep: true, NodeTestRun: true,
	NodeAgent: true, NodeSkill: true, nodeToolGuide: true,
	NodeThought: true, NodeCharge: true, NodeThoughtSession: true,
	NodeProxy: true, NodePattern: true, NodeReuseCheck: true, NodeUseCase: true,
	NodeExample: true, NodeIdiom: true, NodeSource: true, NodeGraphTypeDef: true,
	NodePlanSection: true, NodePlanAnnotation: true, NodeMetaValue: true,
}

// IsEnrolled reports whether the vocabulary enrolls this node type — the
// question a graph with a CLOSED type set asks of a write before accepting it.
//
// EXPORTED AS A PREDICATE, NEVER BY EXPORTING THE MAP: a caller that could take
// its own copy of the rule is a caller whose copy can drift from this one, which
// is the whole failure this vocabulary is guarded against.
func (t NodeType) IsEnrolled() bool { return enrolledNodeTypes[t] }

// EnrolledNodeTypes returns the enrolled vocabulary SORTED, for the censuses
// that compare it against the server's. Sorted because it is derived from a
// map, and a comparison whose failure named its members in a different order on
// every run is one no gate can assert.
//
// IT IS THE WHOLE VOCABULARY, system-managed types included, because that is
// what the census compares. A message OFFERING repairs wants
// CreatableNodeTypes below instead.
func EnrolledNodeTypes() []NodeType {
	out := make([]NodeType, 0, len(enrolledNodeTypes))
	for t := range enrolledNodeTypes {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

// systemManagedTypes are the enrolled types a hand-written create is refused
// for whatever graph it names: they are emitted by the code indexer, the web
// collector's github materializer, or the cross-graph link resolver, and a
// user-created one lacks the chunks, vectors, hierarchy or foreign ids its
// producer supplies.
//
// THIS MODULE'S COPY OF A SERVER RULE, on the same terms as the vocabulary
// beside it: the rule's home is the server's systemManagedType switch, which
// runs immediately after the vocabulary check and is authoritative. It is
// mirrored here for ONE purpose — so a refusal composed on this side does not
// offer a repair the next rule refuses — and for nothing else. A client that
// let this set drift wide would only under-offer repairs; one that let it drift
// narrow offers a type the server rejects, which is the failure it exists to
// prevent.
var systemManagedTypes = map[NodeType]bool{
	NodeFile: true, NodePackage: true, NodeBranch: true,
	NodeGithubRepo: true, NodeProxy: true,
}

// CreatableNodeTypes returns the enrolled vocabulary a hand-written create may
// actually use: sorted, minus the system-managed types.
//
// IT IS THE SET A REFUSAL MESSAGE NAMES. A message that listed the whole
// vocabulary offered five repairs — file, package, branch, github_repo, proxy —
// that the very next validation rule refuses for a different reason, so an
// author who took the message at its word got a second refusal. A refusal that
// names a repair owes the caller one the next rule admits.
func CreatableNodeTypes() []NodeType {
	out := make([]NodeType, 0, len(enrolledNodeTypes))
	for t := range enrolledNodeTypes {
		if systemManagedTypes[t] {
			continue
		}
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

// commentTypes are node types that represent comments.
// Comments are indexed for search but not summarized (they're already summaries).
var commentTypes = map[NodeType]bool{
	"comment": true, "line_comment": true, "block_comment": true,
	"documentation_comment": true, "doc_comment": true,
}

// isKnowledgeType returns true for node types created by users/LLM.
func (t NodeType) isKnowledgeType() bool {
	return knowledgeTypes[t]
}

// IsCodeType returns true for node types produced by code indexing.
// This is everything that isn't a knowledge type or a pipeline type (file/package/branch/language).
func (t NodeType) IsCodeType() bool {
	return !t.isKnowledgeType() && t != NodeFile && t != NodePackage && t != NodeBranch && t != NodeLanguage
}

// IsComment returns true if this node type represents a comment.
func (t NodeType) IsComment() bool {
	return commentTypes[t]
}
