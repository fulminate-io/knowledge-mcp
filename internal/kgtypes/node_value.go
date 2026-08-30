// SPDX-License-Identifier: Apache-2.0

package kgtypes

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// node_value.go owns the metadata-accessor free funcs over the wire proto
// *knowledgev1.Node, the client-side counterpart to the *store.Node wrapper
// methods Value / Values / SetValue / DeleteValue / Meta / SetMeta
// (cmd/knowledge-server/internal/store/node_value.go + graph_types.go).
//
// These are PLAIN n.Metadata map operations — no PromotionRegistry /
// OverrideConfig / edgeValueCache dispatch. That dispatch is server-internal
// hint machinery the wire-decoded node never carries: a node materialized
// from the proto has nil regHint / ovHint / edgeValueCache, so store.Node.Value
// already resolves every key as scalar (resolveRepresentation returns
// RepresentationAuto → falls through to n.Metadata[key], node_value.go:69-72).
// These free funcs are byte-identical to that scalar fall-through.
//
// kgtypes is a client-private leaf package and MUST NOT import any storage
// engine — these funcs exist precisely so the client can read / write
// wire-node metadata without reaching for the store wrapper.

// Value returns the metadata value for key on n, or "" when the key is absent
// or n / n.Metadata is nil. Mirrors the scalar arm of store.Node.Value
// (cmd/knowledge-server/internal/store/node_value.go:45-73): a read from a nil
// Go map returns the zero value, so the nil guards collapse to a plain map
// lookup. Empty string is returned both for "key absent" and "key present with
// empty value".
func Value(n *knowledgev1.Node, key string) string {
	if n == nil {
		return ""
	}
	return n.GetMetadata()[key]
}

// Values returns the metadata value for key on n as a slice: a single-element
// slice when present, or nil when the key is absent / n is nil. Mirrors the
// scalar arm of store.Node.Values
// (cmd/knowledge-server/internal/store/node_value.go:85-110) — returning
// nil rather than a one-element empty-string slice on absence so callers that
// `for _, v := range Values(n, key)` get zero iterations.
func Values(n *knowledgev1.Node, key string) []string {
	if n == nil {
		return nil
	}
	m := n.GetMetadata()
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok {
		return nil
	}
	return []string{v}
}

// SetValue writes value under key on n, lazily initializing n.Metadata.
// Mirrors the scalar arm of store.Node.SetValue
// (cmd/knowledge-server/internal/store/node_value.go:130-151).
// No-op when n is nil.
func SetValue(n *knowledgev1.Node, key, value string) {
	if n == nil {
		return
	}
	if n.Metadata == nil {
		n.Metadata = make(map[string]string, 1)
	}
	n.Metadata[key] = value
}

// DeleteValue removes key from n's metadata. Mirrors the scalar arm of
// store.Node.DeleteValue
// (cmd/knowledge-server/internal/store/node_value.go:159-173). No-op when n or
// n.Metadata is nil, or when the key is absent (same shape as delete(map, key)).
func DeleteValue(n *knowledgev1.Node, key string) {
	if n == nil || n.Metadata == nil {
		return
	}
	delete(n.Metadata, key)
}

// Meta is an alias for Value, mirroring the deprecated store.Node.Meta shim
// (cmd/knowledge-server/internal/store/graph_types.go:305-307) so callers
// porting off the wrapper keep
// the same call shape.
func Meta(n *knowledgev1.Node, key string) string {
	return Value(n, key)
}

// SetMeta is an alias for SetValue, mirroring the deprecated store.Node.SetMeta
// shim (cmd/knowledge-server/internal/store/graph_types.go:317-319).
func SetMeta(n *knowledgev1.Node, key, value string) {
	SetValue(n, key, value)
}

// IsTombstoned reports whether n has been tombstoned via the field path.
