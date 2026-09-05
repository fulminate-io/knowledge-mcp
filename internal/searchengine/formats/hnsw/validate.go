// SPDX-License-Identifier: Apache-2.0

package hnsw

// validate.go — the READER-side structural check on a stored v3 blob, the hnsw
// mirror of formats/bm25's validate.go.
//
// WHAT IS DIFFERENT HERE, and it is worth stating because it changes how much
// this can find. openGraphV3 already verifies a footer CRC32 over the whole blob
// prefix, so ordinary bit rot is refused at open with a rebuild remedy — bm25
// has no such check, which is why one damaged byte there reached a query. The
// class this validator exists for is the one a checksum cannot see: a WRITER
// that emits internally inconsistent bytes and then checksums exactly what it
// emitted. That is the shape the bm25 incident turned out to have, its id being
// the sha256 of the very bytes that were wrong.
//
// WHY A FULL WALK. Open is deliberately O(1) in the node count: it validates
// section extents and returns, because opening is on the path of every segment
// the daemon loads. Everything below a section — a node's id offset, its layer
// index, the ordinals in its neighbor runs — is DATA INSIDE a validated section,
// and nothing at open constrains it. Those are exactly the values that killed a
// search in the audit that accompanied this file.

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// FormatName is the segment-family name this package's blobs are stored under.
// Exported so a caller that dispatches a validator by format — the store census
// — names the family from its owner rather than from a duplicated literal.
const FormatName = formatName

// ValidateSegment opens payload and resolves every per-node reference the read
// path can follow, returning nil when the segment is structurally consistent
// with itself.
//
// It reports three distinguishable outcomes, exactly as the bm25 mirror does:
//
//   - nil — every node entry, id span, layer row, neighbor run and neighbor
//     ordinal resolved inside the payload.
//   - *searchengine.CorruptSegmentError — a stored reference pointed somewhere
//     the payload does not have. This is the shape that kills a query.
//   - any other error — the payload could not be opened as a v3 blob at all,
//     which includes a failed CRC and is a louder, earlier failure than a
//     segment that opens and then lies about its contents.
//
// The id is stamped onto a corruption error so a caller walking many files can
// name the offending one.
func ValidateSegment(id searchengine.SegmentID, payload []byte) (err error) {
	// Deferred LIFO: CatchCorrupt is registered last so it runs FIRST, converting
	// the raise into corrupt; the outer defer then publishes it as the return.
	var corrupt *searchengine.CorruptSegmentError
	defer func() {
		if corrupt != nil {
			err = corrupt
		}
	}()
	defer searchengine.CatchCorrupt(id, &corrupt)

	g, openErr := openGraphV3(payload)
	if openErr != nil {
		return fmt.Errorf("hnsw: segment %s (%d bytes) does not open: %w", id, len(payload), openErr)
	}

	for ord := range g.nodes {
		validateNode(g, uint32(ord))
	}

	// THE ID DIRECTORY IS A SEPARATE ARRAY OF ORDINALS and is not reached by the
	// per-node walk above: vectorByID binary-searches it and then indexes the
	// node directory with whatever it finds. An out-of-range entry here is a
	// lookup that dies rather than a search that dies, and nothing else checks it.
	for i, ord := range g.idDir {
		if int(ord) >= g.nodes {
			searchengine.RaiseCorrupt(
				"hnsw: id directory row %d names ordinal %d, but the segment holds %d nodes", i, ord, g.nodes)
		}
	}
	return nil
}

// validateNode resolves everything the read path can reach for one node: its
// directory entry, its external id, its vector, and every layer's neighbor run
// including the ordinals inside it.
//
// It calls the REAL accessors rather than re-deriving their arithmetic. A
// validator with its own copy of the layout would drift from the reader it is
// meant to speak for, and would then certify segments the reader still dies on —
// so every check here is the reader's own, reached by using it.
func validateNode(g *mappedGraph, ord uint32) {
	// entryAt bounds the ordinal; nodeMaxLevel reads through it.
	maxLevel := g.nodeMaxLevel(ord)
	// idView raises on a span past the blob.
	_ = g.externalIDAt(ord)
	// The vector block is sized nodes*vecBytes, so an in-range ordinal is in
	// range here too; reading it anyway is what makes that a checked fact rather
	// than an inferred one.
	_ = g.nodeVector(ord)

	// neighborsAt raises on a layer row outside the offset array, on a run whose
	// span is impossible, and on any ordinal the segment cannot have.
	for layer := 0; layer <= maxLevel; layer++ {
		_ = g.neighborsAt(ord, layer)
	}
}
