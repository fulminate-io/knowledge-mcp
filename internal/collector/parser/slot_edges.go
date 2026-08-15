// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// resolveSlotEdges turns every chunk slot the chunker recorded into the node ID
// that chunk will carry, at the one point in the pipeline where that ID is
// final.
//
// IT OVERWRITES; IT DOES NOT FILL. The chunker emits the name-built endpoints
// it has always emitted AND the slots, so every treesitter-layer assertion on
// containment endpoint names stays meaningful. The slot is authoritative
// wherever it exists and the name is the legacy carrier this pass replaces —
// nothing here reads the name to decide anything.
//
// PLACEMENT IS LOAD-BEARING and both halves of it are measured, not assumed.
// It must run strictly AFTER DeduplicateChunks, which rewrites chunk.Name by
// appending "#"+PathHash and so changes ChunkNodeID — an ID taken before it
// would be a pre-rename ID, and that rename hits exactly the colliding chunks
// this work exists to fix. It must run strictly BEFORE the per-result loop in
// chunkResultsToPopulate, which sorts result.Chunks by StartByte and thereby
// invalidates every slot index.
//
// After this pass, containment BYPASSES NAME RESOLUTION for every shape but
// one: an edge whose endpoints are already valid node IDs passes through
// resolveEdges unchanged. THE ONE EXCEPTION is a Go method's parent-to-member
// source — see the comment on the FromChunk branch below.
func resolveSlotEdges(results []*treesitter.Result) {
	for _, result := range results {
		for i := range result.Edges {
			e := &result.Edges[i]
			if e.FromChunk != 0 {
				// A Go method's receiver container never reaches here: its
				// container is a sibling type declaration that may live in
				// another file, so the chunker leaves FromChunk 0 and carries
				// the name plus the Ref instead. resolveEdges resolves that one
				// against the declaration index at package scope.
				if id, ok := slotNodeID(result, e.FromChunk); ok {
					e.FromID = id
				}
				e.FromChunk = 0
			}
			if e.ToChunk != 0 {
				if id, ok := slotNodeID(result, e.ToChunk); ok {
					e.ToID = id
				}
				e.ToChunk = 0
			}
		}
	}
}

// slotNodeID resolves a 1-based chunk slot to its node ID.
//
// An out-of-range slot is a programming error in the chunker, not a data
// condition, so it is logged loudly and the endpoint is LEFT UNTOUCHED rather
// than pointed at whatever chunk happens to sit at a clamped index — a wrong
// edge is the failure this whole change exists to eliminate.
func slotNodeID(result *treesitter.Result, slot int) (string, bool) {
	if slot < 1 || slot > len(result.Chunks) {
		slog.Error("collector: chunk slot out of range",
			"file", result.FilePath,
			"slot", slot,
			"chunks", len(result.Chunks))
		return "", false
	}
	return ChunkNodeID(result.Chunks[slot-1]), true
}
