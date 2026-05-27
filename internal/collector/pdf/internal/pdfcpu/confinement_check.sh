#!/usr/bin/env bash
# confinement_check.sh — verify that github.com/pdfcpu/pdfcpu is imported
# ONLY by packages under cmd/knowledge/internal/collector/pdf/internal/pdfcpu/.
# T1 ticket DoD requires this; the runtime build must keep pdfcpu confined to
# the wrapper so we can swap it out without touching every sub-package.
#
# Run from repo root: ./cmd/knowledge/internal/collector/pdf/internal/pdfcpu/confinement_check.sh
# Exit 0 on clean, exit 1 with a diagnostic on violation.

set -euo pipefail

# Locate the Go binary the same way Makefile / CLAUDE.md does — the
# user's machine has a stale GOROOT pointing at sdk/go1.25.7 but the
# binary is 1.26.1. Unset GOROOT and use the homebrew binary.
unset GOROOT
GO=${GO:-/opt/homebrew/bin/go}
if ! command -v "$GO" >/dev/null 2>&1; then
  GO=go
fi

# Audit 1: literal grep. Must return empty (modulo the internal wrapper
# package and the build-tag-ignored testdata generator + fixturelib).
# fixturelib lives under testdata/ (excluded from production builds via
# Go's testdata convention) so its pdfcpu imports don't pollute the
# runtime confinement boundary; same for the regen_widths regenerator.
#
# Pathspec scoped to *.go files only — provenance comments in .dat/.md/.txt
# data files are documentation, not imports, and would produce false
# positives. Confinement is a property of Go source, not data assets.
violations=$(git grep -E 'pdfcpu/pdfcpu' -- 'cmd/knowledge/internal/collector/pdf/**/*.go' \
  | grep -v '^cmd/knowledge/internal/collector/pdf/internal/pdfcpu/' \
  | grep -v '^cmd/knowledge/internal/collector/pdf/testdata/gen\.go' \
  | grep -v '^cmd/knowledge/internal/collector/pdf/testdata/fixturelib/' \
  | grep -v '^cmd/knowledge/internal/collector/pdf/font/testdata/regen_widths\.go' \
  | grep -v '^cmd/knowledge/internal/collector/pdf/doc\.go.*//.*pdfcpu/pdfcpu' \
  || true)
if [[ -n "$violations" ]]; then
  echo "FAIL: pdfcpu imports found outside cmd/knowledge/internal/collector/pdf/internal/pdfcpu/" >&2
  echo "$violations" >&2
  exit 1
fi

# Audit 2: go list dependency audit. Walks every package under
# cmd/knowledge/internal/collector/pdf/... and asserts no direct pdfcpu import
# on packages outside internal/pdfcpu. Catches the indirect-via-non-internal
# route the literal grep would miss.
go_violations=$("$GO" list -f '{{.ImportPath}}: {{.Imports}}' ./cmd/knowledge/internal/collector/pdf/... 2>/dev/null \
  | grep -v '^github.com/fulminate-io/knowledge/cmd/knowledge/internal/collector/pdf/internal/pdfcpu' \
  | grep 'github.com/pdfcpu/pdfcpu' \
  || true)
if [[ -n "$go_violations" ]]; then
  echo "FAIL: go list reports pdfcpu imports outside internal/pdfcpu" >&2
  echo "$go_violations" >&2
  exit 1
fi

echo "OK: pdfcpu imports are confined to cmd/knowledge/internal/collector/pdf/internal/pdfcpu/"
