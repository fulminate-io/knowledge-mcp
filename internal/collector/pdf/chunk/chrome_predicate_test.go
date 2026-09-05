package chunk

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// chrome_predicate_test.go — the stamped signals must be a SUFFICIENT
// STATISTIC for the rule they replaced.
//
// Retention only holds its promise if a consumer reading the stamped
// metadata can reach the same verdict the deleting detector reached.
// This drives the case that separates a correct stamp from a lossy one:
// a fingerprint that is shaped on some pages and not on others.
//
// The in-repo rfc-7234-caching fixture cannot witness it. Measured, its
// running headers read "RFC 7234 / HTTP-1.1 Caching / June 2014" with
// no pipe separator, so it contains ZERO shaped fingerprints,
// chrome_repeat_shaped is constant false across all 453 nodes, and the
// retired rule's three inputs collapse to one. Both a correct stamp and
// a per-occurrence one score identically there.

// chromeCoord is a (page, block) coordinate, matching the shape
// dropSetFromChromeIndex returns.
type chromeCoord = struct{ page, blk int }

// sharedFingerprintPages builds three pages carrying ONE fingerprint in
// two guises: the chapter title on its own title page, unshaped, and
// the same words running as a "<text> | <pagenum>" header on the two
// pages after it. Digit normalization and pipe-edge stripping collapse
// all three to the same fingerprint.
//
// This is the exact shape the retired rule's sparing clause was written
// for: the fingerprint qualifies as chrome, but the title-page
// occurrence is NOT itself shaped and must survive.
func sharedFingerprintPages() [][]layout.Block {
	title := mkBlock(layout.BlockHeading, txtRun("The Origins of Streaming"))
	title.PageIndex = 0

	hdr1 := mkBlock(layout.BlockParagraph, txtRun("The Origins of Streaming | 11"))
	hdr1.PageIndex = 1

	hdr2 := mkBlock(layout.BlockParagraph, txtRun("The Origins of Streaming | 12"))
	hdr2.PageIndex = 2

	return [][]layout.Block{{title}, {hdr1}, {hdr2}}
}

// predicateSetFromStamps applies the exported predicate to every
// stamped block and returns the coordinates it selects.
func predicateSetFromStamps(perPage [][]layout.Block) map[chromeCoord]struct{} {
	out := make(map[chromeCoord]struct{})
	for pi, page := range perPage {
		for bi := range page {
			if IsPageChrome(page[bi].Metadata) {
				out[chromeCoord{pi, bi}] = struct{}{}
			}
		}
	}
	return out
}

func TestChromeRetention_StampedPredicateReproducesTheRetiredDropSet(t *testing.T) {
	t.Parallel()

	// The oracle: what the deleting detector would have removed, read
	// from the fingerprint index rather than from any stamp.
	oracle := dropSetFromChromeIndex(indexChromeFingerprints(sharedFingerprintPages()))

	// The subject: what a consumer reaches from the stamped metadata
	// alone.
	stamped := sharedFingerprintPages()
	stampRepeatedChrome(stamped)
	got := predicateSetFromStamps(stamped)

	t.Logf("retired drop set = %v", oracle)
	t.Logf("stamped predicate = %v", got)

	// EXTERNAL EXPECTATION on the oracle itself. Two sets that lost the
	// same members are still equal, so the equality below is only
	// meaningful once the oracle is pinned to a value derived from the
	// fixture rather than from either implementation: the two running
	// headers are chrome and the title-page heading is not.
	wantOracle := map[chromeCoord]struct{}{{1, 0}: {}, {2, 0}: {}}
	if !sameCoordSet(oracle, wantOracle) {
		t.Fatalf("retired rule selected %v, want %v (the two shaped running headers, sparing the title-page heading) - the fixture does not exercise the sparing clause", oracle, wantOracle)
	}

	if !sameCoordSet(got, oracle) {
		t.Errorf("predicate selected %d coordinates %v, the retired rule selected %d %v - the stamped metadata is not a sufficient statistic for the rule it replaced",
			len(got), got, len(oracle), oracle)
	}

	// The fingerprint-level key is what carries the sparing clause. A
	// per-occurrence write would leave the title page reading false and
	// send it over the unshaped threshold instead.
	title := stamped[0][0]
	if got := title.Metadata[ChromeKeyRepeatShaped]; got != "true" {
		t.Errorf("the unshaped title-page occurrence carries %s=%q, want \"true\" - the key is a FINGERPRINT-level fact, not a per-occurrence one", ChromeKeyRepeatShaped, got)
	}
	if got := title.Metadata[ChromeKeyShape]; got != "" {
		t.Errorf("the unshaped title-page occurrence carries %s=%q, want empty - only a shaped OCCURRENCE gets a shape", ChromeKeyShape, got)
	}
	if got := stamped[1][0].Metadata[ChromeKeyShape]; got != chromeShapePageNumberPipe {
		t.Errorf("the shaped running header carries %s=%q, want %q", ChromeKeyShape, got, chromeShapePageNumberPipe)
	}
}

