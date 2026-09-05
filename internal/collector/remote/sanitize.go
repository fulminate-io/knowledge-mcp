// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"log/slog"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// sanitizeText coerces a string into valid proto3-marshalable UTF-8: invalid
// byte sequences become the replacement rune, and NUL bytes  become
// \x01. Mirrors the server's store.SanitizeText so
// a node serializes identically whether it rides the wire or is read back from
// either backend — but it must run CLIENT-SIDE here because the inline-
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

// needsSanitize reports whether s carries anything sanitizeText would change:
// invalid UTF-8, or a NUL. It is the two predicates sanitizeText already tests,
// hoisted so a walk can decide without building anything.
//
// The server carries a TWIN of this name in package store. They are two
// packages and two bodies rather than a shared helper: there is no shared
// hand-written package between the two binaries.
func needsSanitize(s string) bool {
	return !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0
}

// sanitizeMetadata coerces the keys and values of a node's metadata map in
// place, preserving the caller's map identity.
//
// FAST PATH FIRST, and it is why this is not one loop. It runs on EVERY node of
// EVERY collect, including code collects of hundreds of thousands of nodes, and
// the overwhelming common case is a map that is already clean. The walk tests
// each key and value and returns with NO allocation when none is dirty, which
// is the same no-allocation contract sanitizeText keeps for the scalar fields.
// The always-rebuild shape measured 304 B/op and 7 allocs/op on a clean
// eight-key node against 0 and 0 for this one.
//
// SANITIZING A KEY IS A MANY-TO-ONE TRANSFORM, so two distinct keys can collapse
// onto one entry and keeping both is unrepresentable in a map[string]string.
// The survivor is therefore chosen deterministically: the dirty path iterates
// the keys SORTED rather than ranged, so the lexicographically lesser ORIGINAL
// key wins, and the loser is logged at Warn naming both originals so a collapse
// is reconstructible from the log. Go's map iteration order is randomized, so a
// ranged rewrite would pick a different survivor per run and break the
// byte-identical-on-either-backend contract this coercion exists to keep. The
// product already answers this question the same way elsewhere: where a bulk
// merge collapses many source rows onto one key, the survivor is settled by a
// recorded EXTENDED TOTAL ORDER tie disposition rather than by whichever row
// the iteration happened to reach last. The shape of the problem is identical
// and so is the remedy — an arbitrary order replaced by a total one.
//
// The collapse is covered by the sanitizer's ALREADY-RECORDED coercion approval
// extended to this corner, not by a counted census entry: sanitizeNodeText runs
// downstream of the CollectComposition that owns the degrade census, so there is
// no collect response to count into. Reachability is very low — a collision
// needs two keys on ONE node differing only in invalid-UTF-8 or NUL bytes.
func sanitizeMetadata(md map[string]string) {
	if len(md) == 0 {
		return
	}
	dirty := false
	for k, v := range md {
		if needsSanitize(k) || needsSanitize(v) {
			dirty = true
			break
		}
	}
	if !dirty {
		return
	}

	out := make(map[string]string, len(md))
	keptOriginal := make(map[string]string, len(md))
	for _, k := range slices.Sorted(maps.Keys(md)) {
		sk := sanitizeText(k)
		if winner, taken := keptOriginal[sk]; taken {
			slog.Warn("collector: metadata keys collided after sanitizing, later key dropped",
				"sanitized_key", sk, "kept_original", winner,
				"kept_value", out[sk], "dropped_original", k)
			continue
		}
		keptOriginal[sk] = k
		out[sk] = sanitizeText(md[k])
	}
	clear(md)
	maps.Copy(md, out)
}

// sanitizeNodeText sanitizes every proto3 string field of n in place: the
// thirteen named scalars below AND the metadata map's keys and values, which
// ride proto3 strings too — a proto3 map<string,string> rejects invalid UTF-8 in
// a KEY on the same terms as in a value. Called on each inline node before the
// CollectChunk marshal.
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
	sanitizeMetadata(n.Metadata)
}
