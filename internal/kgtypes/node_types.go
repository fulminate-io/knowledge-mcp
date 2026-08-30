// SPDX-License-Identifier: Apache-2.0

package kgtypes

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

	// Cloud graph node types — created by cloud collectors.
	NodeCloudResource NodeType = "cloud-resource" // cloud infrastructure resource (EC2, VPC, IAM role, GCS bucket, etc.)

	// CI/CD graph node types — created by CI/CD collectors.
	NodeCICDResource NodeType = "cicd-resource" // CI/CD resource (workflow, pipeline, runner, environment, etc.)

	// Log graph node types — created by log ingestion.
	NodeLogTemplate NodeType = "log-template" // clustered log pattern with <*> wildcards
	NodeLogStream   NodeType = "log-stream"   // unique label-set identifying a log source
	NodeLogChunk    NodeType = "log-chunk"    // time-bounded compressed block of log entries
	NodeLogLabel    NodeType = "log-label"    // shared low-cardinality label (e.g., namespace=prod)

	// NodeLogBackend is a persistent configuration record describing how to
	// reach a log backend (CloudWatch, Loki, Elasticsearch, ...). Unlike the
	// other NodeLog* types it lives in the knowledge graph (GraphKnowledge),
	// NOT in GraphLogs, because the configuration must survive log-graph
	// discard cycles. The metadata contract is:
	//
	//   - name        (key used for lookup; stored in SymbolName too)
	//   - provider    (cloudwatch | loki | stackdriver | elasticsearch | ...)
	//   - url         (base endpoint or project identifier for the backend)
	//   - auth_type   (bearer | basic | aws_profile | api_key | service_account | ...)
	//   - credential  (raw credential value or $ENV_VAR reference — stored
	//                  encrypted at rest alongside every other graph blob)
	//
	// No storage or BM25 wiring is required: the node piggy-backs on the
	// existing knowledge graph infrastructure.
	NodeLogBackend NodeType = "log-backend"

	// Pattern catalog node types.
	NodePattern    NodeType = "pattern"     // canonical design pattern (library or project instantiation)
	NodeReuseCheck NodeType = "reuse_check" // recorded proof a planner/implementer searched before authoring new code
	NodeUseCase    NodeType = "use_case"    // granular pattern applies-when / avoid-when condition (pattern → use_case via applies-when / avoid-when edge)
	NodeExample    NodeType = "example"     // pattern exemplar — code snippet or reference with language/attribution metadata

	// NodeGraphTypeDef is the user-registered graph-type configuration record.
	// It carries the combined collector + behavior definition for a
	// new arbitrary graph type, stored as a per-account, graph-resident config
	// node. Like NodeLogBackend it is a configuration record and
	// opts out of LLM summarization and embedding. The record body is persisted
	// as a single base64 serialized-proto blob under the "graph_type_def_pb"
	// metadata key (see cmd/knowledge/internal/graphtypecrud/codec.go), so both
	// the client and the server decode the SAME proto with one proto.Unmarshal.
	// The wire string is mirrored verbatim by the server store vocabulary
	// (cmd/knowledge-server/internal/store/node_types_vocab.go) — a deliberate
	// dual declaration across the two modules (no shared package).
	NodeGraphTypeDef NodeType = "graph_type_def"

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
	NodeUseCase: true, NodeExample: true,
	NodeMetaValue: true,
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
