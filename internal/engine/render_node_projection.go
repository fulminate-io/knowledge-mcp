// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// render_node_projection.go holds the NODE-grammar projector, split out of
// render_misc.go so that file stays under the 500-line cap. Same package, same
// callers: renderBrowseJSON, renderNodesByIDsResponse and renderNodeResponse
// reach ProjectNodeJSON exactly as before.

// ProjectNodeJSON mirrors the server projectNodeJSON projection grammar: every
// nodeProjectionKeys member as a top-level key, plus the per-metadata-key
// "metadata.<key>" form. An unsupported key is REFUSED by ValidateNodeProjection
// before this runs, so every key reaching this switch is a declared one.
// Exported so engine.BrowseJSONResult serves the tools-side rules browse
// (intercept_query_rules.go) through the SAME grammar (tools→engine import is
// one-way; no cycle) rather than copy-pasting it.
//
// A requested top-level key is emitted UNCONDITIONALLY — empty string for an
// unset text field, 0 for an unset timestamp, empty map for absent metadata — so
// "the field is present and unset" stays distinguishable from "the key was not in
// your projection". tombstoned_at is the ONE top-level key carved out of that
// rule: it is OMITTED ENTIRELY for a live node, because 0 is what a live node
// carries and a sentinel 0 is indistinguishable at the wire from a real
// tombstone stamp. Only that key and the metadata.<key> form keep a conditional
// omission. created_at/updated_at are raw int64 unix nanos, matching the by-id
// convention, and so is tombstoned_at when it is emitted at all.
func ProjectNodeJSON(n *knowledgev1.Node, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "id":
			out["id"] = n.Id
		case "name":
			out["name"] = n.SymbolName
		case "type":
			out["type"] = n.Type
		case "status":
			out["status"] = n.Status
		case "description":
			out["description"] = n.Description
		case "content":
			out["content"] = n.Content
		case "source":
			out["source"] = n.Source
		case "symbol_name":
			out["symbol_name"] = n.SymbolName
		case "signature":
			out["signature"] = n.Signature
		case "summary":
			out["summary"] = n.Summary
		case "keywords":
			out["keywords"] = n.Keywords
		case "test_kind":
			out["test_kind"] = n.TestKind
		case "file_path":
			out["file_path"] = n.FilePath
		case "line":
			out["line"] = n.StartLine
		case "language":
			out["language"] = n.Language
		case "created_at":
			out["created_at"] = n.CreatedAt
		case "updated_at":
			out["updated_at"] = n.UpdatedAt
		case tombstonedAtProjectionKey:
			// Absent, never a sentinel — the shared rule the hit arm applies too.
			projectTombstonedAt(out, n.TombstonedAt)
		case "metadata":
			out["metadata"] = copyProjectedMetadata(n.Metadata)
		default:
			if key, ok := strings.CutPrefix(f, "metadata."); ok {
				if v := kgtypes.Value(n, key); v != "" {
					out[f] = v
				}
			}
		}
	}
	return out
}
