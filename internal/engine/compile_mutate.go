// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// mutateArgs is the compile-local view of the `mutate` tool's wire shape,
// covering the reducible operations (create/create_batch/update/update_batch/
// delete/link/unlink). It mirrors the mutate tool's declared wire shape
// (MutateToolDef, cmd/knowledge/internal/tools) for the fields the reducible
// path consumes, plus the create_batch nodes[]/edges[] and update_batch items[]
// sub-shapes.
type mutateArgs struct {
	Operation   string   `json:"operation"`
	Type        string   `json:"type"`
	ID          string   `json:"id"`
	IDs         []string `json:"ids"`
	Source      string   `json:"source"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Summary     string   `json:"summary"`
	Content     string   `json:"content"`
	// Status is a POINTER so an explicit status:"" (clear to blank) is
	// distinguishable from an absent status (leave untouched) — the same reason
	// batchItem.Status below is one. A plain string collapses the two and makes
	// a clear-to-blank request a silent no-op.
	Status       *string           `json:"status"`
	Keywords     string            `json:"keywords"`
	Metadata     map[string]string `json:"metadata"`
	From         string            `json:"from"`
	To           string            `json:"to"`
	Relationship string            `json:"relationship"`
	Graph        string            `json:"graph"`
	Language     string            `json:"language"`
	LinkGraph    string            `json:"link_graph"`
	Format       string            `json:"format"`
	// Repo/Account/Name route a batch write to the per-graph backing (code by
	// repo, cloud/cicd by account, logs/etc. by name). The update_batch arm
	// threads them onto the Execute Target so a code/cloud-graph write-back lands
	// in the right graph (the pipeline write-back's cross-graph routing).
	Repo    string `json:"repo"`
	Account string `json:"account"`
	// Branch carries the overlay dimension for a branch-overlay-resident batch
	// write. The update_batch arm threads it onto the Execute Target so an
	// overlay-resident pipeline write-back resolves the SAME overlay layer the gap
	// scan read from (resolveCode Scopes repo@branch). Empty → base graph.
	Branch string `json:"branch"`

	// create_batch payload.
	Nodes []nodeBody  `json:"nodes"`
	Edges []edgeBody  `json:"edges"`
	Items []batchItem `json:"items"` // update_batch payload.

	// Updates is the bulk_update_metadata payload: N per-item {id, metadata}
	// bodies. It is a metadata-only subset of the update_batch items[] shape and
	// lowers to the SAME MUTATION_KIND_UPDATE_ITEMS arm (compileMutateBulkMetadata).
	Updates []bulkUpdateItem `json:"updates"`

	// BundleID is an optional caller-supplied bundle identifier on create_batch,
	// mirroring the legacy mutateCreateBatchArgs.BundleID (tools_mutate_create_
	// batch.go:44) field name + semantics. It rides the MutationPlan.bundle_id
	// carrier so the engine wraps the mutation ctx via ContextWithBundleID (a
	// no-op when empty). Whether a create_batch routes through Execute or legacy,
	// the same bundle_id reaches the same ContextWithBundleID wrap.
	BundleID string `json:"bundle_id"`

	// Thought/charge create-arg signals — their PRESENCE forces ok=false on a
	// create (the engine CREATE arm is create_batch parity with NO thought/
	// charge fields; routing one through Execute would silently drop them).
	Polarity       string   `json:"polarity"`
	Weight         float64  `json:"weight"`
	BranchesFrom   string   `json:"branches_from"`
	ChargeEvidence []string `json:"charge_evidence"`
	ThoughtParent  string   `json:"thought_parent"`

	// Edge-metadata carrier (the LINK arm). Canonical wire json tags mirror the
	// edge-metadata params MutateToolDef declares (weight, confidence, method,
	// edge_evidence, last_validated), which the LLM already uses for edge
	// metadata on mutate(link). The existing Weight field (above,
	// json:"weight") is REUSED as the edge weight on the LINK arm — no second
	// weight-tagged field. compileMutateByIDLinkUnlink (compile_mutate_link.go)
	// threads these onto the EdgeSpec; the UNLINK arm leaves them zero.
	Confidence    float64 `json:"confidence"`
	Method        string  `json:"method"`
	EdgeEvidence  string  `json:"edge_evidence"`
	LastValidated string  `json:"last_validated"` // RFC3339 (accepts fractional seconds)
}

// nodeBody mirrors the create_batch nodes[] item MutateToolDef declares
// — the seven body fields plus id + source (the proto NodeBody field-8/field-9
// carriers the engine now honors). Keywords stays deliberately omitted (matching
// engine.proto's NodeBody, which mirrors nodeCreateItem — the client builds the
// set_fields / NodeBody JSON directly, never the server-side write-field type).
type nodeBody struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Summary     string            `json:"summary"`
	Content     string            `json:"content"`
	Status      string            `json:"status"`
	Metadata    map[string]string `json:"metadata"`
	// ID rides as the proto NodeBody.id carrier: empty → store auto-gen,
	// non-empty → honored verbatim by the engine CREATE/UPSERT decode.
	ID string `json:"id"`
	// Source rides as the proto NodeBody.source carrier, stored as-given. The
	// empty→'llm:claude' default is CLIENT policy (the intercept handlers stamp
	// it, e.g. buildFindingNode); the engine stores whatever rides this field.
	Source string `json:"source"`
}

// edgeBody mirrors the create_batch edges[] item MutateToolDef declares.
// from_idx/to_idx default to -1 (the "use the string ID" sentinel) when absent,
// matching the batch-edge build-carrier contract — see the UnmarshalJSON below.
type edgeBody struct {
	FromIdx int    `json:"from_idx"`
	ToIdx   int    `json:"to_idx"`
	FromID  string `json:"from_id"`
	ToID    string `json:"to_id"`
	Type    string `json:"type"`
	// Edge-metadata carriers (additive). Client-supplied INPUTS the engine stores
	// verbatim onto the batch edge (generic-litmus). last_validated arrives as an
	// RFC3339 string and rides the wire as int64 unix-nanos (parseLastValidated-
	// Nanos), matching the LINK-arm EdgeSpec carriers.
	Weight        float64 `json:"weight"`
	Confidence    float64 `json:"confidence"`
	Method        string  `json:"method"`
	Evidence      string  `json:"evidence"`
	LastValidated string  `json:"last_validated"` // RFC3339 (accepts fractional seconds)
}

// UnmarshalJSON treats absent from_idx/to_idx as -1 (the explicit "no slot ref,
// use the string ID" sentinel), which is the from_idx/to_idx default
// MutateToolDef declares. Without this, the Go zero value 0 would collide with
// slot index 0.
func (e *edgeBody) UnmarshalJSON(data []byte) error {
	type raw struct {
		FromIdx       *int    `json:"from_idx"`
		ToIdx         *int    `json:"to_idx"`
		FromID        string  `json:"from_id"`
		ToID          string  `json:"to_id"`
		Type          string  `json:"type"`
		Weight        float64 `json:"weight"`
		Confidence    float64 `json:"confidence"`
		Method        string  `json:"method"`
		Evidence      string  `json:"evidence"`
		LastValidated string  `json:"last_validated"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	e.FromIdx, e.ToIdx = -1, -1
	if r.FromIdx != nil {
		e.FromIdx = *r.FromIdx
	}
	if r.ToIdx != nil {
		e.ToIdx = *r.ToIdx
	}
	e.FromID, e.ToID, e.Type = r.FromID, r.ToID, r.Type
	// Edge metadata carries straight through — no sentinel semantics like the
	// from_idx/to_idx -1 default.
	e.Weight, e.Confidence = r.Weight, r.Confidence
	e.Method, e.Evidence, e.LastValidated = r.Method, r.Evidence, r.LastValidated
	return nil
}

