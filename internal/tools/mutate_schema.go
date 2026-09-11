// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"maps"
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// MutateToolDef returns the unified mutate tool definition.
//
// The MCP tool catalog is client-owned: loadSchemas composes this def into
// tools/list. Pure kgtools.MCPTool literal.
func MutateToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "mutate",
		Description: "Unified mutation tool. Create, update, or link knowledge nodes. " +
			"operation=create: create a finding, research question, rule, criterion, or generic node. " +
			"operation=create_batch: create N nodes + M edges in one atomic store.Txn. Args: {nodes:[{type,name,description,summary,content,status,metadata}], edges:[{from_idx,to_idx,from_id,to_id,type}]}. Returns {ids:[...]} of length N. It runs on the practice graph too, where source_hub groups the created nodes under their origin hub, and it HONORS a per-node id and source on either graph even though the nodes[] item shape below declares neither. " +
			"operation=update: edit node fields or update status of one or more nodes. " +
			"operation=upsert: create-or-update a node by caller-supplied id. Three types — proxy, graph_type_def, criterion — bypass create-time validation, because they are tool-owned config records whose producers already validated them. Every OTHER type runs the full create-time validation (summary/name and the system-managed-type guard), so upsert is not a general-purpose escape hatch around create. " +
			"operation=link: create a relationship between two nodes. " +
			"operation=unlink: remove a single directed edge keyed by from/to/relationship. Idempotent — returns success even if the edge did not exist. Use to retract a misattached pattern, fix a wrong informed-by, etc. A linkage-graph edge is retracted with graph:\"linkage\" (NOT link_graph, which only link routes) and the PROXY endpoint the link materialized: mutate(unlink, from:<id>, to:\"proxy:knowledge:<code-id>\", relationship:\"...\", graph:\"linkage\"). " +
			"operation=answer: mark a research question as answered. " +
			"Required params by operation (in addition to the always-required operation): " +
			"create requires type (and summary for embed-only-knowledge types); " +
			"create_batch requires nodes; update requires id or ids; update_batch requires items; " +
			"bulk_update_metadata requires updates; upsert requires id + type; " +
			"link / unlink require from + to + relationship; answer requires id (or question_id).",
		InputSchema: kgtools.InputSchema{
			Type:       "object",
			Properties: mutateProperties(),
			Required:   []string{"operation"},
		},
	}
}

