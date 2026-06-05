package font

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// header is the canonical CMap preamble that real /ToUnicode streams
// open with. parseCMap doesn't strictly require it (the tokenizer
// only cares about begin*/end* keywords) but our fixtures use it for
// realism.
const cmapHeader = `/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CMapName /Adobe-Identity-UCS def
/CMapType 2 def
1 begincodespacerange
<00> <FF>
endcodespacerange
`

const cmapFooter = `endcmap
CMapName currentdict /CMap defineresource pop
end
end
`

// TestParseCMap_BfcharSingle: one bfchar pair maps code 0x41 → 'A'.
func TestParseCMap_BfcharSingle(t *testing.T) {
	t.Parallel()
	src := cmapHeader + "1 beginbfchar\n<41> <0041>\nendbfchar\n" + cmapFooter
	c, err := parseCMap([]byte(src))
	if err != nil {
		t.Fatalf("parseCMap: %v", err)
	}
	rs, ok := c.decode(0x41)
	if !ok || len(rs) != 1 || rs[0] != 'A' {
		t.Errorf("decode(0x41): got %v ok=%v, want ['A']", rs, ok)
	}
}

// TestParseCMap_BfcharMulti: multiple bfchar pairs.
func TestParseCMap_BfcharMulti(t *testing.T) {
	t.Parallel()
	src := cmapHeader + "3 beginbfchar\n<41> <0041>\n<42> <0042>\n<43> <0043>\nendbfchar\n" + cmapFooter
	c, err := parseCMap([]byte(src))
	if err != nil {
		t.Fatalf("parseCMap: %v", err)
	}
	for code, want := range map[uint32]rune{0x41: 'A', 0x42: 'B', 0x43: 'C'} {
		rs, ok := c.decode(code)
		if !ok || len(rs) != 1 || rs[0] != want {
			t.Errorf("decode(%#x): got %v, want [%c]", code, rs, want)
		}
	}
}

// TestParseCMap_BfrangeSequential: range with hex target → consecutive
// codes get consecutive runes.
func TestParseCMap_BfrangeSequential(t *testing.T) {
	t.Parallel()
	src := cmapHeader + "1 beginbfrange\n<20> <22> <0030>\nendbfrange\n" + cmapFooter
	c, err := parseCMap([]byte(src))
	if err != nil {
		t.Fatalf("parseCMap: %v", err)
	}
	for code, want := range map[uint32]rune{0x20: '0', 0x21: '1', 0x22: '2'} {
		rs, ok := c.decode(code)
		if !ok || len(rs) != 1 || rs[0] != want {
			t.Errorf("decode(%#x): got %v, want [%c]", code, rs, want)
		}
	}
}

// TestParseCMap_BfrangeArray: range with array target — per-code
// mapping.
func TestParseCMap_BfrangeArray(t *testing.T) {
	t.Parallel()
	src := cmapHeader + "1 beginbfrange\n<10> <12> [<0041> <0042> <0043>]\nendbfrange\n" + cmapFooter
	c, err := parseCMap([]byte(src))
	if err != nil {
		t.Fatalf("parseCMap: %v", err)
	}
	for code, want := range map[uint32]rune{0x10: 'A', 0x11: 'B', 0x12: 'C'} {
		rs, ok := c.decode(code)
		if !ok || len(rs) != 1 || rs[0] != want {
			t.Errorf("decode(%#x): got %v, want [%c]", code, rs, want)
		}
	}
}

// TestParseCMap_NotdefRange: notdef ranges fire on misses to the bf
// tables.
func TestParseCMap_NotdefRange(t *testing.T) {
	t.Parallel()
	src := cmapHeader + `1 beginbfchar
<41> <0041>
endbfchar
1 beginnotdefrange
<F0> <FF> <FFFD>
endnotdefrange
` + cmapFooter
	c, err := parseCMap([]byte(src))
	if err != nil {
		t.Fatalf("parseCMap: %v", err)
	}
	rs, ok := c.decode(0xF0)
	if !ok || len(rs) != 1 || rs[0] != 0xFFFD {
		t.Errorf("decode(0xF0) notdef: got %v ok=%v, want [U+FFFD]", rs, ok)
	}
}

// TestParseCMap_Ligature: hex target with multiple UTF-16 code units
// returns multi-rune slice (e.g. <0066 0069> → "fi").
func TestParseCMap_Ligature(t *testing.T) {
	t.Parallel()
	src := cmapHeader + "1 beginbfchar\n<01> <00660069>\nendbfchar\n" + cmapFooter
	c, err := parseCMap([]byte(src))
	if err != nil {
		t.Fatalf("parseCMap: %v", err)
	}
	rs, ok := c.decode(0x01)
	if !ok || len(rs) != 2 || rs[0] != 'f' || rs[1] != 'i' {
		t.Errorf("decode(0x01) ligature: got %v ok=%v, want ['f','i']", rs, ok)
	}
}

// TestParseCMap_SurrogatePair: <D83DDC2D> → U+1F42D 🐭 (one rune from
// two UTF-16 code units).
func TestParseCMap_SurrogatePair(t *testing.T) {
	t.Parallel()
	src := cmapHeader + "1 beginbfchar\n<01> <D83DDC2D>\nendbfchar\n" + cmapFooter
	c, err := parseCMap([]byte(src))
	if err != nil {
		t.Fatalf("parseCMap: %v", err)
	}
	rs, ok := c.decode(0x01)
	if !ok || len(rs) != 1 || rs[0] != 0x1F42D {
		t.Errorf("decode(surrogate): got %v ok=%v, want [U+1F42D]", rs, ok)
	}
}

