# cidfont-identity-h

## What this fixture exercises

A 1-page synthetic PDF using a CIDFont (Type0) with `/Encoding`
`/Identity-H`. CIDFonts encode text as 2-byte CID indices rather than
single-byte character codes; the Identity-H encoding is the simplest
case (CID == byte sequence verbatim).

Source: `collector/pdf/testdata/cidfont_identity_h.pdf` — generated
by `collector/pdf/testdata/gen.go` via the T1 fixturelib.
`fontspec_type0.go` builds the CIDFont dict.

## Edge cases captured

- **2-byte CID decoding** — exercises the multi-byte string-show path
  in `collector/pdf/text/content_stream_show.go` and the CID font
  dispatch in `collector/pdf/font/cidfont.go`.
- **Identity-H mapping** — the simplest CMap; CID values pass
  through unchanged. Future fixtures will exercise non-trivial
  /CIDToGIDMap and /ToUnicode CMaps for CIDFonts.

## Threshold notes

Default thresholds pass against the regression-locked golden.

## Source attribution

Synthesized via the project's own fixturelib (Apache-2.0). No
third-party redistribution constraints.
