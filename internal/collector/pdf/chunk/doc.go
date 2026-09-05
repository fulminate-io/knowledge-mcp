// Package chunk owns the layout-block → Chunk emitter for the
// collector/pdf module. It exposes Chunk + Options + Mode and the
// Build entry point that turns a sequence of classified layout blocks
// into a flat (ModeParagraph) or hierarchical (ModeSection) chunk
// stream suitable for retrieval-augmented generation, recipe-driven
// pattern extraction, and downstream embedding pipelines.
//
// # Modes
//
// ModeParagraph emits one Chunk per layout block in document order.
// Useful for paragraph-level retrieval and per-block embedding.
//
// ModeSection groups every block under its enclosing heading into a
// single Chunk per section, with the per-block breakdown carried as
// Children. Headings build a stack; sub-headings nest under their
// parent until a same-or-higher-level heading closes the run. When
// the document has no headings at all, every block lands under a
// single synthetic root chunk.
//
// # Continuity
//
// Build runs a cross-page continuity merge before mode dispatch: a
// paragraph that ends a page without terminator punctuation and is
// followed on the next page by a lowercase-starting block of similar
// font and X-start is treated as a single logical block spanning both
// pages. All three signals are required — terminator punctuation,
// font mismatch, or X-start mismatch each block the merge.
//
// # Running page chrome
//
// Build drops nothing. A block whose text repeats across pages — a
// running header, a footer, a page number — is STAMPED with
// page_repeat_count and the two companion chrome signals and emitted
// like any other block. A consumer that wants it gone filters on those
// signals; chrome.IsPageChrome is the one Go copy of the rule the old
// deleting detector applied, for callers who want that exact verdict.
//
// # MinChunkChars
//
// MinChunkChars drops chunks whose text is shorter than the threshold
// after normalization. Short fragments are dropped entirely (not
// merged into the next chunk). The filter recurses into Children in
// ModeSection so a section chunk's body slice is also scrubbed.
//
// # Dependency invariant
//
// chunk depends on layout + classify + stdlib only. It does NOT
// import the top-level pdf package; the public Document.Chunks method
// supplies a thin adapter that satisfies the chunk.Document
// interface defined here.
package chunk
