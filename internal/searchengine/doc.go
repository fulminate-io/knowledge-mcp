// Package searchengine is the index-agnostic, CLIENT-ONLY segmented index engine.
//
// It implements SegmentedIndex[Q, S] over a pluggable SegmentFormat[Q, S]: an
// atomic copy-on-write segment set (the lock-free read path), an atomic liveDocs
// bitset for deletes, coalesce/seal/CAS-append writes, a background merge that
// consolidates live indexed data via SegmentFormat.Merge (no Document retention,
// so decoded/pulled segments are merge-equivalent to locally-built ones), and a
// lock-free parallel cross-segment Search with top-k merge.
//
// # Dependency-direction invariant: zero server-internal imports
//
// This package imports the standard library and its own subpackages ONLY (later:
// formats/bm25, formats/hnsw). It must NEVER import any of:
//
//	github.com/fulminate-io/knowledge-mcp-server/internal/...
//
// The invariant is STRUCTURALLY ENFORCED, not merely conventional: cmd/knowledge
// is a SEPARATE Go module from cmd/knowledge-server, so an import of the server's
// internal/ tree would be a cross-module edge into another module's internal/
// packages — which the Go toolchain refuses to compile (internal/ is unimportable
// across module boundaries, and the two cmd modules do not depend on each other).
// A violation therefore fails the build rather than slipping through review.
//
// # Why client-only
//
// The engine is CLIENT application logic (client/server separation: cmd/knowledge
// = client). The server is a dumb opaque blob store with NO searchengine import:
// the client builds + ships segments; the server stores/serves opaque bytes and
// never decodes them. Keeping the package import-clean means a future
// service-extraction is a pure directory move — nothing here is wired to the
// client beyond stdlib + its own subpackages.
package searchengine
