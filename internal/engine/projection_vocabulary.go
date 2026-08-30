// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"slices"
	"strings"
)

// nodeProjectionKeys is the projection vocabulary of the NODE grammar
// (ProjectNodeJSON) — every top-level key a by-id, by-ids or type-browse read
// accepts. Sorted, because the refusal message renders it in this order.
//
// `line` projects knowledgev1.Node.start_line; end_line, is_exported, is_test
// and collect_epoch are deliberately OUT of the vocabulary and become a named
// refusal rather than a silent drop.
//
// tombstoned_at is IN the vocabulary but is not served unconditionally: naming
// it without the include_tombstones opt-in is refused, because a read that
// cannot return a tombstoned row has no value to project. Under the opt-in it
// is OMITTED ENTIRELY for a live node rather than emitted as 0 — the
// per-metadata-key precedent, never the created_at/updated_at one, whose 0 is
// also a real row's real value.
var nodeProjectionKeys = []string{
	"content",
	"created_at",
	"description",
	"file_path",
	"id",
	"keywords",
	"language",
	"line",
	"metadata",
	"name",
	"signature",
	"source",
	"status",
	"summary",
	"symbol_name",
	"test_kind",
	"tombstoned_at",
	"type",
	"updated_at",
}

// tombstonedAtProjectionKey is the one vocabulary member gated on an opt-in, so
// the gate and the projectors name it from a single place.
const tombstonedAtProjectionKey = "tombstoned_at"

// projectTombstonedAt writes the tombstone stamp onto a projected row, and only
// when the node actually carries one.
//
// ABSENT, NEVER A SENTINEL: a live node's TombstonedAt is 0, so emitting it
// unconditionally would put a value on the wire that a reader cannot tell from a
// real stamp. Both projection arms — the node grammar and the hit grammar — call
// this, so the omit-when-live rule has exactly one implementation and the two
// arms cannot drift apart on it.
func projectTombstonedAt(out map[string]any, tombstonedAt int64) {
	if tombstonedAt != 0 {
		out[tombstonedAtProjectionKey] = tombstonedAt
	}
}

// hitOnlyProjectionKeys are properties of a search HIT rather than of a node:
// score is SearchResult.Score, graph and graph_instance are SearchResult.Graph
// and SearchResult.GraphInstance. A by-id or browse read has no hit to read
// them from, so naming one there is a caller error and is told, not ignored.
var hitOnlyProjectionKeys = []string{
	"graph",
	"graph_instance",
	"score",
}

// hitProjectionKeys is the projection vocabulary of the HIT grammar
// (projectHydratedResult) — every node key plus the three hit-only ones.
var hitProjectionKeys = mergeProjectionKeys(nodeProjectionKeys, hitOnlyProjectionKeys)

// The membership sets and the rendered accepted-key lists are built ONCE at
// package scope: validation runs once per response, and the error path then
// allocates nothing beyond the formatted message.
var (
	nodeProjectionSet    = projectionKeySet(nodeProjectionKeys)
	hitProjectionSet     = projectionKeySet(hitProjectionKeys)
	hitOnlyProjectionSet = projectionKeySet(hitOnlyProjectionKeys)

	nodeProjectionList = strings.Join(nodeProjectionKeys, ", ")
	hitProjectionList  = strings.Join(hitProjectionKeys, ", ")
)

// mergeProjectionKeys returns a fresh sorted union of the two key sets. It
// builds a new slice rather than appending, so neither input can be aliased or
// grown in place by the result.
func mergeProjectionKeys(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	slices.Sort(out)
	return slices.Compact(out)
}

// projectionKeySet renders a key list as a membership map.
func projectionKeySet(keys []string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// metadataProjectionPrefix marks the per-metadata-key projection form, which is
// an open vocabulary (any metadata key) and so is never validated against the
// declared key set.
const metadataProjectionPrefix = "metadata."

// ValidateNodeProjection refuses any field the NODE grammar does not serve,
// naming the offending key and the accepted vocabulary. A hit-only key gets the
// message telling the caller where that key IS available rather than the
// generic one. Returns nil for an empty field list; skips the
// "metadata.<key>" form. Refuses on the FIRST offending key, iterating the
// caller's slice in order so the message is deterministic.
//
// includeTombstones is the caller's opt-in, and it gates tombstoned_at alone.
// Without it a read returns no tombstoned row at all, so the key could only ever
// project nothing — accepting it would be the accepted-and-dropped shape this
// refusal exists to prevent. The message names the key and the flag that serves
// it, in the style the hit-only refusal above uses.
func ValidateNodeProjection(fields []string, includeTombstones bool) error {
	for _, f := range fields {
		if f == tombstonedAtProjectionKey && !includeTombstones {
			return fmt.Errorf("fields: projection key %q is served only under the include_tombstones opt-in — without it this read returns no tombstoned node, so the key has nothing to project. Pass include_tombstones:true alongside it", f)
		}
		if strings.HasPrefix(f, metadataProjectionPrefix) || nodeProjectionSet[f] {
			continue
		}
		if hitOnlyProjectionSet[f] {
			return fmt.Errorf("fields: projection key %q is a search-result property, available only on ranked-search reads (query with text or mode:\"text\", or the search tool). Accepted keys on this read: %s", f, nodeProjectionList)
		}
		return fmt.Errorf("fields: unsupported projection key %q. Accepted keys: %s. Per-metadata-key projections use the \"metadata.<key>\" form", f, nodeProjectionList)
	}
	return nil
}

// ValidateHitProjection refuses any field the HIT grammar does not serve. Same
// contract as ValidateNodeProjection; there is no hit-only special case here
// because a ranked-search read serves every hit-only key.
//
// THE tombstoned_at ASYMMETRY IS DELIBERATE, and it is documented here because
// the two arms differ on exactly one key: the node arm refuses tombstoned_at
// without the opt-in; the hit arm accepts it quietly because omit-when-live
// makes absence semantically truthful on arms that reject the flag, while arms
// that route it serve it. A search caller naming tombstoned_at on a read that
// surfaces no tombstoned row therefore gets an absent key, which is the true
// answer for a live node rather than an accepted-and-dropped projection — so
// there is nothing for this arm to refuse. See projectTombstonedAt above: both
// arms share that one implementation, so they cannot drift apart on it.
func ValidateHitProjection(fields []string) error {
	for _, f := range fields {
		if strings.HasPrefix(f, metadataProjectionPrefix) || hitProjectionSet[f] {
			continue
		}
		return fmt.Errorf("fields: unsupported projection key %q. Accepted keys: %s. Per-metadata-key projections use the \"metadata.<key>\" form", f, hitProjectionList)
	}
	return nil
}
