// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"strings"
	"unicode/utf8"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// sanitizeText coerces a string into valid proto3-marshalable UTF-8: invalid
// byte sequences become the replacement rune, and NUL bytes  become
// \x01. Mirrors the server's store.SanitizeText so
// a node serializes identically whether it rides the wire or is read back from
// either backend — but it must run CLIENT-SIDE here because the FUL-351 inline-
// Node wire marshals typed proto Node messages, and proto3 string fields REJECT
// invalid UTF-8 at MARSHAL time (the old by-hash path shipped opaque node_json
// bytes, which tolerated it). Fast path: clean strings (the overwhelming common
// case) return unchanged.
func sanitizeText(s string) string {
	out := s
	if !utf8.ValidString(out) {
		out = strings.ToValidUTF8(out, "�")
	}
	if strings.IndexByte(out, 0) >= 0 {
		out = strings.ReplaceAll(out, "\x00", "\x01")
	}
	return out
}

// sanitizeNodeText sanitizes every proto3 string field of n in place. Called on
// each inline node before the CollectChunk marshal.
func sanitizeNodeText(n *knowledgev1.Node) {
	n.SymbolName = sanitizeText(n.SymbolName)
	n.FilePath = sanitizeText(n.FilePath)
	n.Language = sanitizeText(n.Language)
	n.Content = sanitizeText(n.Content)
	n.Signature = sanitizeText(n.Signature)
	n.TestKind = sanitizeText(n.TestKind)
	n.Summary = sanitizeText(n.Summary)
	n.Keywords = sanitizeText(n.Keywords)
	n.Description = sanitizeText(n.Description)
	n.Source = sanitizeText(n.Source)
	n.Status = sanitizeText(n.Status)
	n.Type = sanitizeText(n.Type)
	n.Id = sanitizeText(n.Id)
}
