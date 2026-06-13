// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"maps"

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
			"operation=create_batch: create N nodes + M edges in one atomic store.Txn. Args: {nodes:[{type,name,description,summary,content,status,metadata}], edges:[{from_idx,to_idx,from_id,to_id,type}]}. Returns {ids:[...]} of length N. Knowledge-graph only. " +
			"operation=update: edit node fields or update status of one or more nodes. " +
			"operation=upsert: create-or-update a node by caller-supplied id, bypassing create-time validation guards (summary/name/duplicate-name) — intended for tool-owned config records (workers, log backends) where the caller already validated. Restricted to an explicit allowlist of types; not a general-purpose escape hatch. " +
			"operation=link: create a relationship between two nodes. " +
			"operation=unlink: remove a single directed edge keyed by from/to/relationship. Idempotent — returns success even if the edge did not exist. Use to retract a misattached pattern, fix a wrong informed-by, etc. " +
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
		"type":                  {Type: "string", Description: "Node type for create (finding, research, rule, criterion, resource, event, observation, memory, document)"},
		"id":                    {Type: "string", Description: "Target node ID for update or answer (operation=answer also accepts question_id as an alias)"},
		"ids":                   {Type: "array", Description: "List of node IDs for a batch update over PLAIN LOCAL NON-CONTAINER nodes — the same universal-scalar set_fields (e.g. status) are applied uniformly to every id. Tracker-backed nodes, container nodes (project/ticket/plan/phase/step — for status updates of ANY value), per-type params (command/scope/...), and source must be updated per-id; those batch shapes are rejected. For heterogeneous per-id bodies use update_batch.", Items: &kgtools.Property{Type: "string"}},
		"name":                  {Type: "string", Description: "Node name or title"},
		"description":           {Type: "string", Description: "Node description"},
		"summary":               {Type: "string", MaxLength: 500, Description: "Required search-optimized one-line summary, max 500 chars, when create-ing an embed-only-knowledge node type (NodeType.Summarizable()=false). Handler-side enforcement returns an error when missing/empty/whitespace or > 500 chars."},
		"content":               {Type: "string", Description: "Full content. For research create: context/background."},
		"status":                {Type: "string", Description: "Status to set (for update operation)"},
		"expand_to_descendants": {Type: "boolean", Description: "When updating status to 'completed' on a project/ticket/plan/phase, also walk the contains tree and write completed to every non-terminal descendant. Default true. Set false to update only the named node. This is a SINGLE-ID container path — a batch of container ids carrying a status (ids:[...]) is rejected, so issue container status updates per-id. Has no effect for non-completed statuses or non-container types."},
		"source":                {Type: "string", Description: "Source of the knowledge"},
		"evidence":              {Type: "string", Description: "Supporting evidence (for findings)"},
		"question_id":           {Type: "string", Description: "Research question node ID this finding answers"},
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
		"graph":                 {Type: "string", Description: "Target graph for the operation (default: knowledge). Use 'practice' with language param."},
		"language":              {Type: "string", Description: "Language for practice graph operations (e.g. 'go', 'python')"},
		"metadata":              {Type: "object", Description: "Arbitrary key-value metadata pairs (string→string). On create: sets the node's initial metadata map. On update: merged per-key into existing metadata — keys in the payload overwrite, absent keys are preserved. Retrievable via Node.Value(key)."},
		"keywords":              {Type: "string", Description: "Sets Node.Keywords (top-level struct field, NOT a metadata key). Powers the search BM25 keyword-token boost and the keywords display facet. Wired for the client-side LLM pipeline writeback path; carrying the value in metadata would land it in the inline map and bypass the search-side reader."},
		"binary_vector":         {Type: "string", Description: "Base64-encoded binary embedding to install on the node via PutBinaryVector. Decoded payload length must equal 32 bytes (256-bit). Used by the client-side LLM pipeline writeback path. Mismatched lengths return a structured validation error and no write is performed."},
		"confidence":            {Type: "number", Description: "Edge metadata (operation=link only). 0.0-1.0 caller-asserted confidence. Routed into store.Edge.Confidence via LinkBatch."},
		"method":                {Type: "string", Description: "Edge metadata (operation=link only). Short tag describing how the edge was derived (e.g. 'image-target', 'dockerfile-copy', 'manual'). Routed into store.Edge.Method."},
		"edge_evidence":         {Type: "string", Description: "Edge metadata (operation=link only). Caller-supplied evidence string (file path, snippet, URL) backing the edge. Renamed from `evidence` on the wire to avoid collision with the finding `evidence` field. Routed into store.Edge.Evidence."},
		"last_validated":        {Type: "string", Description: "Edge metadata (operation=link only). RFC3339 timestamp the linker stamps when (re-)asserting an edge. Routed into store.Edge.LastValidated."},
		"link_graph":            {Type: "string", Description: "Optional graph selector for operation=link (e.g. 'linkage' for the cross-graph linkage view). When set, the link is dispatched via store.LinkBatch against the named graph rather than the default knowledge graph."},
		"format":                {Type: "string", Description: "Output format: 'text' (default) or 'json' (structured per operation: create→{id, type, name, warnings}; link→{from, to, relationship}; answer→{id, name, conclusion}; update→{ids, fields}; delete→{deleted, total, ids})."},
		// thought / charge create payload fields. Carried by
		// the client-side InterceptThoughts when it translates
		// thoughts(operation:think|charge) into mutate(create,
		// type:thought|charge).
		"branches_from":   {Type: "string", Description: "Thought ID this branches from (mutate(create, type=thought) only). Adds an edge from the new thought to its parent for trace lineage."},
		"links":           {Type: "array", Description: "Node IDs to relate the new node to (finding/research/rule create, and thought create). Knowledge-graph IDs ride the atomic create as a node--relates-to-->target edge; code/cloud IDs are linked post-create via the cross-graph linkage. An unresolvable ID is dropped with a warning, never blocking the write.", Items: &kgtools.Property{Type: "string"}},
		"session":         {Type: "string", Description: "Session name to group the created node under via session--contains-->node (finding/research/rule create, and thought create). Creates the session if new."},
		"ticket_id":       {Type: "string", Description: "Active ticket/project ID — born-linked as ticket--contains-->node so a finding/research/rule/decision is grouped under the work item that produced it (finding/research/rule create). An unresolvable ticket_id is dropped with a warning, never blocking the write."},
		"polarity":        {Type: "string", Description: "Charge polarity (mutate(create, type=charge) only). Must be 'positive' or 'negative'.", Enum: []string{"positive", "negative"}},
		"weight":          {Type: "number", Description: "Charge weight 1-10 (mutate(create, type=charge) only). Significance of the evidence."},
		"reasoning":       {Type: "string", Description: "Why this charge applies (mutate(create, type=charge) only)."},
		"charge_evidence": {Type: "array", Description: "Evidence node IDs backing the charge (mutate(create, type=charge) only). Renamed from `evidence` on the wire to avoid collision with the finding evidence field.", Items: &kgtools.Property{Type: "string"}},
		"thought_parent":  {Type: "string", Description: "Parent thought ID the charge attaches to (mutate(create, type=charge) only)."},
	}
	maps.Copy(props, mutateBatchProperties())
	return props
}

