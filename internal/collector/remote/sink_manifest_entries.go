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

	// perFileHashes is the client's per-file aggregate. EMPTY for graph families
	// outside the diff-eligible set, BY CONSTRUCTION rather than by oversight:
	// FileContributionHashes is called only inside that gate, so a web or pdf
	// collect has no map to send — and those families have no manifest either, so
	// the server declines nothing for them. Do NOT "fix" this by hoisting the map
	// out of the gate: the per-ROW hashes are hoisted because every family stores
	// them, the per-FILE map is not because only the diff consumes it.
	perFileHashes map[string][32]byte

	// fileByNodeID resolves an EDGE's owning file from its FROM node, which is how
	// an edge chunk names the files its rows belong to — edges carry no file path
	// of their own.
	fileByNodeID map[string]string
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

// entriesForNodes names the owning files of a NODE chunk's rows. A fileless node
// contributes no entry — it is outside the manifest entirely and can never be
// declined.
func (h chunkHashFields) entriesForNodes(nodes []*knowledgev1.Node) []*knowledgev1.ManifestEntry {
	paths := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		if p := n.GetFilePath(); p != "" {
			paths[p] = struct{}{}
		}
	}
	return h.entriesFor(paths)
}

// entriesForEdges names the owning files of an EDGE chunk's rows, resolved from
// each edge's FROM node because an edge carries no file path of its own. This is
// the same derivation the server's edge-side decline performs, so both sides
// compare the same per-file hashes.
func (h chunkHashFields) entriesForEdges(edges []*knowledgev1.BatchEdge) []*knowledgev1.ManifestEntry {
	paths := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		if p := h.fileByNodeID[e.GetFromId()]; p != "" {
			paths[p] = struct{}{}
		}
	}
	return h.entriesFor(paths)
}

// entriesFor renders the named files' hashes, in FILE-PATH ORDER so a chunk's
// wire bytes are reproducible across runs rather than following Go's randomized
// map iteration. A path the client computed no hash for is omitted: the server
// then has nothing to compare and lands the rows, which is the fail-closed side.
func (h chunkHashFields) entriesFor(paths map[string]struct{}) []*knowledgev1.ManifestEntry {
	if len(h.perFileHashes) == 0 || len(paths) == 0 {
		return nil
	}
	ordered := make([]string, 0, len(paths))
	for p := range paths {
		if _, ok := h.perFileHashes[p]; ok {
			ordered = append(ordered, p)
		}
	}
	if len(ordered) == 0 {
		return nil
	}
	sort.Strings(ordered)
	out := make([]*knowledgev1.ManifestEntry, 0, len(ordered))
	for _, p := range ordered {
		digest := h.perFileHashes[p]
		row := make([]byte, len(digest))
		copy(row, digest[:])
		out = append(out, &knowledgev1.ManifestEntry{FilePath: p, ContributionHash: row})
	}
	return out
}
