package pdf_test

import (
	"strconv"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
)

// chrome_retention_test.go — what the collector emits now that running
// page chrome is stamped instead of deleted.
//
// The rule the collector used to apply is RESTATED HERE rather than
// read from the collector's own exported predicate. That is deliberate:
// if the test called pdf.IsPageChrome, a change to the predicate would
// move the test's expectation along with it and the gate would agree
// with whatever the collector currently does. Restating it pins the
// population against an expectation the collector cannot edit.

// retiredChromeRule reports whether a chunk's stamped metadata
// satisfies the deletion rule the collector applied before retention:
// a fingerprint recurring on three pages, or on two when the
// fingerprint is shaped like a running header, and — when it is shaped
// — only for the occurrences that are themselves shaped, which is what
// spared a chapter title-page heading whose text also ran as a header.
func retiredChromeRule(md map[string]string) bool {
	count, err := strconv.Atoi(md["page_repeat_count"])
	if err != nil {
		return false
	}
	repeatShaped := md["chrome_repeat_shaped"] == "true"
	threshold := 3
	if repeatShaped {
		threshold = 2
	}
	if count < threshold {
		return false
	}
	return !repeatShaped || md["chrome_shape"] != ""
}

// TestChromeRetention_StampsExactlyWhatDeletionRemoved asserts the
// stamp is a faithful replacement for the deletion: the same 92 blocks
// the old rule removed from rfc-7234-caching now carry a stamp
// satisfying that rule, and the same 361 it kept do not.
//
// Equality of the two counts is the point. A stamp that fired more
// broadly would push retained content into any consumer's chrome
// filter; one that fired more narrowly would leave running headers in
// the substantive population. Either way the retention would not be the
// lossless swap it claims to be.
func TestChromeRetention_StampsExactlyWhatDeletionRemoved(t *testing.T) {
	t.Parallel()
	doc, err := pdf.OpenFile("testdata/corpus/rfc-7234-caching/source.pdf")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()

	chunks, err := doc.Chunks(pdf.ChunkOptions{
		Mode: pdf.ChunkModeParagraph,
		LayoutParams: pdf.LayoutParams{
			LineMargin: 0.4, CharMargin: 2.0, WordMargin: 0.1,
			BoxesFlow: 0.5, ParagraphGapRatio: 1.6,
		},
		ClassifyParams: pdf.ClassifyParams{
			HeadingFontSizeRatio: 1.15, HeadingMinBoldOnly: true,
			CodeMonospaceRatio: 0.8,
		},
		MinChunkChars: 0,
	})
	if err != nil {
		t.Fatalf("doc.Chunks: %v", err)
	}

	const (
		wantTotal    = 453 // everything the collector now emits
		wantStamped  = 92  // what the retired rule deleted
		wantRetained = 361 // what the pre-change collector emitted
	)

	stamped, retained, maxRepeat := 0, 0, 0
	for _, c := range chunks {
		if !retiredChromeRule(c.Metadata) {
			retained++
			continue
		}
		stamped++
		raw, ok := c.Metadata["page_repeat_count"]
		if !ok {
			t.Errorf("chunk on pages %v satisfies the retired rule but carries no page_repeat_count", c.PageRange)
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			t.Errorf("page_repeat_count = %q does not parse: %v", raw, err)
			continue
		}
		if n < 2 {
			t.Errorf("page_repeat_count = %d on a chrome chunk, want at least 2", n)
		}
		if n > maxRepeat {
			maxRepeat = n
		}
	}

	t.Logf("total=%d stamped=%d retained=%d maxRepeat=%d", len(chunks), stamped, retained, maxRepeat)

	if len(chunks) != wantTotal {
		t.Errorf("emitted %d chunks, want %d - retention is not emitting the full block population", len(chunks), wantTotal)
	}
	if stamped != wantStamped {
		t.Errorf("stamped %d chunks as repeated chrome, want %d - the stamp does not reproduce what deletion removed", stamped, wantStamped)
	}
	if retained != wantRetained {
		t.Errorf("left %d chunks unstamped, want %d - the substantive population moved", retained, wantRetained)
	}
	// KNOWN-POSITIVE for the counter itself: a counter that was
	// declared but never incremented would sit at the minimum on every
	// chrome chunk, which is indistinguishable from a genuinely
	// twice-repeated header without this.
	if maxRepeat <= 2 {
		t.Errorf("highest page_repeat_count on a 43-page document = %d, want > 2 - the counter looks defaulted rather than computed", maxRepeat)
	}
}