// batchItem mirrors the update_batch items[] shape MutateToolDef declares
// (id, summary, keywords, binary_vector, metadata). The pointer fields preserve
// the set/unset distinction. Heterogeneous per-item bodies (summary/keywords/binary_vector/
// metadata/status) are expressible — compileMutateUpdateBatch lowers them
// onto the MUTATION_KIND_UPDATE_ITEMS arm (each batchItem → a distinct proto
// UpdateItem).
// EmbedIdentity states what BinaryVector's bytes ARE. It rides the same
// per-item body because it is a claim ABOUT that item's vector: an identity on
// an item carrying no vector claims nothing and is ignored server-side.
type batchItem struct {
	ID       string  `json:"id"`
	Summary  *string `json:"summary"`
	Keywords *string `json:"keywords"`
	// Description carries the node BODY, under the same set/unset pointer
	// contract as its siblings. It was the one field a caller could name here
	// and lose: with no proto carrier the key died at json.Unmarshal, the item
	// compiled to the id alone, and the call returned success having written
	// none of it.
	Description   *string                    `json:"description"`
	BinaryVector  []byte                     `json:"binary_vector"`
	Metadata      map[string]string          `json:"metadata"`
	Status        *string                    `json:"status"`
	EmbedIdentity *knowledgev1.EmbedIdentity `json:"embed_identity"`
}

