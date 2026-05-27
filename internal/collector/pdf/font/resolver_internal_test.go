package font

import (
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

// TestDecode_Standard14_ImplicitStandardEncoding (per T3-7): a Standard
// 14 font with NO /ToUnicode AND NO /Encoding should default to
// /StandardEncoding per PDF 32000-1:2008 §9.6.2.2. We construct the
// case synthetically — the existing onepage.pdf fixture has /Encoding
// /WinAnsiEncoding emitted by pdfcpu, so we feed a hand-made
// ResolvedFont through buildDecoder directly.
//
// Lives in resolver_internal_test.go (package font, not font_test) so
// it can reach the package-private buildDecoder + fontDecoder fields.
func TestDecode_Standard14_ImplicitStandardEncoding(t *testing.T) {
	t.Parallel()
	rf := &internalpdf.ResolvedFont{
		FontResource: &internalpdf.FontResource{BaseFont: "Helvetica", Subtype: "Type1"},
	}
	d, err := buildDecoder(rf)
	if err != nil {
		t.Fatalf("buildDecoder: %v", err)
	}
	if !d.hasEnc {
		t.Fatalf("hasEnc: false, want true (StandardEncoding default)")
	}
	// 0x41 in StandardEncoding is "A" → AGL → 'A'.
	if got, ok := d.decode(0x41); !ok || len(got) != 1 || got[0] != 'A' {
		t.Errorf("decode(0x41): got %v ok=%v, want ['A']", got, ok)
	}
	// 0x20 → "space" → AGL → ' '.
	if got, ok := d.decode(0x20); !ok || len(got) != 1 || got[0] != ' ' {
		t.Errorf("decode(0x20): got %v ok=%v, want [' ']", got, ok)
	}
}
