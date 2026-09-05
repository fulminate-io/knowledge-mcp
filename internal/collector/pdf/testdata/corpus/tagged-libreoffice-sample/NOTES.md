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
- **Struct roles survive to the chunk** — the heading carries
  `struct_role: H1` and both paragraphs carry `P`. Nothing else in the
  corpus exercises a role at all.
- **Reading order out of the hybrid merge** — the three chunks are
  ordered heading, first paragraph, second paragraph. Structure-tree
  blocks and clustered residue are merged in PDF user space, where +y
  points up, so a merge sorted the wrong way returns the page
  bottom-first and this fixture is where that shows.
- **Mark-info /Marked = true** — pdfcpu validates the PDF as tagged.

## Threshold notes

Default thresholds pass against the regression-locked golden, with no
per-fixture override. The harness uses the canonical ChunkOptions
(LayoutParams + Classify defaults from `chunks_integration_test.go`).

The golden was RE-MARKED when the tagged structure-tree path became
reachable in production. It previously recorded ONE merged paragraph
chunk reading "Heading One First paragraph. Second paragraph.", which
is what the heuristic clusterer produced while nothing in the chunking
path ever called the structure-tree reader — the earlier revision of
these notes predicted the reshape. It now records the three chunks the
tagged path emits.

Two entries on the golden are load-bearing beyond kind and text: the
heading's `heading_level` and every chunk's `struct_role`. The harness
reads `heading_level` from the golden entry to score heading-level
agreement, so a golden marked with kind, text, page_range and bbox
alone scores a perfect chunk count, boundary IoU and classification
accuracy and still reds at `headingLevelAgreement 0.000 < threshold
0.850`.

`sections.golden.json` stays as it is — an empty array, as on every
fixture in the corpus.

## Source attribution

Synthesized via the project's own fixturelib (Apache-2.0). No
third-party redistribution constraints.
