# multipage-cross-paragraph

## What this fixture exercises

A 3-page synthetic PDF where a single flowing paragraph spans the
page-1 → page-2 → page-3 boundaries. Each page shares the F1
(Helvetica) indirect ref via `WriteMultiPagePDF` shared-fonts.

This is the surface T8 (cross-page paragraph continuity) lands on.
T9 captures the current behavior — the chunker treats each page as
an independent unit and emits 3 separate paragraph chunks. When T8
ships, the golden updates to a single merged chunk with
`page_range: [0, 2]`.

## Edge cases captured

- **3-page document** — exercises the per-page chunker driver loop.
- **Same paragraph spanning a page boundary** — at T9 ship time the
  chunker emits 3 chunks; the surface for T8's continuity merge to
  reshape.
- **Shared font indirect refs across pages** — F1 (Helvetica) is
  registered ONCE and referenced from all 3 pages.

## Threshold notes

Default thresholds pass against the regression-locked golden. When
T8 lands and reshapes the chunker output, regenerate the golden
atomically with the chunker change.

## Source attribution

Synthesized via the project's own fixturelib (Apache-2.0). No
third-party redistribution constraints.
