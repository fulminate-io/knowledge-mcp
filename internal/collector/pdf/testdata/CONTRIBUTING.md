# Adding a fixture to the PDF validation corpus

This document is the authoring workflow for fixtures under
`collector/pdf/testdata/corpus/`. The corpus drives `TestAccuracy_AllFixtures`
in `collector/pdf/accuracy_test.go`. Each fixture is a directory; the
harness walks every subdirectory and scores the chunker output against
hand-marked golden files.

## 1. Choosing a fixture source

Fixtures must be **public-domain** or licensed under a license that
permits redistribution and modification (CC0, CC-BY, CC-BY-SA, US
Government Works, RFC IETF Trust Legal Provisions). Allowed sources:

- **RFCs** — IETF Trust Legal Provisions (TLP). Cite exact RFC number.
- **arXiv papers** — CC-BY 4.0 or CC0 only (verify via the paper's
  abstract page; arXiv defaults are NOT redistributable).
- **USGS / NIST reports** — US Government Works, 17 USC §105.
- **CC-licensed academic papers / preprints** — verify license per
  paper.
- **Synthesized via fixturelib** — `collector/pdf/testdata/fixturelib/`
  reproducible builders (no external sourcing).

**Size caps:**

- Single fixture PDF ≤ 500 KB.
- Aggregate corpus across all fixtures ≤ 10 MB.

**Eight ticket categories** (see FUL-83):

1. Caching/HTTP RFC (rfc-7234-caching).
2. Two-column academic (arxiv-paper-sample).
3. Single-column technical report (usgs-report-sample).
4. Tagged PDF (tagged-libreoffice-sample).
5. Encoding edge cases — synthetic via fixturelib (no-tounicode,
   differences-array, cidfont-identity-h).
6. Multi-page cross-paragraph (multipage-cross-paragraph).
7. List-heavy (list-heavy).
8. Code-heavy (code-heavy).

## 2. Creating the fixture directory

The canonical fixture shape is **5 files**:

```
collector/pdf/testdata/corpus/<fixture-name>/
  source.pdf            — the PDF being validated
  LICENSE               — license + attribution + source URL
  chunks.golden.json    — hand-marked chunks (paragraph mode)
  sections.golden.json  — hand-marked sections (section mode)
  NOTES.md              — what the fixture exercises (prose)
```

Fixture directory names are kebab-case lowercase (e.g.
`rfc-7234-caching`). The harness uses the directory basename as the
`t.Run` sub-test name.

**Canonical-shape exception: `poppler-references/` subdirectory.** When
a fixture exists for the express purpose of cross-validating against
poppler's pre-baked text output (e.g. `rfc-7234-caching/` carries the
historical `*.pdftotext.txt` and `*.pdftotext-bbox.txt` references used
by `font/poppler_compat_test.go` and `text/charbounds_pdftotext_test.go`),
those reference text files live under
`<fixture>/poppler-references/`. The 5-file canonical shape is intact
in the parent directory; the `poppler-references/` subdirectory is
ignored by `discoverFixtures` (it has no `source.pdf`) and is the
only documented exception. New fixtures should NOT add this
subdirectory unless they have a specific cross-validation consumer.

## 3. LICENSE file format

5-line plain text, no headers. Examples:

**RFC (rfc-7234-caching):**

```
RFC 7234 - Hypertext Transfer Protocol (HTTP/1.1): Caching
Authors: R. Fielding (ed.), M. Nottingham (ed.), J. Reschke (ed.)
Date: June 2014
License: IETF Trust Legal Provisions (TLP); see https://trustee.ietf.org/license-info
Source: https://www.rfc-editor.org/rfc/rfc7234.txt
```

**arXiv paper:**

```
<paper title>
Authors: <author list>
Date: <YYYY-MM>
License: CC-BY 4.0
Source: https://arxiv.org/abs/<paper id>
```

**USGS / NIST report:**

```
<report title>
Authors: <author list>
Date: <YYYY-MM>
License: US Government Works, 17 USC §105
Source: https://www.usgs.gov/<report URL>
```

## 4. Hand-marking `chunks.golden.json`

The golden file's JSON shape is defined in
`collector/pdf/accuracy_golden_test.go`. Top-level structure:

```json
{
  "schema_version": 1,
  "thresholds": { "...optional overrides..." },
  "chunks": [
    {
      "kind": "paragraph",
      "text": "...",
      "page_range": [0, 0],
      "bbox": [72, 700, 540, 720]
    }
  ]
}
```

**Required fields** on each chunk: `kind` (string), `text` (string),
`page_range` ([first, last] 0-indexed inclusive), `bbox`
([x0, y0, x1, y1] in PDF user-space).