// compileMutate translates a reducible `mutate` op into a MutationPlan. Returns
// ok=false (default-deny → legacy) for graph=practice/checks, cross-graph
// practice link, thought/charge creates, heterogeneous update_batch, and the
// non-reducible ops (upsert/bulk_update_metadata/answer/prune-by-age).
func compileMutate(args json.RawMessage) (*knowledgev1.ExecuteRequest, bool) {
	var a mutateArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, false
	}

	// The cross-graph practice link (link_graph set) is SPECIALIZED
	// (handleClientCrossGraphLink — proxy creation, legacy). The engine
	// targets a single graph; the cross-graph LINK is its own surface, so it
	// stays the residual deny. A practice/checks create/update/delete/link/
	// upsert with NO link_graph now compiles to a Target-routed MutationPlan
	// (Target.Graph == the requested graph, Target.Language == a.Language) via
	// the op switch below + mutationRequest/buildTarget — the engine is a generic
	// single-graph parametric mutator (1ad493da), so practice/checks need
	// no special server arm.
	if a.LinkGraph != "" {
		return nil, false
	}

	switch a.Operation {
	case "create", "create_batch":
		return compileMutateCreate(a)
	case "upsert":
		return compileMutateUpsert(a)
	case "update":
		return compileMutateByIDUpdate(a)
	case "update_batch":
		return compileMutateUpdateBatch(a)
	case "bulk_update_metadata":
		return compileMutateBulkMetadata(a)
	case "delete":
		// Both by-ids and id-less prune-by-age lower through the ONE shared
		// compileDelete (the standalone `delete` tool routes there too — compile.go
		// switch). Pass the raw args so the by-ids / prune-by-age discriminant +
		// the older_than/session_id/dry_run carriers are read uniformly.
		return compileDelete(args)
	case "link", "unlink":
		return compileMutateByIDLinkUnlink(a)
	default:
		// answer / prune-by-age → legacy.
		//
		// (update_batch + bulk_update_metadata are lowered to
		// MUTATION_KIND_UPDATE_ITEMS in the sibling compile_mutate_batch.go; upsert
		// is lowered to MUTATION_KIND_UPSERT in compileMutateUpsert above.) The rest
		// (answer / prune-by-age) are SPECIALIZED and fall
		// through here.
		return nil, false
	}
}