// TestParseCMap_BfrangeOverflow_Skipped covers that
// a bfrange claiming 4 billion entries (<00000000> <FFFFFFFF>) MUST
// be skipped (no allocation), parser returns nil error, slog.Warn
// fires with "bfrange span exceeds maxBfRangeSpan".
func TestParseCMap_BfrangeOverflow_Skipped(t *testing.T) {
	logs := captureSlog(t)
	src := cmapHeader + "1 beginbfrange\n<00000000> <FFFFFFFF> <0041>\nendbfrange\n" + cmapFooter
	c, err := parseCMap([]byte(src))
	if err != nil {
		t.Fatalf("parseCMap returned err = %v, want nil", err)
	}
	if len(c.bfranges) != 0 {
		t.Errorf("bfranges: got %d, want 0 (overflow directive must be skipped)", len(c.bfranges))
	}
	if !strings.Contains(logs.String(), bfRangeWarn) {
		t.Errorf("expected bfRange warning in logs, got: %q", logs.String())
	}
}

// TestParseCMap_HexTokenOverflow_Skipped covers that
// a bfchar target whose decoded byte length exceeds maxHexTokenBytes
// is skipped; cmap.bfchars is empty; slog.Warn fires.
func TestParseCMap_HexTokenOverflow_Skipped(t *testing.T) {
	logs := captureSlog(t)
	// 3 MiB of "AB" repeated → 1.5 MiB decoded > maxHexTokenBytes (1 MiB).
	huge := strings.Repeat("AB", (maxHexTokenBytes+10)*2)
	src := cmapHeader + "1 beginbfchar\n<41> <" + huge + ">\nendbfchar\n" + cmapFooter
	c, err := parseCMap([]byte(src))
	if err != nil {
		t.Fatalf("parseCMap: %v", err)
	}
	if len(c.bfchars) != 0 {
		t.Errorf("bfchars: got %d, want 0 (oversized target must be skipped)", len(c.bfchars))
	}
	if !strings.Contains(logs.String(), hexTokenWarn) {
		t.Errorf("expected hexToken warning in logs, got: %q", logs.String())
	}
}

// TestParseCMap_DirectiveCountCap covers that a CMap
// with > maxDirectives entries stops at the cap. Bound test at
// maxDirectives + 5 (200000 is overkill for CI; use cap+5).
func TestParseCMap_DirectiveCountCap(t *testing.T) {
	logs := captureSlog(t)
	var b bytes.Buffer
	b.WriteString(cmapHeader)
	const want = maxDirectives + 5
	fmt.Fprintf(&b, "%d beginbfchar\n", want)
	for i := range want {
		fmt.Fprintf(&b, "<%04X> <%04X>\n", i, 0x4000+i%0x100)
	}
	b.WriteString("endbfchar\n")
	b.WriteString(cmapFooter)

	c, err := parseCMap(b.Bytes())
	if err != nil {
		t.Fatalf("parseCMap: %v", err)
	}
	// Should have stopped at maxDirectives.
	if got := len(c.bfchars); got != maxDirectives {
		t.Errorf("bfchars: got %d, want %d (cap)", got, maxDirectives)
	}
	if !strings.Contains(logs.String(), directiveWarn) {
		t.Errorf("expected directive-count warning in logs, got: %q", logs.String())
	}
}

// TestParseCMap_UseCMap_Silent covers that a usecmap
// directive is silently skipped — no warning, no recursion. Per T2-4
// (silent rejection prevents the implementation gap from widening
// into a chained-CMap loader).
func TestParseCMap_UseCMap_Silent(t *testing.T) {
	logs := captureSlog(t)
	src := cmapHeader + `/SomeOtherCMap usecmap
1 beginbfchar
<41> <0041>
endbfchar
` + cmapFooter
	c, err := parseCMap([]byte(src))
	if err != nil {
		t.Fatalf("parseCMap: %v", err)
	}
	// The legitimate bfchar after usecmap should still parse.
	if rs, ok := c.decode(0x41); !ok || len(rs) != 1 || rs[0] != 'A' {
		t.Errorf("decode(0x41) after usecmap: got %v ok=%v, want ['A']", rs, ok)
	}
	if strings.Contains(logs.String(), "usecmap") {
		t.Errorf("usecmap directive should be silent (no log line); got: %q", logs.String())
	}
}

// TestParseCMap_Empty: empty input returns empty cmap, no error.
func TestParseCMap_Empty(t *testing.T) {
	t.Parallel()
	c, err := parseCMap(nil)
	if err != nil {
		t.Fatalf("parseCMap(nil): %v", err)
	}
	if c == nil {
		t.Fatal("parseCMap(nil): returned nil cmap")
	}
}

// TestParseCMap_TooLarge: input > maxCMapBytes returns ErrCMapTooLarge.
func TestParseCMap_TooLarge(t *testing.T) {
	t.Parallel()
	huge := make([]byte, maxCMapBytes+1)
	_, err := parseCMap(huge)
	if !errors.Is(err, ErrCMapTooLarge) {
		t.Errorf("parseCMap(>maxCMapBytes): got %v, want ErrCMapTooLarge", err)
	}
}

// captureSlog sets the default slog handler to a buffer-backed text
// handler for the duration of the test. Returns the buffer; cleanup
// restores the original handler.
//
// Tests using captureSlog should NOT call t.Parallel() — slog's
// default is process-global state.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}
