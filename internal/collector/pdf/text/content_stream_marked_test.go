package text

import (
	"strings"
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

func TestExtract_BMC_BDC_EMC_PropagatesMCID(t *testing.T) {
	t.Parallel()
	body := []byte(`/Span /P << /MCID 5 >> BDC BT /F1 12 Tf 0 0 Td (tagged) Tj ET EMC BT /F1 12 Tf 0 0 Td (untagged) Tj ET`)
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 2 {
		t.Fatalf("got %d, want 2", len(runs))
	}
	if runs[0].MCID != 5 {
		t.Errorf("BDC tagged MCID: got %d, want 5", runs[0].MCID)
	}
	if runs[1].MCID != 0 {
		t.Errorf("after EMC MCID: got %d, want 0", runs[1].MCID)
	}
}

func TestExtract_BMC_PropagatesParentMCID(t *testing.T) {
	t.Parallel()
	body := []byte(`/P << /MCID 7 >> BDC /Span BMC BT /F1 12 Tf (deep) Tj ET EMC EMC`)
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 1 {
		t.Fatalf("got %d, want 1", len(runs))
	}
	if runs[0].MCID != 7 {
		t.Errorf("BMC inside BDC: got MCID=%d, want 7 (propagated parent)", runs[0].MCID)
	}
}

func TestExtract_EmptyTj_Skipped(t *testing.T) {
	t.Parallel()
	body := []byte("BT /F1 12 Tf 0 0 Td () Tj ET")
	runs := extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	if len(runs) != 0 {
		t.Errorf("empty Tj: got %d runs, want 0", len(runs))
	}
}

func TestExtract_NoFont_StillEmitsButFontFieldsEmpty(t *testing.T) {
	t.Parallel()
	body := []byte("BT /F0 12 Tf 0 0 Td (orphan) Tj ET")
	runs := extractFromBytes(t, body,
		map[string]*internalpdf.ResolvedFont{"F0": nil},
		ExtractOptions{})
	if len(runs) != 1 {
		t.Fatalf("got %d, want 1", len(runs))
	}
	if runs[0].FontKey != "F0" {
		t.Errorf("FontKey: got %q, want F0", runs[0].FontKey)
	}
	if runs[0].FontName != "" {
		t.Errorf("FontName: got %q, want empty", runs[0].FontName)
	}
}

func TestExtract_Type3Font_SuppressesEmits(t *testing.T) {
	// Not t.Parallel() — captureLogs mutates the global slog default.
	body := []byte(`BT /F2 12 Tf (suppressed) Tj /F1 12 Tf (visible) Tj ET`)
	logs := captureLogs(t, func() {
		runs := extractFromBytes(t, body,
			map[string]*internalpdf.ResolvedFont{
				"F2": type3F2(),
				"F1": helveticaF1(),
			},
			ExtractOptions{})
		if len(runs) != 1 {
			t.Errorf("got %d runs, want 1 (Type3 suppresses, F1 emits)", len(runs))
		}
		if len(runs) > 0 && runs[0].FontName != "Helvetica" {
			t.Errorf("emitted run font: %q, want Helvetica", runs[0].FontName)
		}
	})
	if !strings.Contains(logs, "Type 3 font") {
		t.Errorf("expected Type 3 log, got: %q", logs)
	}
}

func TestExtract_MalformedOperator_LogsAndSkips(t *testing.T) {
	// Not t.Parallel() — captureLogs mutates the global slog default.
	body := []byte("BT Tf 1 0 0 1 Tm (x) Tj ET")
	logs := captureLogs(t, func() {
		_ = extractFromBytes(t, body, fontsF1(), ExtractOptions{})
	})
	if !strings.Contains(logs, "operand-stack underflow") {
		t.Errorf("expected underflow warning, got: %q", logs)
	}
}

func TestExtract_BDC_DepthCap(t *testing.T) {
	// Not t.Parallel() — captureLogs mutates the global slog default.
	var b strings.Builder
	for i := 1; i <= 33; i++ {
		b.WriteString("/Span << /MCID ")
		b.WriteString(itoaSimple(i))
		b.WriteString(" >> BDC ")
	}
	b.WriteString("BT /F1 12 Tf 0 0 Td (deep) Tj ET ")
	for range 33 {
		b.WriteString("EMC ")
	}
	logs := captureLogs(t, func() {
		runs := extractFromBytes(t, []byte(b.String()), fontsF1(), ExtractOptions{})
		if len(runs) != 1 {
			t.Fatalf("got %d runs, want 1", len(runs))
		}
		if runs[0].MCID != 32 {
			t.Errorf("MCID at depth-32: got %d, want 32 (33rd push dropped)", runs[0].MCID)
		}
	})
	if !strings.Contains(logs, "marked-content stack at cap") {
		t.Errorf("expected depth-cap warning, got: %q", logs)
	}
	if !strings.Contains(logs, "EMC with empty marked-content stack") {
		t.Errorf("expected over-pop warning on 33rd EMC, got: %q", logs)
	}
}

// itoaSimple is a tiny test-only int->ascii to avoid pulling strconv
// into the test file's primary import surface.
func itoaSimple(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