// compileMutateByIDUpdate lowers a by-id update (operation=update, id=X OR
// ids=[X,Y,...]) into a UPDATE MutationPlan with Selection.Ids carrying the full
// target id-set — the by-id WRITE selector T2.4c added. The scalar set fields
// (status/name/description/summary/content) ride as set_fields applied uniformly
// to every id by the server's applyUpdateOverSet; metadata rides as set_metadata
// (the engine merges per-key). The engine validates the field keys + the
// backend-tag guard at decode. This arm is contract-BLIND: it reduces whatever
// id-set survives, regardless of node type or backing — the intercept's
// guardBatchUpdateShape gate is what restricts WHICH ids[] batches reach this
// fall-through (backend-backed / per-type-param / source / container-status are
// rejected before it). A by-id update needs at least one id; without any (and
// without items[] — that is update_batch, handled in the default arm) it falls
// through to legacy.
func compileMutateByIDUpdate(a mutateArgs) (*knowledgev1.ExecuteRequest, bool) {
	// Build the target id-set: the singular id folds into the set; the plural
	// ids[] batch rides whole. The update_batch shape (items[]) is the
	// heterogeneous UPDATE_ITEMS arm handled separately → legacy.
	ids := a.IDs
	if a.ID != "" {
		ids = []string{a.ID}
	}
	if len(ids) == 0 || len(a.Items) > 0 {
		return nil, false // no id, or update_batch shape → legacy.
	}
	sf := updateSetFields(a)
	if len(sf) == 0 && len(a.Metadata) == 0 {
		// A by-id update with NO payload (nothing to set) is a degenerate shape;
		// leave it to the legacy handler so its "nothing to update" validation
		// path is preserved (equivalence). The reducible by-id update needs at
		// least one set field or metadata key. Covers the ids[]-with-no-payload
		// case too (correctly legacy).
		return nil, false
	}
	plan := &knowledgev1.MutationPlan{
		Kind:      knowledgev1.MutationPlan_MUTATION_KIND_UPDATE,
		Selection: &knowledgev1.Selection{Ids: ids},
	}
	if len(sf) > 0 {
		plan.SetFields = sf
	}
	if len(a.Metadata) > 0 {
		plan.SetMetadata = a.Metadata
	}
	return mutationRequest(plan, a), true
}

// updateSetFields collects the populated scalar update fields into the
// set_fields map the engine UPDATE arm consumes. Only non-empty fields ride —
// an absent field is not in the map, so the engine leaves it unchanged (the
// per-key UPDATE semantics). Keys match setFieldsToNodeFields' allowlist
// (name/description/summary/content/status/source/keywords) — source + keywords
// were previously declared in the schema but dropped on update; both now route.
func updateSetFields(a mutateArgs) map[string]string {
	set := map[string]string{}
	// Status keys on PRESENCE, not non-emptiness: an explicit status:"" clears
	// the node to blank, which is a write, while an absent status leaves it
	// untouched. Every other field below is a plain string and cannot tell the
	// two apart, so they stay on the non-empty gate.
	if a.Status != nil {
		set["status"] = *a.Status
	}
	if a.Name != "" {
		set["name"] = a.Name
	}
	if a.Description != "" {
		set["description"] = a.Description
	}
	if a.Summary != "" {
		set["summary"] = a.Summary
	}
	if a.Content != "" {
		set["content"] = a.Content
	}
	if a.Keywords != "" {
		set["keywords"] = a.Keywords
	}
	if a.Source != "" {
		set["source"] = a.Source
	}
	return set
}

// compileMutateCreate lowers create / create_batch into a CREATE MutationPlan.
// thought/charge creates are NO LONGER denied: the client
// composers (handleThinkClient / handleChargeClient) lower think/charge into a
// GENERIC create_batch carrying explicit type=thought/charge NodeBodies + the
// EdgeChargedBy / EdgeRelatesTo / session-lineage edges, so a type:thought|charge
// create_batch (and a direct LLM mutate(create,type:thought)) compiles to
// MUTATION_KIND_CREATE here. The old thought/charge fold-in fields
// (polarity/weight/branches_from/charge_evidence/thought_parent) are composed
// client-side into the node Metadata + edges, not dropped.
func compileMutateCreate(a mutateArgs) (*knowledgev1.ExecuteRequest, bool) {
	bodies, edges, ok := createPayload(a)
	if !ok {
		return nil, false // empty batch / missing type / unparseable last_validated → legacy.
	}

	plan := &knowledgev1.MutationPlan{
		Kind:       knowledgev1.MutationPlan_MUTATION_KIND_CREATE,
		NodeBodies: bodies,
		Edges:      edges,
		// bundle_id rides the wire to the engine ctx-wrap (Phase 3); empty is the
		// no-bundle case. Only create_batch carries it, mirroring the legacy field.
		BundleId: a.BundleID,
	}
	return mutationRequest(plan, a), true
}

