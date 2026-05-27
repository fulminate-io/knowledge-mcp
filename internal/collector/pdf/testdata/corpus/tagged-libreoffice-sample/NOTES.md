# tagged-libreoffice-sample

## What this fixture exercises

A 1-page tagged PDF with a `/StructTreeRoot` that hierarchically
describes a Document → H1 + 2 paragraphs. `pdf.Document.IsTagged()`
returns `true`. Exercises the tagged-PDF read path including the
structure-tree walk in `collector/pdf/structtree/`.

## Synthesis path

**LibreOffice was not available in the implementer's environment.**
Per locked decision #6 (T9 plan), the fallback synthesis path is
`collector/pdf/testdata/fixturelib/tagged.go` (T6 ticket). This
fixture's `source.pdf` is the existing T1 synthesis output at
`collector/pdf/testdata/simple_tagged.pdf` — produced via
`fixturelib.WriteTaggedPDF` with a Document → H1 → P × 2 structure
tree. The directory name retains "libreoffice" per the original plan
locked-name; future curation may swap to a true LibreOffice-rendered
sample for fidelity, in which case `LICENSE` and these notes update.

## Edge cases captured

- **`/StructTreeRoot` parsing** — `Document.IsTagged()` returns true.
- **Heading classification via structure tree** — chunk[0] kind is
  `heading` because the structure tree explicitly labels it H1.
  When the chunker prefers structure-tree (default), tagged headings
  are recoverable without geometric heading-size inference.
- **Mark-info /Marked = true** — pdfcpu validates the PDF as tagged.

## Threshold notes

Default thresholds pass against the regression-locked golden. The
harness uses the canonical T9 ChunkOptions (LayoutParams + Classify
defaults from `chunks_integration_test.go:29-36`); these options
merge the H1 + 2 paragraphs into a single paragraph chunk, which the
golden captures verbatim. A future T6+ change that preserves the
H1-as-heading distinction will reshape the golden.

## Source attribution

Synthesized via the project's own fixturelib (Apache-2.0). No
third-party redistribution constraints.