func sameCoordSet(a, b map[chromeCoord]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// TestChromeRetention_ASplitFooterLosesItsShape is the downstream half
// of the residue-scale fix, driven through the real stamping pipeline.
//
// hasChromeShape requires a single line in a single block. A running
// footer rendered as two runs on one baseline — "Chapter 3 | " and
// "42" — therefore only registers as chrome-shaped if the grouper kept
// it whole. While the structure-tree residue was clustered at PAGE
// scale it did not: the few-runs guard emitted one block per run, and
// the Phase 4 chrome deliverable was silently disabled for exactly the
// footers it targets.
//
// This drives both shapes through stampRepeatedChrome and asserts the
// difference, which ESTABLISHES THE CONSEQUENCE: it shows what a split
// footer costs a consumer. It does NOT observe the grouper, because it
// builds both block shapes by hand, so it passes on a tree where the
// residue is still clustered at page scale. The gate that observes the
// grouper is TestHybridFallback_ResidueIsGroupedAtElementScale; the two
// compose, and neither substitutes for the other.
func TestChromeRetention_ASplitFooterLosesItsShape(t *testing.T) {
	t.Parallel()

	footerPages := func(split bool) [][]layout.Block {
		var perPage [][]layout.Block
		for page := range 2 {
			if split {
				a := mkBlock(layout.BlockParagraph, txtRun("Chapter 3 | "))
				a.PageIndex = page
				b := mkBlock(layout.BlockParagraph, txtRun("42"))
				b.PageIndex = page
				perPage = append(perPage, []layout.Block{a, b})
				continue
			}
			whole := mkBlock(layout.BlockParagraph, txtRun("Chapter 3 | 42"))
			whole.PageIndex = page
			perPage = append(perPage, []layout.Block{whole})
		}
		return perPage
	}

	// WHOLE: the footer the element-scale grouper now produces.
	whole := footerPages(false)
	stampRepeatedChrome(whole)
	md := whole[0][0].Metadata
	if md[ChromeKeyRepeatShaped] != "true" {
		t.Errorf("whole footer: %s = %q, want \"true\"", ChromeKeyRepeatShaped, md[ChromeKeyRepeatShaped])
	}
	if md[ChromeKeyShape] != chromeShapePageNumberPipe {
		t.Errorf("whole footer: %s = %q, want %q", ChromeKeyShape, md[ChromeKeyShape], chromeShapePageNumberPipe)
	}
	if !IsPageChrome(md) {
		t.Errorf("whole footer is not recognized as page chrome: %v", md)
	}
	t.Logf("whole footer stamped: %v", md)

	// SPLIT: what the page-scale guard produced before the fix. The
	// halves still repeat across pages, so they may carry a repeat
	// count — but neither is shaped, so the retired rule's shaped
	// threshold and sparing clause cannot apply and the footer is not
	// recognized as the running header it is.
	split := footerPages(true)
	stampRepeatedChrome(split)
	for i, b := range split[0] {
		if got := b.Metadata[ChromeKeyShape]; got != "" {
			t.Errorf("split footer half %d carries %s = %q, want empty - a half footer cannot match the shape", i, ChromeKeyShape, got)
		}
		if b.Metadata[ChromeKeyRepeatShaped] == "true" {
			t.Errorf("split footer half %d carries %s = true, want false", i, ChromeKeyRepeatShaped)
		}
	}
	t.Logf("split footer halves stamped: %v / %v", split[0][0].Metadata, split[0][1].Metadata)
}
