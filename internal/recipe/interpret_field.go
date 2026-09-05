// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// interpret_field.go — the FIELD READERS: everything that turns a dotted path
// into a string, for every carrier a row can read through.
//
// It was split out of interpret_expr.go, which measured 498 lines against the
// 500-line per-file hard cap lefthook.yml enforces on every staged .go file and
// had no room for the edge reader below. The three readers moved here whole —
// evalField, evalVarField and readNodeField — and nothing else moved with them.
//
// THE CARRIERS ARE DIFFERENT MAPS WRITTEN BY DIFFERENT EMITTERS, which is why
// this file has separate readers rather than one. A node's Metadata is stamped
// by a collector onto a node; an edge's Evidence is stamped by that same
// collector onto an edge, as a flat JSON string map; and a `$var` binding holds
// a scalar the graph never carried at all.

// evalField dispatches on the first path segment: bare identifiers
// (e.g. `node.type`, `section.name`) read from the current row's
// hydrated Node; `edge` reads the edge this row was traversed along;
// leading "$var" reads from a per-row binding, then
// falls back to env. The second and later segments address either a
// well-known Node field (type/symbol_name/name/summary/description/
// content/source/status) or a metadata key.
//
// Unknown paths return "" — consistent with Node.Value's own
// soft-miss behavior and the recipe DSL's fail-soft field reads.
func evalField(env *Env, row *Row, path []string) (string, error) {
	if len(path) == 0 {
		return "", fmt.Errorf("empty field path")
	}
	head := path[0]
	if strings.HasPrefix(head, "$") {
		return evalVarField(env, row, head[1:], path[1:]), nil
	}
	// `edge` names a DIFFERENT CARRIER from every other bare head: the edge the
	// row was reached along rather than the node the row is. It is dispatched
	// before the node read for that reason, and its head legality is settled at
	// parse time — a traverse adds it to the legal set and a select resets it
	// away — so a select-scoped `edge.…` never reaches here.
	if head == edgeHead {
		if row == nil {
			return "", nil
		}
		return readEdgeField(row.Edge, path[1:])
	}
	// Bare head: treat as a reference to the current row's Node.
	// Single-segment paths like `section` by itself return the node's
	// SymbolName — this is the equivalent of "the row's name".
	if row == nil {
		return "", nil
	}
	if len(path) == 1 {
		return row.Node.SymbolName, nil
	}
	// A ROW-SCOPED PSEUDO-VARIABLE wins over the node read: group_by stashes
	// its key list on the representative row under a literal dotted name, and
	// without this the read falls through to the node and returns a metadata
	// key that does not exist, so the documented accessor yielded nothing.
	//
	// The guards before the join are load-bearing rather than defensive. This
	// is the hottest path in the interpreter, so joining unconditionally would
	// allocate on every dotted read to service a lookup that almost always
	// misses; a single-segment head cannot be a dotted name and never reaches
	// the join. Only the ROW's Vars are consulted — the environment would let a
	// stale global shadow a row-scoped read.
	if len(row.Vars) > 0 && len(path) >= 2 {
		if v, ok := row.Vars[strings.Join(path, ".")]; ok {
			return v, nil
		}
	}
	return readNodeField(row.Node, path[1:]), nil
}

// evalVarField resolves "$name.<rest>" where rest addresses fields on
// the variable's bound value. Because v1 binds only strings (not full
// node handles), a var-rooted field path with a non-empty rest is
// treated as metadata access on the per-row bound Node IF the path
// head matches a traverse-as binding that stashed the hydrated Node.
// Otherwise the bare var value is returned and the rest is ignored.
func evalVarField(env *Env, row *Row, name string, rest []string) string {
	value := lookupVar(env, row, name)
	if len(rest) == 0 {
		return value
	}
	// Traverse-as rows stash the bound Node under a special per-row
	// key "<var>:node". When present, honor field-dotted access.
	if row != nil && row.Vars != nil {
		if nodeID, ok := row.Vars[name]; ok && nodeID != "" {
			// Best-effort: we only have scalar bindings, so the full
			// hydrated node isn't here. Fall through to returning the
			// raw value — interpreter rules that need the hydrated
			// node use a different accessor.
			return nodeID
		}
	}
	return value
}

