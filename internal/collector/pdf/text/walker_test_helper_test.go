package text

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// extractFromBytes drives the content-stream walker against a literal
// content-stream body without going through pdfcpu. The helper
// pre-populates the walker's font cache so Tf operators find the
// resolved font without calling page.ResolvedFont. The walker's page
// is left nil; operators that need the page handle (Do, which calls
// page.XObjectKind) cannot be exercised through this helper — Phase
// 10's golden tests cover the Do path against real fixtures.
func extractFromBytes(
	t *testing.T,
	body []byte,
	fonts map[string]*internalpdf.ResolvedFont,
	opts ExtractOptions,
) []TextRun {
	t.Helper()
	w := newWalker(nil, opts)
	maps.Copy(w.fontCache, fonts)
	if err := w.run(body); err != nil {
		t.Fatalf("walker.run(%q): %v", body, err)
	}
	return w.runs
}

// captureLogs swaps the default slog handler for the duration of fn
// and returns the buffer contents (one record per line). Used by
// per-operator tests that assert on warning-log emission (operand
// underflow, MCID depth-cap, Form XObject skip).
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(prev)

	_ = context.Background()
	fn()
	return buf.String()
}

// helveticaF1 is a stock font resource used by per-operator tests
// that need a non-nil applyTf result. Wraps the lightweight
// FontResource in a ResolvedFont (per Phase 10 type promotion);
// embedded-struct field reads (Subtype, BaseFont, etc.) work via
// Go's field promotion.
func helveticaF1() *internalpdf.ResolvedFont {
	return &internalpdf.ResolvedFont{
		FontResource: &internalpdf.FontResource{
			Key:      "F1",
			BaseFont: "Helvetica",
			Subtype:  "Type1",
		},
	}
}

// type3F2 is a Type3 font resource used by Phase 8's suppression
// tests.
func type3F2() *internalpdf.ResolvedFont {
	return &internalpdf.ResolvedFont{
		FontResource: &internalpdf.FontResource{
			Key:      "F2",
			BaseFont: "MyType3Font",
			Subtype:  "Type3",
		},
	}
}
