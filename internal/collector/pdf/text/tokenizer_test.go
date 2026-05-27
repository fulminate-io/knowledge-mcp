package text

import (
	"bytes"
	"testing"
)

// drain runs the tokenizer to EOF and returns the kind+string-payload
// sequence. Helper for table-driven tests.
func drain(t *testing.T, src string) []token {
	t.Helper()
	tk := newTokenizer([]byte(src))
	var out []token
	for {
		tok, err := tk.next()
		if err != nil {
			t.Fatalf("tokenize %q: %v", src, err)
		}
		out = append(out, tok)
		if tok.kind == tokEOF {
			return out
		}
	}
}

// kinds returns the token kinds in seq.
func kinds(seq []token) []tokKind {
	k := make([]tokKind, len(seq))
	for i, t := range seq {
		k[i] = t.kind
	}
	return k
}

// payloadString returns string(payload) for the i'th non-EOF token.
func payloadString(seq []token, i int) string {
	return string(seq[i].payload)
}

func TestTokenizer_HelloTjBT_SevenTokensPlusEOF(t *testing.T) {
	t.Parallel()
	src := "BT /F1 12 Tf (Hello) Tj ET"
	got := drain(t, src)
	wantKinds := []tokKind{
		tokOperator, tokName, tokInt, tokOperator, tokString, tokOperator, tokOperator, tokEOF,
	}
	if !equalKinds(kinds(got), wantKinds) {
		t.Fatalf("kinds: got %v, want %v", kinds(got), wantKinds)
	}
	wantPayloads := []string{"BT", "F1", "12", "Tf", "Hello", "Tj", "ET"}
	for i, w := range wantPayloads {
		if p := payloadString(got, i); p != w {
			t.Errorf("payload %d: got %q, want %q", i, p, w)
		}
	}
}

func TestTokenizer_Numbers_IntFloatSign(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src      string
		wantKind tokKind
		wantStr  string
	}{
		{"123", tokInt, "123"},
		{"-5", tokInt, "-5"},
		{"+7", tokInt, "+7"},
		{"1.5", tokFloat, "1.5"},
		{"-3.14", tokFloat, "-3.14"},
		{".25", tokFloat, ".25"},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			t.Parallel()
			seq := drain(t, c.src)
			if len(seq) != 2 || seq[0].kind != c.wantKind || string(seq[0].payload) != c.wantStr {
				t.Errorf("got %+v want kind=%d str=%q", seq, c.wantKind, c.wantStr)
			}
		})
	}
}

func TestTokenizer_Names_PlainAndEscaped(t *testing.T) {
	t.Parallel()
	plain := drain(t, "/Tj /F1 /Hello-World")
	if len(plain) != 4 {
		t.Fatalf("plain: got %d tokens, want 4 (3 names + EOF)", len(plain))
	}
	for i, want := range []string{"Tj", "F1", "Hello-World"} {
		if string(plain[i].payload) != want {
			t.Errorf("plain[%d] payload: %q, want %q", i, plain[i].payload, want)
		}
		if plain[i].escaped {
			t.Errorf("plain[%d] escaped: got true, want false (no # in name)", i)
		}
	}
	// /A#20B -> "A B" (#20 = 0x20 = space)
	esc := drain(t, "/A#20B")
	if len(esc) != 2 {
		t.Fatalf("esc: got %d tokens, want 2", len(esc))
	}
	if string(esc[0].payload) != "A B" {
		t.Errorf("escaped name: got %q, want %q", esc[0].payload, "A B")
	}
	if !esc[0].escaped {
		t.Error("escaped name token should have escaped=true")
	}
}

func TestTokenizer_LiteralString_FastAndEscape(t *testing.T) {
	t.Parallel()
	// Fast path: no escapes, no nested parens beyond the outer.
	fast := drain(t, "(Hello, world)")
	if len(fast) != 2 || string(fast[0].payload) != "Hello, world" {
		t.Fatalf("fast: %+v", fast)
	}
	if fast[0].escaped {
		t.Error("fast literal string should not allocate")
	}
	// Escape path: \\n, \\t, \\\\, octal \\053 (= '+').
	esc := drain(t, `(line\nbreak\tdone\\\053)`)
	if len(esc) != 2 {
		t.Fatalf("esc: %+v", esc)
	}
	want := "line\nbreak\tdone\\+"
	if string(esc[0].payload) != want {
		t.Errorf("decoded: %q, want %q", esc[0].payload, want)
	}
	if !esc[0].escaped {
		t.Error("escape-path string should have escaped=true")
	}
	// Nested parens (balanced): "((nested))"
	nest := drain(t, "(outer (inner) tail)")
	if len(nest) != 2 || string(nest[0].payload) != "outer (inner) tail" {
		t.Errorf("nested: got %q, want %q", nest[0].payload, "outer (inner) tail")
	}
}