// readNodeField returns the value for a dotted path rooted on a Node.
// Supported first segments: type, symbol_name, name (alias for
// symbol_name), summary, description, content, source, status, body.
// Everything else is treated as a metadata key or, if the segment is
// literally "metadata", consumes the next segment as the metadata key
// so `node.metadata.kind` reads kgtypes.Value(n, "kind").
//
// `body` is VIRTUAL: no collector writes a metadata key by that name, so it
// shadows nothing. It exists because a node's text does not always live in the
// same field — a LEAF on either raw collector carries its text in Content,
// while a page or document ROOT carries a flattened body or a metadata summary
// in Description. Coalescing Content then Description gives one field name that
// reaches the text wherever it sits, and it deliberately does NOT branch on the
// collector: a source check would silently return nothing for any future
// emitter, where the coalesce is total. A recipe that genuinely needs to tell
// the sources apart still has the source field.
//
// THE SOURCES AGREE ABOUT SECTIONS AGAIN, and the coalesce reaches the heading
// on either. A pdf section carries its heading in Content as well as in
// SymbolName — a pdf section's own searchable text IS its heading — and the
// web collector's emitSection writes Content: sec.Heading too, so body returns
// the heading on a section from either collector. Section
// BODIES on both sources still come from subtree_concat over the children,
// never from body on the section itself, so a recipe wanting leaf text only
// selects leaves; concatenating a pdf subtree interleaves each nested heading
// with the prose beneath it.
func readNodeField(n *knowledgev1.Node, rest []string) string {
	if len(rest) == 0 {
		return n.SymbolName
	}
	head := rest[0]
	switch head {
	case "type":
		return n.Type
	case "symbol_name", "name":
		return n.SymbolName
	case "summary":
		return n.Summary
	case "description":
		return n.Description
	case "content":
		return n.Content
	case "source":
		return n.Source
	case "status":
		return n.Status
	case "id":
		return n.Id
	case "body":
		if n.Content != "" {
			return n.Content
		}
		return n.Description
	case "metadata":
		if len(rest) < 2 {
			return ""
		}
		return kgtypes.Value(n, rest[1])
	default:
		return kgtypes.Value(n, head)
	}
}

// edgeHead is the bare field-path head naming the edge a row was traversed
// along. It is not a node type and never can be: parser_heads.go admits it to
// the legal set on a traverse and drops it on a select.
const edgeHead = "edge"

// edgeEvidenceKey is the explicit spelling that consumes the NEXT segment as an
// Evidence key, mirroring `metadata` on the node reader. `edge.rel` and
// `edge.evidence.rel` therefore read the same value; the explicit form exists so
// an author can name a key that collides with a well-known edge field.
const edgeEvidenceKey = "evidence"

// wellKnownEdgeFields are the field-path tails readEdgeField answers from the
// Edge struct itself rather than from Evidence, in the shape
// wellKnownNodeFields takes for nodes.
//
// THE ACCESSOR IS `edge.type`, NEVER `edge.rel`. `rel` is a REAL Evidence key
// the web collector stamps on link edges, with the values "internal" and
// "external", so spelling the edge's own type `rel` would put two values from
// two different authorities one word apart. Under this spelling there is no
// collision: `edge.type` is the edge's own type, and `edge.rel` falls through
// the sugar below to the Evidence key of that name, which is what an author
// writing it on a web graph means.
var wellKnownEdgeFields = map[string]bool{"type": true}

