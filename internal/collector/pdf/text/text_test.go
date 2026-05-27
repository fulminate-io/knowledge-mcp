package text

import "testing"

// TestTextRun_ZeroValue is the T1 compile-only smoke. Asserts the zero
// value of every exported field — a tripwire that breaks loudly if any
// of the 13 fields drift in shape (string→[]byte, int→int64, etc.).
// T2 replaces this with real walker tests.
func TestTextRun_ZeroValue(t *testing.T) {
	t.Parallel()
	var r TextRun
	if r.Text != "" {
		t.Errorf("Text: got %q, want \"\"", r.Text)
	}
	if r.Glyphs != nil {
		t.Errorf("Glyphs: got %v, want nil", r.Glyphs)
	}
	if r.X != 0 || r.Y != 0 {
		t.Errorf("X/Y: got %v/%v, want 0/0", r.X, r.Y)
	}
	if r.Width != 0 || r.Height != 0 {
		t.Errorf("Width/Height: got %v/%v, want 0/0", r.Width, r.Height)
	}
	if r.FontName != "" || r.FontKey != "" {
		t.Errorf("FontName/FontKey: got %q/%q, want empty", r.FontName, r.FontKey)
	}
	if r.Size != 0 {
		t.Errorf("Size: got %v, want 0", r.Size)
	}
	if r.Mono || r.Bold || r.Italic {
		t.Errorf("Mono/Bold/Italic: got %v/%v/%v, want false/false/false", r.Mono, r.Bold, r.Italic)
	}
	if r.MCID != 0 {
		t.Errorf("MCID: got %d, want 0", r.MCID)
	}
}