// mutateProperties builds the mutate tool's property map. Split out of
// MutateToolDef so the batch array-of-object sub-shapes (mutateBatchProperties)
// keep the def scannable and under the funlen gate.
func mutateProperties() map[string]kgtools.Property {
	props := map[string]kgtools.Property{
		"operation":             {Type: "string", Description: "What to do", Enum: []string{"create", "create_batch", "update", "update_batch", "bulk_update_metadata", "upsert", "link", "unlink", "answer", "delete"}},
		"type":                  {Type: "string", Description: "Node type for create (finding, research, rule, criterion, resource, event, memory, document). criterion is knowledge-graph-only: criteria attach to the plan/step verifies structure, which no other graph family carries, so a criterion create naming any graph — including an explicit graph:\"knowledge\" — is rejected pre-write rather than routed."},
		"id":                    {Type: "string", Description: "Target node ID for update or answer (operation=answer also accepts question_id as an alias)"},
		"ids":                   {Type: "array", Description: "List of node IDs for a batch update over PLAIN LOCAL NON-CONTAINER nodes — the same universal-scalar set_fields (e.g. status) are applied uniformly to every id. Tracker-backed nodes, container nodes (project/ticket/plan/phase/step/test_plan/test_step — for status updates of ANY value), per-type params (command/scope/...), and source must be updated per-id; those batch shapes are rejected. For heterogeneous per-id bodies use update_batch.", Items: &kgtools.Property{Type: "string"}},
		"name":                  {Type: "string", Description: "Node name or title"},
		"description":           {Type: "string", Description: "Node description"},
		"summary":               {Type: "string", MaxLength: 500, Description: "Required search-optimized one-line summary, max 500 chars, when create-ing an embed-only-knowledge node type (NodeType.Summarizable()=false). Handler-side enforcement returns an error when missing/empty/whitespace or > 500 chars. A criterion create requires it on the same terms as every other embed-only-knowledge type — criterion summaries are author-supplied, never composed from the description and command. mutate(answer) requires it too: supply a summary describing the CONCLUDED state of the question, since answering replaces the summary the question was created with."},
		"content":               {Type: "string", Description: "Full content. For research create: context/background."},
		"status":                {Type: "string", Description: "Status to set (for update operation). An explicit empty string CLEARS the status to blank on a local node — the one param whose empty value is a write rather than an omission. Rejected on tracker-backed nodes, whose tracker has no blank state."},
		"expand_to_descendants": {Type: "boolean", Description: "When updating status to a TERMINAL status on a project/ticket/plan/phase/step/test_plan/test_step, also walk the contains tree and write the mapped descendant status to every unsettled descendant whose own type is one of those seven container types. Every other descendant — criterion, question, finding, research, decision, thought, and any other type — is left unchanged: those nodes record evidence rather than task progress, so a container closing above them is evidence of none of it. The response names every id it wrote and every node it left alone. Default true. Set false to update only the named node. This is a SINGLE-ID container path — a batch of container ids carrying a status (ids:[...]) is rejected, so issue container status updates per-id. The cascade never completes a descendant that still has criteria that are not yet evaluated: those nodes are held, named in the response, and completed explicitly once their criteria are marked. The mapped descendant status is completed for completed/done/closed/archived, skipped for canceled/cancelled/wont_do/failed, and superseded for superseded; it applies for a tracker-backed container too. Has no effect for a non-terminal status or a non-container type."},
		"source":                {Type: "string", Description: "Source of the knowledge (the node's provenance). NOT the practice source hub — that is `source_hub`."},
		"source_hub":            {Type: "string", Description: "Practice SOURCE HUB id. On a CREATE it groups the write under one origin: it stamps the hub id onto the node's `source_hub` metadata and links the node to the hub by a `sourced-from` edge, and it REPLACES `language`, which is refused on every practice write arm. On a DELETE it is a SELECTION axis instead — it removes every practice node grouped under that hub, and the hub with them, which is how an abandoned or failed collection is cleaned up as a unit. It is ONE write: a hub carries its OWN id under the `source_hub` key, so the single metadata predicate that selects the members selects the hub too, atomically. Mutually exclusive with ids/id there: a delete carrying both selection axes is denied rather than resolved. On a practice LINK or UNLINK it is neither: it SCOPES THE ENDPOINTS, so `from` and `to` must both already be grouped under that hub and an endpoint under another hub, under none, or absent is refused naming the hub, the endpoint and its actual hub or absence. On a practice UPDATE, UPDATE_BATCH or BULK_UPDATE_METADATA it scopes the TARGETS the same way: every id the call names — `id`, each of `ids`, each `items[].id`, each `updates[].id` — must already be grouped under that hub, and one that is not refuses the whole call with nothing written. On a practice UPSERT it scopes the key the same way when the key RESOLVES. An UPSERT never GROUPS: a hub-carrying upsert whose `id` resolves to no practice node is REFUSED naming `mutate(create, graph:\"practice\", source_hub:...)` as the arm that groups, because grouping writes the `source_hub` key AND the `sourced-from` edge together and only a create plan carries both. Omitted on any of them means the whole practice graph, and nothing hub-related reaches the wire except on the create shapes, where the grouping IS the write, and on an upsert of an existing member. AN UPSERT NEVER UN-GROUPS: it REPLACES the body's metadata rather than merging into it, so a member's stored hub is carried onto the body whether or not the call names one, and an ordinary hub-less field edit keeps its membership instead of dropping the `source_hub` key and leaving the `sourced-from` edge behind. That costs the call one read. A body that names the hub the node is ALREADY under is a second spelling and is left as written; a body naming any OTHER hub is REFUSED naming both, because a body key rides no `sourced-from` edge and would leave the node filed under one hub and linked to another. A MEMBER IS NEVER MOVED BETWEEN HUBS on this surface: no arm rewrites both carriers together. An upsert of a new id or of a node grouped under no hub is unchanged. THE HUB MUST NAME A HUB: the id is resolved before any arm acts on it and must be a live node of type `source` in the combined practice graph, so an id that resolves to nothing, names a deleted node, or names a node of another type is REFUSED naming the hub and the arm. A HUB IS GROUPED UNDER NOTHING BUT ITSELF: it carries its OWN id under the `source_hub` key, which is the contract every producer writes and what makes the by-hub delete one predicate; a `source` node keyed to some OTHER node is refused, because that would make the hub a node belongs to a chain rather than a value. THESE RULES RUN IN THE ENGINE, beneath every caller — the mutate tool, the standalone `delete` tool and the direct compile-and-execute callers alike — so no route reaches a practice write without them; that resolution is one read, folded into the read the hub-scoped arms already pay. AND A BODY ALONE NEVER GROUPS: on a CREATE or CREATE_BATCH naming no `source_hub` parameter, a `source_hub` metadata key inside a body is REFUSED naming this parameter, because the body key writes the metadata without the edge. THE MEMBERSHIP EDGE IS NEVER WRITTEN BY HAND: on graph practice a `link` or `unlink` naming `relationship`=\"sourced-from\" is REFUSED whether or not this parameter is present, because membership is recorded on the metadata key AND that edge together and an edge arm carries only one of them; a node is grouped by a create and a whole collection removed by the by-hub delete. Any other relationship is untouched. A BODY MAY NOT NAME A DIFFERENT HUB: on CREATE, CREATE_BATCH, UPSERT, UPDATE, UPDATE_BATCH and BULK_UPDATE_METADATA, a `source_hub` metadata key inside a body — the top-level `metadata`, a `nodes[]` body, an `items[]` or `updates[]` entry — that names a DIFFERENT hub than this parameter is REFUSED naming the body's path, its hub and this one, with nothing written. A body naming the SAME hub is a second spelling of one fact and lands byte-identically to a body naming none, which is stamped. IT IS A PRACTICE-FAMILY PARAMETER ON EVERY ARM. Every operation carrying it — create, create_batch, update, update_batch, bulk_update_metadata, upsert, link, unlink, answer, delete — is REFUSED naming the family on every family but practice: \"knowledge\", \"code\", \"linkage\", \"checks\", \"web\", \"pdf\", and any registered custom family. An omitted `graph` is the knowledge family and is refused with it. It is never consumed and dropped, because no other family has source hubs for it to name. The standalone `delete` tool publishes the same axis as `source`, and it runs the same resolution: a `source` that does not name a live hub is refused there too, never deleted."},
		"evidence":              {Type: "string", Description: "Supporting evidence (for findings)"},
		"question_id":           {Type: "string", Description: "Research question node ID this finding answers"},
		"supports":              {Type: "string", Description: "Node ID this finding supports — draws a finding--supports-->node edge as the finding is created. Read ONLY by mutate(create, type=finding); every other operation rejects it."},
		"concludes":             {Type: "boolean", Description: "If true and question_id is set, marks the question as answered"},
		"scope":                 {Type: "string", Description: "Rule scope (e.g., '*.go', 'pkg/', 'commits')"},
		"enforcement":           {Type: "string", Description: "Rule enforcement mechanism"},
		"step_id":               {Type: "string", Description: "Step node ID to attach a criterion to"},
		"command":               {Type: "string", Description: "Verification command for criterion"},
		"criterion_type":        {Type: "string", Description: "Criterion type: automated or manual"},
		"from":                  {Type: "string", Description: "Source node ID for link operation"},
		"to":                    {Type: "string", Description: "Target node ID for link operation"},
		"relationship":          {Type: "string", Description: "Relationship type for link (e.g., depends-on, contains, informed-by, relates-to)"},
		"conclusion":            {Type: "string", Description: "Conclusion for answer operation"},
		"findings":              {Type: "string", Description: "Comma-separated finding node IDs for answer operation"},
		"graph":                 {Type: "string", Description: "Target graph for the operation (default: knowledge). Use 'practice' for prose guidance — ONE combined graph that takes no language on a write, where a create groups its node under an origin with source_hub — or 'checks' — a single graph, no language or name — for deterministic corpus checks and their fixture example nodes; each check carries its own 'language' metadata key, and a check write is admitted only after its fixtures run. 'linkage' is valid HERE on operation=unlink — it is how a cross-graph linkage edge is retracted — and the endpoint must be the PROXY id the link materialized (to:\"proxy:knowledge:<code-id>\"), because unlink resolves no raw foreign id the way link does. On operation=link the linkage graph is named by link_graph instead, and link accepts the raw foreign id. A collected graph is addressed the same way every other family is, by its own instance selector: 'code' with repo, and a registered custom type with name."},
		"language":              {Type: "string", Description: "REFUSED on every practice mutate arm: practice is ONE combined graph, so a write has no per-language graph to land in, and a create groups its node under an origin with source_hub instead. It is READ by the checks family, where it is the corpus language of the check node being written."},
		"repo":                  {Type: "string", Description: "Code graph name — REQUIRED for graph='code'; it is never inferred from cwd. Writes to a collected graph are caller-owned: a later collect reconciles that graph from its source and overwrites hand-authored changes."},
		"account":               {Type: "string", Description: "NO BUILT-IN FAMILY IS KEYED BY ACCOUNT. It was the instance key of the retired cloud and cicd families; a collected inventory graph is a registered custom type now, addressed by name. Consumed by nothing today."},
		"metadata":              {Type: "object", Description: "Arbitrary key-value metadata pairs (string→string). On create: sets the node's initial metadata map. On update: merged per-key into existing metadata — keys in the payload overwrite, absent keys are preserved. Retrievable via Node.Value(key)."},
		"keywords":              {Type: "string", Description: "Sets Node.Keywords (top-level struct field, NOT a metadata key). Powers the search BM25 keyword-token boost and the keywords display facet. Wired for the client-side LLM pipeline writeback path; carrying the value in metadata would land it in the inline map and bypass the search-side reader."},
		"binary_vector":         {Type: "string", Description: "Base64-encoded binary embedding to install on the node via PutBinaryVector. Decoded payload length must equal 32 bytes (256-bit). Used by the client-side LLM pipeline writeback path. Mismatched lengths return a structured validation error and no write is performed."},
		"confidence":            {Type: "number", Description: "Edge metadata (operation=link only). 0.0-1.0 caller-asserted confidence. Routed into store.Edge.Confidence via LinkBatch."},
		"method":                {Type: "string", Description: "Edge metadata (operation=link only). Short tag describing how the edge was derived (e.g. 'image-target', 'dockerfile-copy', 'manual'). Routed into store.Edge.Method."},
		"edge_evidence":         {Type: "string", Description: "Edge metadata (operation=link only). Caller-supplied evidence string (file path, snippet, URL) backing the edge. Renamed from `evidence` on the wire to avoid collision with the finding `evidence` field. Routed into store.Edge.Evidence."},
		"last_validated":        {Type: "string", Description: "Edge metadata (operation=link only). RFC3339 timestamp the linker stamps when (re-)asserting an edge. Routed into store.Edge.LastValidated."},
		"link_graph":            {Type: "string", Description: "Optional graph selector for operation=link (e.g. 'linkage' for the cross-graph linkage view). When set, the link is dispatched via store.LinkBatch against the named graph rather than the default knowledge graph. LINK ONLY: operation=unlink does not route this param and rejects it — an unlink names the linkage graph with `graph` and targets the proxy id (see the graph param)."},
		"format":                {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured per operation: create→{ids}; link→{from, to, relationship}; answer→{id, name, conclusion}; update→{ids, fields}; delete→{deleted, total, ids})."},
		// thought / charge create payload fields. Carried by
		// the client-side InterceptThoughts when it translates
		// thoughts(operation:think|charge) into mutate(create,
		// type:thought|charge).
		"branches_from":   {Type: "string", Description: "Thought ID this branches from (mutate(create, type=thought) only). Adds an edge from the new thought to its parent for trace lineage."},
		"links":           {Type: "array", Description: "Node IDs to relate the new node to — any single knowledge-graph create routes it, as does a thought create. Knowledge-graph IDs ride the atomic create as a node--relates-to-->target edge; foreign-graph IDs are linked post-create via the cross-graph linkage. An unresolvable ID is dropped with a warning, never blocking the write. create_batch rejects it pre-write naming the field — context-linking is a capability that path does not have, not a param it drops.", Items: &kgtools.Property{Type: "string"}},
		"session":         {Type: "string", Description: "Session name to group the created node under via session--contains-->node — any single knowledge-graph create routes it, as does a thought create. Creates the session if new. create_batch rejects it pre-write naming the field — context-linking is a capability that path does not have, not a param it drops."},
		"ticket_id":       {Type: "string", Description: "Active ticket/project ID — born-linked as ticket--contains-->node so the created node is grouped under the work item that produced it; any single knowledge-graph create routes it. An unresolvable ticket_id is dropped with a warning, never blocking the write. create_batch rejects it pre-write naming the field — born-linking a batch is a capability that path does not have, not a param it drops."},
		"polarity":        {Type: "string", Description: "Charge polarity (mutate(create, type=charge) only). Must be 'positive' or 'negative'.", Enum: []string{"positive", "negative"}},
		"weight":          {Type: "number", Description: "Charge weight 1-10 (mutate(create, type=charge) only). Significance of the evidence."},
		"reasoning":       {Type: "string", Description: "Why this charge applies (mutate(create, type=charge) only)."},
		"charge_evidence": {Type: "array", Description: "Evidence node IDs backing the charge (mutate(create, type=charge) only). Renamed from `evidence` on the wire to avoid collision with the finding evidence field.", Items: &kgtools.Property{Type: "string"}},
		"thought_parent":  {Type: "string", Description: "Parent thought ID the charge attaches to (mutate(create, type=charge) only)."},
		// Negation-gate proof-of-work fields, read by InterceptNegationGate
		// (negation_gate.go) BEFORE the call reaches any write handler. Declared in
		// their own block so gofmt gives them their own alignment group rather than
		// re-aligning the thought/charge run above.
		"verified_quote": {Type: "string", Description: "Negation-gate proof of work — a TOP-LEVEL param on the call, NOT a metadata key and NOT edge_evidence. REQUIRED for negation-class calls: mutate(link, relationship:\"contradicts\") and mutate(update, status:\"invalidated\"). Must be a verbatim substring of the TARGET node's CURRENT source (whitespace-normalized before matching). Consumed by the gate before any write and never persisted; supplying it on a non-negation call is rejected."},
		"cited_range":    {Type: "string", Description: "Optional locality hint accompanying verified_quote on a negation-class call, as \"path/file.go:start-end\". When set, the verbatim substring must resolve to the cited path; when empty the gate checks existence and currency only. TOP-LEVEL param, consumed by the gate before any write and never persisted."},
	}
	maps.Copy(props, mutateBatchProperties())
	return props
}

