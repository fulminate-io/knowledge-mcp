// SPDX-License-Identifier: Apache-2.0

package segmentdist

// testkit_shared_test.go holds the RAIL-FREE test helpers that were defined inside
// files the cloud-rail deletion removed whole.
//
// WHY THEY MOVED RATHER THAN DIED WITH THEIR FILES. Each of these is a pure helper
// with no dependence on the rail: two format-name bindings and an id-prefixer. They
// happened to be declared in publish_manifest_swap_test.go and
// publish_lifecycle_gate_test.go, whose SUBJECTS were the publish path, but the
// helpers themselves are about neither publishing nor shipping and are consumed by a
// dozen surviving tests. Relocating them keeps that coverage compiling without
// keeping one line of rail machinery alive.
//
// The rail-DEPENDENT helpers from those same files were deliberately NOT relocated:
// writerManifest and serverSegCount read the server fake's manifest/publish
// machinery, and moving them would have kept that machinery alive. They got
// SUCCESSORS instead — l2IDsFor below answers writerManifest's question against the
// only store that remains. A successor is not a relocation: the old helper reported
// what a writer had PUBLISHED, the new one reports what a pool HOLDS, and the two
// coincide only because there is now exactly one place a segment can be.

import (
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// hnswFormatName and bm25FormatName are the two live format names, bound once so a
// test asserting across formats cannot drift from what the engines actually report.
var (
	hnswFormatName = hnsw.New().Name()
	bm25FormatName = bm25.New().Name()
)

// l2IDsFor returns the segment ids one graph+format actually holds on disk.
//
// IT IS THE SUCCESSOR TO writerManifest, which read the same question off the server
// fake's PUBLISHED manifest for a given writer. There is no manifest and no writer
// axis, so "what did this writer publish" becomes "what does this pool hold" — the
// only remaining answer, and the one every caller of writerManifest was really after.
//
// THE FORMAT ARGUMENT IS NOT DECORATION. Each format's cache is rooted separately by
// graphCacheDirFor, so this reads exactly one family; passing the wrong format
// silently reads a different pool rather than filtering a shared list.
func l2IDsFor(cacheDir, name, format string) []searchengine.SegmentID {
	return newDiskSegmentCache(
		graphCacheDirFor(cacheDir, kgtypes.GraphCode, name, format), 0, adviceRandom).Keys()
}

// sortedCacheIDs is one pool's id set in a stable order, for before/after comparison.
//
// THE SET IS THE OBSERVABLE, NOT ITS SIZE, and the distinction is load-bearing for
// every "did the write land" assertion in this package. A drain confined to one
// partition REWRITES it: the new bytes hash to a new id and the old id retires, so a
// count is unchanged while every byte changed. Because ids are content hashes, an
// unchanged SET is the one thing that genuinely means unchanged bytes.
func sortedCacheIDs(c segmentL2Cache) []searchengine.SegmentID {
	ids := c.Keys()
	slices.Sort(ids)
	return ids
}

// failAfterWarmSource WAS DELETED HERE. It wrapped a segment source and, once
// tripped, failed its List and Fetch, which is how the overlay error contract used to
// be driven: one pool warm, the other cold, and the cold one's fall-through to the
// source made to fail.
//
// IT COULD NOT FAIL ANY MORE. Nothing calls a source's List or Fetch — the load path
// reads the L2 cache directly — so a tripped double was simply never consulted. That
// left one leg of TestSearchOverlayPoolErrors asserting an error that could not
// arrive, and the other leg passing VACUOUSLY: its "degraded to base" result was an
// empty overlay pool contributing nothing, which equals the base ranking whether the
// degrade works or not.
//
// THE PROPERTY SURVIVED AND MOVED, it did not go with the double. Both legs now break
// the cold pool's own L2 read with an undecodable blob planted in that pool's cache
// root, which fails through the real read path; the degrade leg additionally seeds the
// overlay corpus through a separate producer so the pool is non-empty AND cold, which
// is what makes "degraded to base" distinguishable from "there was no overlay".

// prefixIDs returns a copy of docs with every id prefixed — the established
// per-batch distinct-id technique so successive batches seal DISTINCT segments.
//
// IT MUTATES IN PLACE AND RETURNS THE SAME SLICE, exactly as it did before the move.
// That is not tidy, but several callers rely on the aliasing, and "fixing" it here
// would change the behaviour of tests this relocation is only supposed to keep
// compiling.
func prefixIDs(docs []searchengine.Document, prefix string) []searchengine.Document {
	for i := range docs {
		docs[i].ID = prefix + docs[i].ID
	}
	return docs
}
