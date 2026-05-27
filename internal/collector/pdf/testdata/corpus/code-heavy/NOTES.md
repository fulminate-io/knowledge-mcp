# code-heavy

## What this fixture exercises

A 1-page synthetic PDF alternating prose paragraphs (Helvetica F1)
with a monospace-font code block (Courier F2). Exercises the
classifier's code-block heuristic (`collector/pdf/classify/`),
which keys on the monospace-font ratio.

## Edge cases captured

- **F1/F2 font switching mid-page** — exercises the per-text-run
  font-key tracking in the chunker.
- **Code-block kind classification** — the Courier-rendered run is
  emitted with `kind: code_block` while the surrounding prose uses
  `kind: paragraph`.
- **Paragraph-code-paragraph alternation** — the chunker emits 3
  chunks in source-order: prose, code, prose.

## Threshold notes

Default thresholds pass against the regression-locked golden.

## Source attribution

Synthesized via the project's own fixturelib (Apache-2.0). No
third-party redistribution constraints.
