// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
)

// emit_document_title.go holds the document ROOT'S LABEL and the vocabulary
// that reports where the label came from, split out of emit.go so the
// derivation, its closed vocabulary and the blurb composed from it read
// together — and so emit.go, which was within six lines of this package's file
// length limit, has room for the rest of the emitter.

// The THREE values metadata.title_source may carry, declared here and nowhere
// else. Every stamping site cites one: a bare literal at the stamp is a second
// declaration, free to drift from this block without the compiler noticing.
//
// THEY ARE EXPORTED SO A READER OUTSIDE THIS PACKAGE CAN CITE THEM TOO. A
// fixture elsewhere in the module that mirrors this vocabulary as a literal is
// that same second declaration wearing a different hat: it can spell a word
// this block does not contain, and nothing compares the two. The recipe help's
// fixture graph in cmd/knowledge/internal/tools is shaped like this emitter's
// output and compares its values against these names.
const (
	TitleSourceInfoDict     = "info_dict"
	TitleSourceFirstHeading = "first_heading"
	TitleSourceFilename     = "filename"
)

// deriveDocumentTitle answers what a reader is shown as this document's title
// and reports WHICH source answered: the Info dictionary's Title, else the
// first top-level section heading, else the basename with its extension
// removed. fileStem is total, so no leg returns "".
//
// THE GUARD TRIMS BUT THE RETURN DOES NOT: a real Info-dict title survives
// byte-identically, while a whitespace-only one falls through instead of
// rendering as a blank label. metadata.title records what the Info dict
// literally said; this decides what a reader is shown.
//
// TOP-LEVEL MEANS DIRECT CHILDREN OF THE ROOT — the loop never recurses into
// Children. The untagged path's top-level chunk is a text-less wrapper holding
// every classified block, so recursing would promote an arbitrary mid-document
// heading to the document's title.
func deriveDocumentTitle(meta pdf.Metadata, pdfPath string, chunks []pdf.Chunk) (string, string) {
	if strings.TrimSpace(meta.Title) != "" {
		return meta.Title, TitleSourceInfoDict
	}
	for _, c := range chunks {
		if nodeTypeForChunk(c) != "section" {
			continue
		}
		if heading := strings.TrimSpace(c.Text); heading != "" {
			return heading, TitleSourceFirstHeading
		}
	}
	return fileStem(pdfPath), TitleSourceFilename
}

// fileStem returns path's final element with its extension removed, trimmed.
// IT NEVER SLUG-IFIES: no lowercasing, no substitution, no hash suffix — this
// is a value a human reads as a title, not a graph name, which is why
// SourceSlug and sanitizeSlug are wrong here. The second arm makes it total: a
// final element that is all extension ("/.pdf") yields the full base, not "".
func fileStem(path string) string {
	base := filepath.Base(path)
	if stem := strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base))); stem != "" {
		return stem
	}
	return strings.TrimSpace(base)
}

// BuildDocumentBlurb concatenates the high-signal Info-dict fields into
// a single string so downstream BM25 indexes have text to match
// against. Empty fields are skipped.
//
// IT IS EXPORTED BECAUSE IT IS THE COMPOSITION, and a caller that needs to
// know what blurb a document root carries has to run it rather than restate
// it. The recipe help's fixture graph in cmd/knowledge/internal/tools composes
// its root's Content through this function from that root's own metadata, so a
// change to the ordering or the separators here reds there instead of leaving a
// hand-typed string behind.
//
// It opens on title — deriveDocumentTitle's answer — so a titleless document's
// blurb is labeled rather than starting at the author. The author and subject
// branches read the Info dict directly and are unchanged.
func BuildDocumentBlurb(meta pdf.Metadata, title string) string {
	var b strings.Builder
	if title != "" {
		b.WriteString(title)
	}
	if meta.Author != "" {
		if b.Len() > 0 {
			b.WriteString(" — ")
		}
		b.WriteString(meta.Author)
	}
	if meta.Subject != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(meta.Subject)
	}
	return b.String()
}