// mutateDeclaredOperations is the mutate operation vocabulary, read from the
// live schema rather than transcribed — so unlike the hand-copied lists the
// other operation-dispatched tools keep, there is no second copy that can drift
// from what the tool advertises.
//
// A plain package-level var, not sync.OnceValue: mutateProperties() is a pure
// static map literal with no dependencies or I/O, so package-init evaluation
// can never observe a partial or stale value and there is nothing to defer.
// What the lazy form was protecting against still holds and is still
// satisfied — the property map is built ONCE, never per request. Calling
// mutateProperties() from inside the guard is the anti-pattern to avoid.
var mutateDeclaredOperations = mutateProperties()["operation"].Enum

// mutateOperationDeclared reports whether op is in the schema's operation enum.
func mutateOperationDeclared(op string) bool {
	return slices.Contains(mutateDeclaredOperations, op)
}

// mutateBatchProperties returns the array-of-object batch params whose element
// shapes are closed (additionalProperties:false) with described nested keys.
// Nested key Descriptions are lifted from each array's own Description prose —
// no new prose invented.
func mutateBatchProperties() map[string]kgtools.Property {
	return map[string]kgtools.Property{
		"references": {Type: "array", Description: "Citations for findings: [{url, title, summary}, {file, title, summary}, or {node_id, title}]", Items: &kgtools.Property{Type: "object", Description: "Reference object", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
			"url":     {Type: "string", Description: "URL of the cited source"},
			"title":   {Type: "string", Description: "Human-readable title of the citation"},
			"summary": {Type: "string", MaxLength: 500, Description: "Required search-optimized one-line summary of the cited reference, max 500 chars. NOT accepted on a node_id entry, which creates no node."},
			"file":    {Type: "string", Description: "File path cited (alternative to url)"},
			"node_id": {Type: "string", Description: "Knowledge node ID cited (alternative to url/file)"},
		}}},
		// THE DECLARED SET IS THE WHOLE SET THE DECODER HONORS, and the two are
		// checked against each other by the undeclared-key guard, which reads its
		// vocabulary off this map rather than a hand-listed copy — so this map is
		// the ONE place a per-item key is declared, and adding one here is what
		// moves it out of the refused set. `status` and `embed_identity` were
		// absent here while engine.batchItem and the proto UpdateItem carried
		// both: a caller reading this schema could not learn that a per-item
		// status write exists, and the guard would have refused a call the
		// compiler handles.
		"items": {Type: "array", Description: "For operation=update_batch: per-item array; each entry carries {id, summary, keywords, description, binary_vector (base64), metadata, status, embed_identity}. Single store.Txn wraps every item — all-or-nothing. Per-item validation mirrors single-item update (length checks on binary_vector, backend-tagged metadata rejection). An entry carrying any OTHER key is REFUSED naming it — an undeclared key is dropped at decode, so accepting it would return success having written none of it. Used by the client-side LLM pipeline for high-throughput writeback so per-batch RPC count stays at 1.", Items: &kgtools.Property{Type: "object", Description: "Per-item shape: {id (required), summary?, keywords?, description?, binary_vector? (base64 → 32 bytes), metadata?, status?, embed_identity?}", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
			"id":             {Type: "string", Description: "Target node ID (required)"},
			"summary":        {Type: "string", MaxLength: 500, Description: "Search-optimized one-line summary"},
			"keywords":       {Type: "string", Description: "BM25 keyword-token boost string"},
			"description":    {Type: "string", Description: "Node body to set (unset = untouched, present-and-empty = a deliberate clear). This is the per-item carrier for a plan section's body, so revising several sections of a chunked plan is one batch rather than one call each. It is BM25-indexed, so an item setting it is re-indexed."},
			"binary_vector":  {Type: "string", Description: "Base64-encoded binary embedding (32 bytes / 256-bit decoded)"},
			"metadata":       {Type: "object", Description: "Key-value metadata pairs merged per-key"},
			"status":         {Type: "string", Description: "Status to set on this item (nil = untouched)"},
			"embed_identity": {Type: "object", Description: "The embedder that produced binary_vector. A writeback under an identity the target graph did not record is refused rather than stored; unset on summary and metadata writes, which produce no vector."},
		}}},
		"nodes": {Type: "array", Description: "For operation=create_batch: per-node array; each entry carries {type, name, description, summary, content, status, metadata}. Created in a single store.Txn alongside the edges[] payload — all-or-nothing. Returns {ids:[...]} of length len(nodes). Not knowledge-graph-only: the arm runs on the practice graph as well, where source_hub groups the created nodes under their origin hub. It also honors a per-node id (which becomes the created node's id) and a per-node source, neither of which this item shape declares.", Items: &kgtools.Property{Type: "object", Description: "Per-node shape: {type (required), name, description, summary, content, status, metadata}", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
			"type":        {Type: "string", Description: "Node type (required)"},
			"name":        {Type: "string", Description: "Node name or title"},
			"description": {Type: "string", Description: "Node description"},
			"summary":     {Type: "string", MaxLength: 500, Description: "Search-optimized one-line summary"},
			"content":     {Type: "string", Description: "Full content body"},
			"status":      {Type: "string", Description: "Initial status"},
			"metadata":    {Type: "object", Description: "Initial key-value metadata pairs"},
		}}},
		"edges": {Type: "array", Description: "For operation=create_batch: per-edge array; each entry carries {from_idx, to_idx, from_id, to_id, type} plus the optional edge-metadata carriers {weight, confidence, method, evidence, last_validated}. An endpoint is either a slot index into nodes[] (from_idx/to_idx >= 0) OR an existing node ID (from_id/to_id). Use -1 / absent for the slot index when supplying an ID instead. Created atomically inside the same store.Txn as the nodes payload. ATTACHING A CRITERION TO A STEP TAKES A PAIR OF EDGES, not one: step--contains-->criterion AND criterion--verifies-->step, the same pair create_plan and mutate(create, type:criterion) both emit. plan_tree walks contains only, so a criterion attached by verifies alone is invisible in the rendered tree. A batch carrying one direction without its partner is REJECTED pre-write, naming the missing edge — the pair is never auto-completed.", Items: &kgtools.Property{Type: "object", Description: "Per-edge shape: {from_idx?, to_idx?, from_id?, to_id?, type (required), weight?, confidence?, method?, evidence?, last_validated?}", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
			"from_idx": {Type: "integer", Description: "Slot index into nodes[] for the edge source (-1/absent when using from_id)"},
			"to_idx":   {Type: "integer", Description: "Slot index into nodes[] for the edge target (-1/absent when using to_id)"},
			"from_id":  {Type: "string", Description: "Existing node ID for the edge source (alternative to from_idx)"},
			"to_id":    {Type: "string", Description: "Existing node ID for the edge target (alternative to to_idx)"},
			"type":     {Type: "string", Description: "Relationship type (required)"},
			// THE EDGE-METADATA CARRIERS THE ARM HAS ALWAYS ACCEPTED. engine.edgeBody
			// declares all five and TestCompileMutate_CreateBatchEdgeMetadata has
			// asserted since before this change that they land on the compiled edge;
			// they were simply undeclared here, so a caller reading the schema could
			// not learn they exist. Declaring them is what lets a caller write a
			// coherent plan_annotation attachment — which the coherence guard now
			// requires — from the documentation alone.
			"weight":         {Type: "number", Description: "Caller-asserted edge weight, stored verbatim."},
			"confidence":     {Type: "number", Description: "Caller-asserted 0.0-1.0 confidence, stored verbatim."},
			"method":         {Type: "string", Description: "Short tag describing how the edge was derived (e.g. 'manual', 'plan-section'). A relates-to edge from a plan_annotation MUST carry 'plan-annotation'."},
			"evidence":       {Type: "string", Description: "Evidence backing the edge (a file path, a snippet, a JSON payload). A relates-to edge from a plan_annotation MUST carry that annotation's kind and tier here, or the write is refused naming the exact value to send."},
			"last_validated": {Type: "string", Description: "RFC3339 timestamp the linker stamps when (re-)asserting the edge."},
		}}},
		"updates": {Type: "array", Description: "For operation=bulk_update_metadata: per-item array; each entry carries {id (required), metadata (required, non-empty)}. Single store.Txn wraps every item — all-or-nothing. Backend-tagged metadata rejects the whole batch. Used by client-side cluster persistence + propagation writeback so per-batch RPC count stays at 1 regardless of node count.", Items: &kgtools.Property{Type: "object", Description: "Per-item shape: {id (required), metadata (required, non-empty map)}", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
			"id":       {Type: "string", Description: "Target node ID (required)"},
			"metadata": {Type: "object", Description: "Key-value metadata pairs (required, non-empty)"},
		}}},
	}
}
