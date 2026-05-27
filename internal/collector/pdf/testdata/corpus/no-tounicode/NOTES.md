# no-tounicode

## What this fixture exercises

A 1-page synthetic PDF with a Type1 font that lacks a `/ToUnicode`
CMap. Decoding falls back to the base font's encoding (WinAnsi in
this case). The chunker must produce text by going through the
encoding path rather than the ToUnicode shortcut.

Source: `collector/pdf/testdata/no_tounicode_winansi.pdf` — generated
by `collector/pdf/testdata/gen.go` via the T1 fixturelib.
`fontspec.go:NoToUnicodeWinAnsi` builds the font dict.

## Edge cases captured

- **Missing `/ToUnicode`** — exercises the encoding-fallback codepath
  in `collector/pdf/font/resolver.go`.
- **WinAnsi base encoding** — distinct from Standard / MacRoman /
  MacExpert; the corpus probes all three encoding families across
  separate fixtures (this one is WinAnsi; future T9 fixtures expand).

## Threshold notes

Default thresholds pass — this fixture's chunker output is
small (1 chunk, text "Hello, T1 - winansi via direct chars" or
similar). No per-fixture override needed.

## Source attribution

Synthesized via the project's own fixturelib (Apache-2.0). No
third-party redistribution constraints.
