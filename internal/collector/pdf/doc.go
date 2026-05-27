// Package pdf provides layout-aware text extraction for born-digital PDFs.
//
// V1 scope is intentionally narrow:
//   - Born-digital PDFs only — no OCR, no scanned-page rasterization.
//   - No chart / figure / handwriting analysis.
//   - No table-grid reconstruction (only paragraph + heading + list flow).
//
// Anything outside that envelope returns ErrNotImplemented or an empty result;
// scope expansion is deliberately deferred to follow-up tickets.
//
// Sub-package layout:
//
//   - internal/pdfcpu — confined wrapper over github.com/pdfcpu/pdfcpu. The
//     ONLY package allowed to import pdfcpu directly. Verified by a confinement
//     audit script.
//   - text         — TextRun + content-stream walker (filled by T2).
//   - font         — CMap / Encoding / glyph→Unicode mapping (filled by T3).
//   - layout       — Block / Line / BlockKind plus geometric grouping (T4).
//   - structtree   — PDF structure-tree (tagged-PDF) reader (filled by T6).
//   - classify     — heading / list / caption classifier (filled by T5).
//   - chunk        — output chunker that flattens layout blocks into export
//     records (filled by T7).
//
// Consumers import this top-level package. High-traffic types (TextRun,
// Block, Line, BlockKind, Chunk, Mode, etc.) are re-exported via Go type
// aliases in aliases.go so callers do not need to import the subsystem
// packages directly.
package pdf
