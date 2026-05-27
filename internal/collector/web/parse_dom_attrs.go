// SPDX-License-Identifier: Apache-2.0

package web

import (
	"encoding/json"
	"strings"

	"golang.org/x/net/html"
)

// commonAttrs captures the HTML attributes the scraper preserves verbatim
// on every emitted structural node. Scraper makes no judgment about what
// these mean — transformers interpret them per their target schema.
type commonAttrs struct {
	Class string            // raw class attribute value (empty-omit)
	ID    string            // raw id attribute value (empty-omit)
	Role  string            // ARIA role (empty-omit)
	Data  map[string]string // every data-* attribute, raw values (nil-omit)
}

// extractCommonAttrs reads class/id/role + every data-* attribute from n.
// Returns zero-valued commonAttrs when n is nil or has no attributes. Does
// NOT filter, lowercase, or interpret — verbatim preservation only.
func extractCommonAttrs(n *html.Node) commonAttrs {
	if n == nil {
		return commonAttrs{}
	}
	var out commonAttrs
	for _, a := range n.Attr {
		switch a.Key {
		case "class":
			out.Class = a.Val
		case "id":
			out.ID = a.Val
		case "role":
			out.Role = a.Val
		default:
			if strings.HasPrefix(a.Key, "data-") {
				if out.Data == nil {
					out.Data = make(map[string]string)
				}
				out.Data[strings.TrimPrefix(a.Key, "data-")] = a.Val
			}
		}
	}
	return out
}

// applyCommonAttrs writes a commonAttrs into a metadata map with the keys
// "class" / "id" / "role" / "data". Empty strings are omitted. Data is
// JSON-encoded via encoding/json.Marshal; on the theoretical marshal
// failure the data key is silently skipped. json.Marshal of a
// map[string]string cannot fail in practice — every supported value type
// (string keys, string values) round-trips cleanly — so the silent skip
// is safe: the only way here is a compiler/runtime bug, at which point a
// missing metadata key is the least of our problems.
func applyCommonAttrs(md map[string]string, attrs commonAttrs) {
	if md == nil {
		return
	}
	if attrs.Class != "" {
		md["class"] = attrs.Class
	}
	if attrs.ID != "" {
		md["id"] = attrs.ID
	}
	if attrs.Role != "" {
		md["role"] = attrs.Role
	}
	if len(attrs.Data) > 0 {
		if b, err := json.Marshal(attrs.Data); err == nil {
			md["data"] = string(b)
		}
	}
}
