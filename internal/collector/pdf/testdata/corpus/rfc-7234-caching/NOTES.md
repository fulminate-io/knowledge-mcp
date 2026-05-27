# rfc-7234-caching

## What this fixture exercises

RFC 7234 ("Hypertext Transfer Protocol (HTTP/1.1): Caching", June 2014)
rendered as a 43-page PDF via the rfc-editor.org tool. Single-column
flowing prose with section headings, code blocks, and the standard RFC
running header/footer per page.

This is the largest fixture in the corpus and the canonical "real
document" stress test. The chunker emits 495 paragraph-mode chunks
across all 43 pages.

## Edge cases captured

- **Running headers/footers on every page** — "RFC 7234 HTTP/1.1
  Caching June 2014" at the top and "Fielding, et al. Standards
  Track [Page N]" at the bottom of each page surface as
  paragraph-kind chunks today. When T5 (header/footer detection) lands,
  the chunker will filter these and the golden file gets regenerated.
- **Code-block classification** — the chunker currently labels several
  body paragraphs as `code_block` (kind heuristic catches their tighter
  line spacing). This is a known limitation; the regression-locked
  golden captures it so future heuristic improvements show up as
  measurable changes rather than silent drift.
- **Multi-line paragraphs across body, indented blocks, and quoted
  blocks** — exercises the layout grouper's paragraph segmentation.

## Threshold notes

All six metrics pass at default thresholds against the regression-
locked golden. No per-fixture threshold override is needed.

If a future T7+ chunker change deliberately reshapes the output, the
expected workflow is:

1. Confirm the change is intentional (not a regression).
2. Regenerate `chunks.golden.json` from the new chunker output.
3. Land the chunker change + golden update in the same atomic commit.

## Source attribution

See `LICENSE`. The PDF is the rfc-editor.org rendering of RFC 7234.
The peer text references in `poppler-references/` (`source.pdftotext.txt`
and `source.pdftotext-bbox.txt`) are pre-baked outputs from
`pdftotext -layout` and `pdftotext -bbox -layout` respectively, used
by `collector/pdf/font/poppler_compat_test.go` and
`collector/pdf/text/charbounds_pdftotext_test.go`. Regenerate via the
commands at the top of those test files.

The `poppler-references/` subdirectory is the canonical-shape exception
documented in `collector/pdf/testdata/CONTRIBUTING.md` Section 2.
