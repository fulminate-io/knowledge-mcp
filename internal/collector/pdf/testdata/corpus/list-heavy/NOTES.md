# list-heavy

## What this fixture exercises

A 1-page synthetic PDF with three list types: bullet (`*` prefix),
numbered (`1.` / `2.` / `3.` prefix), and a hyphen-prefixed pseudo-
nested list. Exercises the classifier's list-item heuristic
(`collector/pdf/classify/`).

## Edge cases captured

- **`* `-prefix bullets** — classified as `list-item`.
- **`1.`-prefix numbered items** — classified as `list-item`.
- **`- `-prefix items** — currently classified as `paragraph` (the
  classifier's heuristic doesn't yet recognize hyphen-prefixed
  ordered lists). This is captured in the regression-locked golden;
  a future heuristic improvement that adds hyphen recognition will
  reshape the golden.

## Threshold notes

Default thresholds pass against the regression-locked golden.

## Source attribution

Synthesized via the project's own fixturelib (Apache-2.0). No
third-party redistribution constraints.