// mutateBatchProperties returns the array-of-object batch params whose element
// shapes are closed (additionalProperties:false) with described nested keys.
// Nested key Descriptions are lifted from each array's own Description prose —
// no new prose invented.
func mutateBatchProperties() map[string]kgtools.Property {
	return map[string]kgtools.Property{
		"references": {Type: "array", Description: "Citations for findings: [{url, title}, {file, title}, or {node_id, title}]", Items: &kgtools.Property{Type: "object", Description: "Reference object", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
			"url":     {Type: "string", Description: "URL of the cited source"},
			"title":   {Type: "string", Description: "Human-readable title of the citation"},
			"file":    {Type: "string", Description: "File path cited (alternative to url)"},
			"node_id": {Type: "string", Description: "Knowledge node ID cited (alternative to url/file)"},
		}}},
		"items": {Type: "array", Description: "For operation=update_batch: per-item array; each entry carries {id, summary, keywords, binary_vector (base64), metadata}. Single store.Txn wraps every item — all-or-nothing. Per-item validation mirrors single-item update (length checks on binary_vector, backend-tagged metadata rejection). Used by the client-side LLM pipeline for high-throughput writeback so per-batch RPC count stays at 1.", Items: &kgtools.Property{Type: "object", Description: "Per-item shape: {id (required), summary?, keywords?, binary_vector? (base64 → 32 bytes), metadata?}", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
			"id":            {Type: "string", Description: "Target node ID (required)"},
			"summary":       {Type: "string", MaxLength: 500, Description: "Search-optimized one-line summary"},
			"keywords":      {Type: "string", Description: "BM25 keyword-token boost string"},
			"binary_vector": {Type: "string", Description: "Base64-encoded binary embedding (32 bytes / 256-bit decoded)"},
			"metadata":      {Type: "object", Description: "Key-value metadata pairs merged per-key"},
		}}},
		"nodes": {Type: "array", Description: "For operation=create_batch: per-node array; each entry carries {type, name, description, summary, content, status, metadata}. Created in a single store.Txn alongside the edges[] payload — all-or-nothing. Returns {ids:[...]} of length len(nodes). Knowledge-graph only.", Items: &kgtools.Property{Type: "object", Description: "Per-node shape: {type (required), name, description, summary, content, status, metadata}", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
			"type":        {Type: "string", Description: "Node type (required)"},
			"name":        {Type: "string", Description: "Node name or title"},
			"description": {Type: "string", Description: "Node description"},
			"summary":     {Type: "string", MaxLength: 500, Description: "Search-optimized one-line summary"},
			"content":     {Type: "string", Description: "Full content body"},
			"status":      {Type: "string", Description: "Initial status"},
			"metadata":    {Type: "object", Description: "Initial key-value metadata pairs"},
		}}},
		"edges": {Type: "array", Description: "For operation=create_batch: per-edge array; each entry carries {from_idx, to_idx, from_id, to_id, type}. An endpoint is either a slot index into nodes[] (from_idx/to_idx >= 0) OR an existing node ID (from_id/to_id). Use -1 / absent for the slot index when supplying an ID instead. Created atomically inside the same store.Txn as the nodes payload.", Items: &kgtools.Property{Type: "object", Description: "Per-edge shape: {from_idx?, to_idx?, from_id?, to_id?, type (required)}", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
			"from_idx": {Type: "integer", Description: "Slot index into nodes[] for the edge source (-1/absent when using from_id)"},
			"to_idx":   {Type: "integer", Description: "Slot index into nodes[] for the edge target (-1/absent when using to_id)"},
			"from_id":  {Type: "string", Description: "Existing node ID for the edge source (alternative to from_idx)"},
			"to_id":    {Type: "string", Description: "Existing node ID for the edge target (alternative to to_idx)"},
			"type":     {Type: "string", Description: "Relationship type (required)"},
		}}},
		"updates": {Type: "array", Description: "For operation=bulk_update_metadata: per-item array; each entry carries {id (required), metadata (required, non-empty)}. Single store.Txn wraps every item — all-or-nothing. Backend-tagged metadata rejects the whole batch. Used by client-side cluster persistence + propagation writeback so per-batch RPC count stays at 1 regardless of node count.", Items: &kgtools.Property{Type: "object", Description: "Per-item shape: {id (required), metadata (required, non-empty map)}", AdditionalProperties: &falseValue, Properties: map[string]kgtools.Property{
			"id":       {Type: "string", Description: "Target node ID (required)"},
			"metadata": {Type: "object", Description: "Key-value metadata pairs (required, non-empty)"},
		}}},
	}
}