// compileMutateUpsert lowers a mutate(upsert) into a one-body
// MUTATION_KIND_UPSERT plan. Both id (the upsert key) and type are required —
// a missing one returns (nil,false) so the shape is denied rather than compiled.
// The single body carries id + type + source + the seven body fields via
// nodeBodyToProto (which carries Id+Source). The engine UPSERT arm is type-blind;
// the caller owns body validation (the precheck path does not gate upsert).
//
// This arm is reached by (a) the LLM-facing mutate(operation:upsert) tool
// dispatch and (b) the client proxy branch, which calls executeMutate →
// engine.Compile → here.
// derefStatus reads a presence-bearing status for the CREATE-shaped paths,
// which carry a plain string: there is no node to leave untouched on a create,
// so an absent status and an explicit blank one mean the same thing.
func derefStatus(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func compileMutateUpsert(a mutateArgs) (*knowledgev1.ExecuteRequest, bool) {
	if a.ID == "" || a.Type == "" {
		return nil, false // missing upsert key or type → deny.
	}
	plan := &knowledgev1.MutationPlan{
		Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPSERT,
		NodeBodies: []*knowledgev1.NodeBody{nodeBodyToProto(nodeBody{
			Type:        a.Type,
			Name:        a.Name,
			Description: a.Description,
			Summary:     a.Summary,
			Content:     a.Content,
			Status:      derefStatus(a.Status),
			Metadata:    a.Metadata,
			ID:          a.ID,
			Source:      a.Source,
		})},
	}
	return mutationRequest(plan, a), true
}

// nodeBodyToProto maps a compile-local nodeBody onto the proto NodeBody (the
// 7-field create_batch payload).
func nodeBodyToProto(n nodeBody) *knowledgev1.NodeBody {
	return &knowledgev1.NodeBody{
		Type:        n.Type,
		Name:        n.Name,
		Description: n.Description,
		Summary:     n.Summary,
		Content:     n.Content,
		Status:      n.Status,
		Metadata:    n.Metadata,
		Id:          n.ID,
		Source:      n.Source,
	}
}

// mutationRequest wraps a MutationPlan in an ExecuteRequest with the target
// graph selector. An empty graph targets the knowledge graph (the engine's
// graph=="" default); a practice/checks graph routes the mutation to that
// graph via buildTarget (the guard now denies only the cross-graph link_graph
// case upstream, so practice/checks ops reach here Target-routed).
//
// Repo/Account are threaded onto the Target so a named-graph write routes to the
// right per-graph backing: the server resolves graph=code by Target.Repo and
// graph=cloud/cicd by Target.Account (ResolveGraphDB, tools_graph_routing.go).
// Without these a mutate(create_batch, graph:"cloud", account:"aws-123") would
// land Account-less and the server would reject it with "graph=cloud requires
// account" — the postpopulate wire writes depend on this.
//
// The `name` param is threaded PER FAMILY (mutateTargetName) rather than
// verbatim, because on the mutate surface one JSON key carries two meanings. For
// an LLM caller `name` is the NODE name ("Node name or title", mutate_schema.go);
// for the pipeline write-back it is the graph INSTANCE key that
// graphsel.ApplyInstanceKey assigned. Copying it verbatim served the second
// meaning and, for every family that does not address an instance by name, asked
// the server for a graph named after the node being written.
//
// The server used to discard sel.Name on the knowledge family, so the mis-mapping
// was invisible for as long as it existed; once validateGraphSelector began
// rejecting an unconsumed selector field, every knowledge-family mutate carrying
// a node name — a typed create, a criterion description update (whose name is
// DERIVED from the description), a log-backend upsert — failed with
// "graph=knowledge holds ONE graph: name= is a label, not a selector".

func mutationRequest(plan *knowledgev1.MutationPlan, a mutateArgs) *knowledgev1.ExecuteRequest {
	return &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Mutation{Mutation: plan},
		Target: mutateTarget(a.Graph, a.Repo, a.Account, a.Name, a.Language, ""),
	}
}