func TestTokenizer_HexString_DecodesAndPadsOdd(t *testing.T) {
	t.Parallel()
	// "Hello" = 48 65 6C 6C 6F
	got := drain(t, "<48656C6C6F>")
	if len(got) != 2 || got[0].kind != tokHexString {
		t.Fatalf("got %+v", got)
	}
	if !bytes.Equal(got[0].payload, []byte("Hello")) {
		t.Errorf("decoded: %q, want %q", got[0].payload, "Hello")
	}
	// Odd-length: "<F>" -> [0xF0]
	odd := drain(t, "<F>")
	if !bytes.Equal(odd[0].payload, []byte{0xF0}) {
		t.Errorf("odd pad: got %v, want [0xF0]", odd[0].payload)
	}
	// Whitespace ignored: "<48 65 6C 6C 6F>"
	ws := drain(t, "<48 65 6C\n6C 6F>")
	if !bytes.Equal(ws[0].payload, []byte("Hello")) {
		t.Errorf("ws-decoded: %q, want Hello", ws[0].payload)
	}
}

func TestTokenizer_ArrayAndDictDelimiters(t *testing.T) {
	t.Parallel()
	got := drain(t, "[1 2 3]")
	wantK := []tokKind{tokArrayStart, tokInt, tokInt, tokInt, tokArrayEnd, tokEOF}
	if !equalKinds(kinds(got), wantK) {
		t.Errorf("array kinds: got %v, want %v", kinds(got), wantK)
	}
	got = drain(t, "<< /A 1 >>")
	wantK = []tokKind{tokDictStart, tokName, tokInt, tokDictEnd, tokEOF}
	if !equalKinds(kinds(got), wantK) {
		t.Errorf("dict kinds: got %v, want %v", kinds(got), wantK)
	}
}

func TestTokenizer_Comments_Skipped(t *testing.T) {
	t.Parallel()
	src := "BT %% set up text\n/F1 12 Tf% inline comment\n(text) Tj\nET"
	got := drain(t, src)
	wantK := []tokKind{
		tokOperator, // BT
		tokName,     // F1
		tokInt,      // 12
		tokOperator, // Tf
		tokString,   // text
		tokOperator, // Tj
		tokOperator, // ET
		tokEOF,
	}
	if !equalKinds(kinds(got), wantK) {
		t.Errorf("kinds: got %v, want %v", kinds(got), wantK)
	}
}

func TestTokenizer_Whitespace_AllSixVariants(t *testing.T) {
	t.Parallel()
	// Six PDF whitespace bytes between tokens.
	src := "BT\x00\t\n\x0c\r /F1 12 Tf"
	got := drain(t, src)
	if len(got) < 4 {
		t.Fatalf("expected at least 4 tokens + EOF, got %d", len(got))
	}
	if string(got[0].payload) != "BT" || got[1].kind != tokName {
		t.Errorf("whitespace lex broke: %+v", got)
	}
}

func TestTokenizer_TJArray_OperandsForKerning(t *testing.T) {
	t.Parallel()
	// PDF TJ takes an array of strings and adjustments.
	src := "[(He) -20 (l) 5 (lo)] TJ"
	got := drain(t, src)
	wantK := []tokKind{
		tokArrayStart, tokString, tokInt, tokString, tokInt, tokString, tokArrayEnd,
		tokOperator, tokEOF,
	}
	if !equalKinds(kinds(got), wantK) {
		t.Errorf("TJ kinds: got %v, want %v", kinds(got), wantK)
	}
	// Operator at index 7.
	if string(got[7].payload) != "TJ" {
		t.Errorf("TJ operator payload: %q", got[7].payload)
	}
}

func TestTokenizer_EmptyAndWhitespaceOnly(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"", "    \n\t  ", "% comment only\n"} {
		got := drain(t, src)
		if len(got) != 1 || got[0].kind != tokEOF {
			t.Errorf("empty src %q: got %+v, want [EOF]", src, got)
		}
	}
}

func TestTokenizer_Errors(t *testing.T) {
	t.Parallel()
	cases := []string{
		"(unterminated", // unterminated literal string
		"<DEAD",         // unterminated hex string
		"<XYZ>",         // bad hex digit
		">",             // stray > without matching >>
	}
	for _, src := range cases {
		t.Run(src, func(t *testing.T) {
			t.Parallel()
			tk := newTokenizer([]byte(src))
			gotErr := false
			for {
				_, err := tk.next()
				if err != nil {
					gotErr = true
					break
				}
				if tk.pos >= len(tk.src) {
					break
				}
			}
			if !gotErr {
				t.Errorf("expected error for %q, got none", src)
			}
		})
	}
}

func equalKinds(a, b []tokKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
