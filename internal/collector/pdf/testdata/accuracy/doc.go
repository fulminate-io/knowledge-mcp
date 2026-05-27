// Package accuracy hosts shared metric helpers for the PDF
// validation corpus harness (T9). It lives under testdata/ so the Go
// tool excludes it from production builds via the testdata
// convention; tests under collector/pdf/ import it directly.
//
// The three helpers — WordLevenshtein, MeanCoverage,
// NormalizedKendallTau — replace shape-duplicates in
// collector/pdf/font/poppler_compat_test.go and
// collector/pdf/layout/pdfminer_xval_test.go (those test files keep
// their copies until their owning tickets migrate; T9 publishes the
// canonical helpers for the new corpus harness without forcing a
// concurrent edit).
//
// The package contains NO references to internal/pdfcpu and NO
// imports of the chunk/layout/text public packages — it operates
// purely on slice + float primitives so the corpus harness can use
// it without leaking PDF-specific types into the metric layer.
package accuracy