**Optional fields:** `heading_level` (int, 1=top), `struct_role`
(string, for tagged PDFs), `children` (recursive — used in section
mode).

**Kind values:** `heading`, `paragraph`, `code`, `list-item`, `table`,
`unknown` (matches `layout.BlockKind`).

**Per-fixture thresholds** override defaults. Pointer-fields — omit any
field to use the default:

| Threshold                          | Default | Direction |
| ---------------------------------- | ------- | --------- |
| `chunk_count_delta_max`            | 0.10    | lower-better |
| `boundary_iou_min`                 | 0.85    | higher-better |
| `classification_accuracy_min`      | 0.90    | higher-better |
| `heading_level_agreement_min`      | 0.85    | higher-better |
| `reading_order_kendall_tau_max`    | 0.10    | lower-better |
| `text_similarity_levenshtein_max`  | 0.05    | lower-better |

## 5. Hand-marking `sections.golden.json`

Same envelope as `chunks.golden.json` but the elements are
`goldenSection` (title + level + page_range + optional bbox + recursive
children). Used by tests that exercise `chunk.ModeSection` grouping.

## 6. Authoring `NOTES.md`

Prose describing the fixture: what it exercises, why it's interesting,
what edge cases it captures. Include observed quirks of the source PDF
(e.g. "fixture relies on Type1 Differences override"; "header text on
every page is identical and should be filtered when SkipHeadersFooters
lands"). Future fixture maintainers read this.

## 7. Running the harness

Default-on (no build tag):

```bash
unset GOROOT && CGO_ENABLED=1 /opt/homebrew/bin/go test \
    -count=1 -run TestAccuracy_AllFixtures -v \
    ./collector/pdf/
```

The `-v` flag emits one metric line per fixture:

```
fixture=<name> actual=N golden=M chunkCountDelta=... boundaryIoU=...
    classAcc=... headLvlAgree=... kendallDiv=... textLev=...
```

**Threshold failure handling:** if a default threshold trips, decide:

1. **Regression?** The chunker change broke a previously-passing
   metric. Fix the chunker.
2. **Expected divergence?** The fixture exercises a known limitation
   (e.g. encoding edge cases). Add a per-fixture `thresholds` block
   in `chunks.golden.json` overriding the failing default. Document
   the override in `NOTES.md`.

Per locked decision #4 (FUL-83): failing-default fixtures get a
per-fixture override; do NOT lower the global defaults without a
deliberate ticket.

## 8. Optional poppler cross-validation

Build-tag-gated harness shells out to `pdftotext` and compares
concatenated chunk text against poppler's reference output. Requires
poppler installed locally; skipped on absence:

```bash
unset GOROOT && CGO_ENABLED=1 /opt/homebrew/bin/go test \
    -tags pdfcompare_poppler -count=1 -v ./collector/pdf/
```

Threshold: 0.10 word-level edit distance ratio. Looser than the
default-on 0.05 to account for pdftotext's hard-newline + page-break
formatting differences from our chunk-stream concatenation.

**Per-fixture poppler opt-out.** Fixtures that intentionally exercise
our-vs-poppler divergence (e.g. encoding edge cases where our chunker
resolves a glyph that poppler drops to a fallback) carry a
`.skip-poppler` peer file in the fixture directory containing a
one-line reason. The poppler harness honors this marker via
`t.Skipf`; the default-on harness ignores it. See
`collector/pdf/testdata/corpus/differences-array/.skip-poppler` for
the canonical example.

## 9. Atomic-commit policy

Per the project's atomic-changesets discipline: **one fixture = one
commit**. The PDF, LICENSE, both golden files, and `NOTES.md` ship in
a single commit so reviewers see the whole fixture and the corpus
remains git-bisectable. Fixture migrations (e.g. moving `rfc7234.pdf`
from flat-corpus to per-fixture directory) similarly bundle the move
with all caller updates.

## 10. Standing rules

- **License attribution is mandatory.** Every fixture's `LICENSE` file
  must name the original work, the author/issuing org, the date, the
  exact license, and a source URL.
- **No third-party PDF library names** in fixture comments, NOTES, or
  golden files. Stick to neutral terminology — the validation harness
  is library-agnostic.
- **No back-compat shims.** Fixtures and goldens are recreated when
  the schema changes; no migration paths in code.
- **Fixtures awaiting human curation** use a `.skip` marker file in the
  fixture directory containing a one-line reason. The harness emits
  `t.Skipf` on these so the case stays visible under `-v` but doesn't
  fail CI.
