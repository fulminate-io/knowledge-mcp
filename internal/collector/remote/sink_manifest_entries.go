// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// sink_manifest_entries.go holds chunkHashFields — the struct and its ENTRY
// RENDERERS, the methods that turn a chunk's rows into the per-file manifest
// entries a CollectChunk carries — split out of sink.go for the 500-line file
// cap.
//
// The renderers moved out of sink.go first and the struct followed, so the type
// and its method set now sit together; collectChunkRequests, which populates and
// threads it, stays in sink.go.

// chunkHashFields carries the three decline-gating values the builder stamps
// alongside the rows. They travel as one argument because they are meaningless
// apart: the hashes are only interpretable under the manifest identity that
// rendered them.
type chunkHashFields struct {
	// manifestID echoes the manifest the server last served. EMPTY when no
	// manifest was fetched — a non-diff-eligible graph family, or any degraded
	// lane — and empty is CORRECT rather than a gap: the server's first decline
	// conjunct then fails and every row lands, which is the fail-closed direction.
	manifestID string

	// nodeHashes is index-aligned with the FULL node slice the chunker split, so
	// the builder slices it in lockstep rather than re-deriving boundaries. The
	// chunker packs whole FILE GROUPS under a byte budget, so those boundaries are
	// neither a fixed stride nor a function of the input order and cannot be
	// recomputed. The caller reorders this array with the nodes before the split
	// (groupNodesAndHashesByFile), which is what keeps the lockstep true.
	nodeHashes [][32]byte

	// perKeyHashes is the client's per-DIFF-KEY aggregate: per file for a code
	// collect, per node id for a registered custom one. EMPTY for graph families
	// outside the diff-eligible set, BY CONSTRUCTION rather than by oversight: the
	// per-key fold is computed only inside that gate, so a web or pdf collect has
	// no map to send — and those families have no manifest either, so the server
	// declines nothing for them. Do NOT "fix" this by hoisting the map out of the
	// gate: the per-ROW hashes are hoisted because every family stores them, the
	// per-KEY map is not because only the diff consumes it.
	perKeyHashes map[string][32]byte

	// fileByNodeID resolves an EDGE's owning file from its FROM node, which is how
	// a FILE-KEYED edge chunk names the files its rows belong to — edges carry no
	// file path of their own. It is EMPTY under the node key, where an edge's
	// owning key is its FromID itself and no projection is needed.
	fileByNodeID map[string]string

	// kind is the unit every entry this struct renders is keyed on, carried from
	// the sink's single family gate rather than re-derived here. It decides which
	// oneof arm the entries take, and a renderer that guessed would produce a chunk
	// the server decodes onto the wrong key space.
	kind diffKeyKind
}

// nodeHashesFor returns this chunk's own slice of the per-node digests, or nil
// when the caller carried none. A short array yields nil rather than a truncated
// slice: sending fewer hashes than nodes is refused server-side, which is the
// loud outcome, while a silently truncated one would not be.
func (h chunkHashFields) nodeHashesFor(offset, n int) [][]byte {
	if n == 0 || offset+n > len(h.nodeHashes) {
		return nil
	}
	out := make([][]byte, n)
	for i := range n {
		digest := h.nodeHashes[offset+i]
		row := make([]byte, len(digest))
		copy(row, digest[:])
		out[i] = row
	}
	return out
}

// entriesForNodes names the owning DIFF KEYS of a NODE chunk's rows: each
// node's own file path under the file key, and its own id under the node key.
//
// A ROW WITH NO KEY CONTRIBUTES NO ENTRY — a fileless node under the file key,
// an id-less node under the node key. It is outside the manifest entirely and
// can never be declined, which is the same rule under either unit.
func (h chunkHashFields) entriesForNodes(nodes []*knowledgev1.Node) []*knowledgev1.ManifestEntry {
	keys := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		if k := h.keyForNode(n); k != "" {
			keys[k] = struct{}{}
		}
	}
	return h.entriesFor(keys)
}

// entriesForEdges names the owning DIFF KEYS of an EDGE chunk's rows, resolved
// from each edge's FROM node because an edge carries no key of its own. This is
// the same derivation the server's edge-side decline performs, so both sides
// compare the same per-key hashes.
func (h chunkHashFields) entriesForEdges(edges []*knowledgev1.BatchEdge) []*knowledgev1.ManifestEntry {
	keys := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		if k := h.keyForEdge(e); k != "" {
			keys[k] = struct{}{}
		}
	}
	return h.entriesFor(keys)
}

// keyForNode is the node half of the ownership rule, in ONE place. Under the
// file key a node is owned by its file path; under the node key it IS its own
// owner, so the key is its id.
func (h chunkHashFields) keyForNode(n *knowledgev1.Node) string {
	if h.kind == diffKeyNode {
		return n.GetId()
	}
	return n.GetFilePath()
}

// keyForEdge is the edge half of the same rule: spec section E says an edge is
// owned by its FROM node, so the file key PROJECTS that node onto its file and
// the node key takes the node itself. The projection map is empty under the node
// key precisely because no projection is needed there.
func (h chunkHashFields) keyForEdge(e *knowledgev1.BatchEdge) string {
	if h.kind == diffKeyNode {
		return e.GetFromId()
	}
	return h.fileByNodeID[e.GetFromId()]
}

// entriesFor renders the named keys' hashes, in KEY ORDER so a chunk's wire
// bytes are reproducible across runs rather than following Go's randomized map
// iteration. A key the client computed no hash for is omitted: the server then
// has nothing to compare and lands the rows, which is the fail-closed side.
//
// THE ARM COMES FROM h.kind, THROUGH newManifestEntry. Spelling the file arm
// here would silently ship a node-keyed collect's ids in the file_path field,
// where the server's decoder either rejects the chunk or — on the persisted
// snapshot path — accepts it and declines nothing forever.
func (h chunkHashFields) entriesFor(keys map[string]struct{}) []*knowledgev1.ManifestEntry {
	if len(h.perKeyHashes) == 0 || len(keys) == 0 {
		return nil
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		if _, ok := h.perKeyHashes[k]; ok {
			ordered = append(ordered, k)
		}
	}
	if len(ordered) == 0 {
		return nil
	}
	sort.Strings(ordered)
	out := make([]*knowledgev1.ManifestEntry, 0, len(ordered))
	for _, k := range ordered {
		digest := h.perKeyHashes[k]
		row := make([]byte, len(digest))
		copy(row, digest[:])
		out = append(out, newManifestEntry(h.kind, k, row))
	}
	return out
}
