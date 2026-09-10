// SPDX-License-Identifier: Apache-2.0

package engine

import knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

// compile_query_plan.go — the SELECTION BUILDERS and the plan-field appliers the
// query compiler's read shapes are assembled from. Split out of
// compile_query.go when that file reached this repository's per-file size
// budget; the arg shape, the reducibility gates and the shape dispatch stay
// there.
//
// EVERY FUNCTION HERE IS A SETTER RATHER THAN A DECISION. Which shape a read
// reduces to is decided next door; these turn a decided shape into the proto
// plan, and each one states the property its arm depends on — a browse's
// default cap, a keyset cursor that is PRESENT-and-empty rather than absent, a
// skip-total opt-in only a drain sets.

// browseSelection builds the Selection for a type-browse / meta-only plan:
// node_type + statuses + metadata predicates + universal-field predicates. The
// meta map lowers to MetadataPredicate exactly as the engine expects (value "*"
// → OP_EXISTS, else OP_EQ — mirroring store.Meta's "*" sentinel; the engine
// consumes these predicates, the client does NOT canonicalize). The fields slice
// lowers to FieldPredicate (e.g. a symbol_name EQ filter the engine evaluates
// server-side via nodeMatchesField).
func browseSelection(nodeType, status string, meta map[string]string, fields []fieldPredicateArg) *knowledgev1.Selection {
	sel := &knowledgev1.Selection{NodeType: nodeType}
	if status != "" {
		sel.Statuses = []string{status}
	}
	sel.MetadataPredicates = lowerMetaPredicates(meta)
	sel.FieldPredicates = lowerFieldPredicates(fields)
	return sel
}

// browsePluralSelection builds the Selection for a plural-types browse: NodeType
// stays EMPTY and NodeTypes carries the set, which the engine lowers into the
// store's type selection so rows AND Total are filtered in one pass. status +
// metadata predicates ride exactly as browseSelection sets them. DISTINCT from
// browseSelection's singular NodeType (a single index browse).
func browsePluralSelection(nodeTypes []string, status string, meta map[string]string, fields []fieldPredicateArg) *knowledgev1.Selection {
	sel := &knowledgev1.Selection{NodeTypes: nodeTypes}
	if status != "" {
		sel.Statuses = []string{status}
	}
	sel.MetadataPredicates = lowerMetaPredicates(meta)
	sel.FieldPredicates = lowerFieldPredicates(fields)
	return sel
}

// lowerMetaPredicates maps the meta equality map onto the proto MetadataPredicate
// vocabulary: "*" → OP_EXISTS, any other value → OP_EQ. Returns nil for an empty
// map. The server decodes these wire ops as OP_EQ → store.Meta(k, v) (the
// exact-match map) and OP_EXISTS → store.MetaPred(MetaOpExists) (the key-presence
// predicate both backends render) — see the MetadataPredicate message
// (engine.proto:395-421) and applyMetadataPredicates server-side.
func lowerMetaPredicates(meta map[string]string) []*knowledgev1.MetadataPredicate {
	if len(meta) == 0 {
		return nil
	}
	preds := make([]*knowledgev1.MetadataPredicate, 0, len(meta))
	for k, v := range meta {
		p := &knowledgev1.MetadataPredicate{Key: k}
		if v == "*" {
			p.Op = knowledgev1.MetadataPredicate_OP_EXISTS
		} else {
			p.Op = knowledgev1.MetadataPredicate_OP_EQ
			p.Value = v
		}
		preds = append(preds, p)
	}
	return preds
}

// lowerFieldPredicates maps {field,op,value} args onto the proto FieldPredicate
// vocabulary, the twin of lowerMetaPredicates for universal struct fields.
// FieldPredicate reuses MetadataPredicate.Op (engine.proto), so "eq" → OP_EQ and
// "exists" → OP_EXISTS for parity with lowerMetaPredicates. Returns nil for an
// empty slice. The engine evaluates each predicate server-side via
// nodeMatchesField (e.g. symbol_name EQ → exact-match the session name).
func lowerFieldPredicates(fields []fieldPredicateArg) []*knowledgev1.FieldPredicate {
	if len(fields) == 0 {
		return nil
	}
	preds := make([]*knowledgev1.FieldPredicate, 0, len(fields))
	for _, f := range fields {
		p := &knowledgev1.FieldPredicate{Field: f.Field, Value: f.Value}
		switch f.Op {
		case "exists":
			p.Op = knowledgev1.MetadataPredicate_OP_EXISTS
		default:
			p.Op = knowledgev1.MetadataPredicate_OP_EQ
		}
		preds = append(preds, p)
	}
	return preds
}