// edgeEvidenceMap decodes an edge's Evidence as the flat JSON string map both
// raw collectors write it as.
//
// A NIL RETURN IS A SOFT MISS, NOT AN ERROR, and that is a property of the
// CORPUS rather than a fail-soft convenience. The web collector writes Evidence
// through jsonMeta as a flat string map — a position payload on contains edges,
// a rel/url payload on link edges — and the contains-edge position lineage.go
// decodes has the same shape. But other graphs write OPAQUE strings on the very
// same field: the treesitter collector's `import:` form in chunker.go and its
// `flow:` form in types.go. An opaque Evidence carries no keys at all; it is not
// a malformed map, and refusing it would refuse every edge of those graphs.
func edgeEvidenceMap(evidence string) map[string]string {
	if evidence == "" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(evidence), &out); err != nil {
		return nil
	}
	return out
}

// readEdgeField returns the value for a dotted path rooted on the edge a row was
// traversed along.
//
// Three spellings, in dispatch order: `type` reads the edge's own type;
// `evidence` consumes the NEXT segment as an Evidence key; and any other head is
// itself the Evidence key — the same sugar readNodeField gives a metadata key,
// and the same shape the help documents for `page.url`.
//
// A NIL EDGE IS A VALIDATOR BUG, NOT A SOFT MISS, and the difference from the
// key case below is the whole point. The parse-time head rules admit `edge` only
// where a rule stamped one: a select RESETS the head away, so a select-scoped
// read is refused at parse, and every rule that keeps the head legal — traverse,
// walk, and the where-tree walk leaves — stamps the edge it stepped along.
// Reaching this branch therefore means a rule admitted the head WITHOUT stamping,
// which no recipe can cause and only a code change can.
//
// AN EARLIER VERSION READ EMPTY HERE, and that soft return is what made exactly
// such a defect silent: a rule landed that admitted the head and stamped no edge,
// and every `edge.…` read under it answered "" instead of saying so. An
// unreachable soft path cannot serve a caller; it can only absorb the next
// regression. The three sibling precedents in this package take the same line —
// evalCompareLeaf's unresolved-leaf error, evalMatchesLeaf's uncompiled-regex
// error, and compareOrdered's default arm.
//
// A KEY ABSENT FROM A REAL EDGE'S EVIDENCE STILL READS EMPTY. That is the
// FALSE-PREDICATE half of the absent-value rule and it is untouched: a nil EDGE
// and a missing KEY are different questions. Its bad-input counterpart is the
// edge-evidence census in validate_source_fields.go, which refuses a key the
// source graph never carried, before the walk.
//
// THE DECODE IS NOT MEMOIZED, on either carrier. Not on the Row, because the
// blob is decoded at most once per leaf per row and a cache would add a field
// whose invalidation has no owner. And NOT ON THE EDGE, because the edge belongs
// to the cached, shared source view: per-run state written onto shared structure
// is exactly the class this package already removed once.
func readEdgeField(e *knowledgev1.Edge, rest []string) (string, error) {
	if len(rest) == 0 {
		return "", fmt.Errorf(
			"recipe: %q names no edge attribute — read %s.type, %s.%s.<key>, or %s.<key> for an evidence key",
			edgeHead, edgeHead, edgeHead, edgeEvidenceKey, edgeHead)
	}
	if e == nil {
		return "", fmt.Errorf(
			"recipe: %s.%s reached a row that walked no edge, which is a validator bug: "+
				"every rule that admits the %s head must stamp the edge it stepped along",
			edgeHead, strings.Join(rest, "."), edgeHead)
	}
	head := rest[0]
	if wellKnownEdgeFields[head] {
		return e.Type, nil
	}
	key := head
	if head == edgeEvidenceKey {
		if len(rest) < 2 {
			return "", fmt.Errorf(
				"recipe: %s.%s names no key — read %s.%s.<key>",
				edgeHead, edgeEvidenceKey, edgeHead, edgeEvidenceKey)
		}
		key = rest[1]
	}
	return edgeEvidenceMap(e.Evidence)[key], nil
}