// applyQueryVec decodes a base64 query_vector into one QueryVecs entry. The
// engine validates the 32-byte length; a malformed base64 is left unset so the
// plan still runs the text query (the engine falls back to embedding it).
func applyQueryVec(p *knowledgev1.QueryPlan, queryVector string) {
	if queryVector == "" {
		return
	}
	raw, err := base64Decode(queryVector)
	if err != nil {
		return
	}
	p.QueryVecs = [][]byte{raw}
}

// applyLimitOffset sets Limit/Offset only when supplied. Used by the SEARCH
// arms (text / recent): this helper injects no default of its own, so a plan
// carrying limit==0 reaches Execute uncapped by it. The knowledge-graph search
// arms take their fallback top-k from knowledgeSearchDefaultLimit in the tools
// package before they reach here; a second default injected in this helper
// could fight the over-fetch plan.
func applyLimitOffset(p *knowledgev1.QueryPlan, limit, offset int) {
	if limit > 0 {
		p.Limit = int32(limit)
	}
	if offset > 0 {
		p.Offset = int32(offset)
	}
}

// applyBrowseLimitOffset is the BROWSE twin of applyLimitOffset: it applies the
// client-side friendly default (browseDefaultLimit=10) when the caller supplied
// no positive limit, then sets Offset. Used by the three query-tool browse arms
// (type-browse, plural-types browse, meta-only browse).
//
// A browse plan does NOT route through the compositor's self-default,
// and the server no longer injects a default (it honors limit==0 = no cap). So
// without this client default a no-limit LLM browse would send limit==0 →
// unbounded → the whole graph. This helper is the load-bearing guard that keeps
// the user-facing browse capped at 10 after the server-side default-injection
// was removed. An explicit positive limit overrides the default verbatim.
func applyBrowseLimitOffset(p *knowledgev1.QueryPlan, limit, offset int) {
	if limit > 0 {
		p.Limit = int32(limit)
	} else {
		p.Limit = browseDefaultLimit
	}
	if offset > 0 {
		p.Offset = int32(offset)
	}
}

// applyAfterID threads the id-keyset cursor onto a browse plan, PRESERVING
// PRESENCE: a non-nil cursor is copied through even when its value is the empty
// string, because that is page 1 of a keyset drain and the plan field is
// `optional` precisely so the backend can tell it from a browse that never asked
// for a keyset. Nil leaves the plan on the ordinary offset paging paths. It never
// touches Offset — the two are mutually exclusive and the server rejects a plan
// carrying both.
func applyAfterID(p *knowledgev1.QueryPlan, afterID *string) {
	if afterID == nil {
		return
	}
	cursor := *afterID
	p.AfterId = &cursor
}

// applyTombstones threads the include_tombstones opt-in onto the plan.
func applyTombstones(p *knowledgev1.QueryPlan, include bool) {
	if include {
		p.IncludeTombstones = true
	}
}

// applyContentB64 threads the content_b64 opt-in onto the plan (binary-safe
// NodeList Content carrier). Set only when requested so non-binary browses pay
// no base64 cost; the caller decodes via DecodeNodesContentB64.
func applyContentB64(p *knowledgev1.QueryPlan, contentB64 bool) {
	if contentB64 {
		p.ContentB64 = true
	}
}

// applySkipTotal threads the skip_total opt-in onto the plan. Set only when the
// caller discards Total (the paged reflection drain), so the single-layer
// executor skips the paginating COUNT; user-facing browses leave it false and
// keep their exact Total.
func applySkipTotal(p *knowledgev1.QueryPlan, skipTotal bool) {
	if skipTotal {
		p.SkipTotal = true
	}
}

// boolPtr dereferences a tri-state *bool, treating nil as false.
func boolPtr(b *bool) bool { return b != nil && *b }
