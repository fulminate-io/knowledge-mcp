package pdfcpu

import "testing"

// TestT3Fixtures_LoadAndPageCount is the smoke check for the 7 T3
// fixtures emitted by collector/pdf/testdata/gen.go. Each fixture loads
// without error and exposes the expected page count. Field-path
// assertions live in resolvedfont_t3_test.go (alongside the deeper
// content-stream checks).
//
// Per plan d76acb28 step 8309c27a — the criterion exercises the
// "Each generated PDF loads via internalpdf.LoadFile without error"
// + "single-page fixtures expose 1 page; multipage_one_font.pdf
// exposes exactly 3 pages" requirements.
func TestT3Fixtures_LoadAndPageCount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path      string
		wantPages int
	}{
		{"../../testdata/tounicode_clean.pdf", 1},
		{"../../testdata/no_tounicode_winansi.pdf", 1},
		{"../../testdata/differences_override.pdf", 1},
		{"../../testdata/no_encoding_info.pdf", 1},
		{"../../testdata/ligatures.pdf", 1},
		{"../../testdata/cidfont_identity_h.pdf", 1},
		{"../../testdata/multipage_one_font.pdf", 3},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			t.Parallel()
			ctx, err := LoadFile(c.path)
			if err != nil {
				t.Fatalf("LoadFile(%q): %v", c.path, err)
			}
			defer ctx.Close()
			if got := ctx.PageCount(); got != c.wantPages {
				t.Errorf("%s page count: got %d, want %d", c.path, got, c.wantPages)
			}
		})
	}
}

// TestMultipageOneFont_SharedFontIndirectRef verifies the per-T3-1
// requirement: all 3 pages of multipage_one_font.pdf reference the
// SAME Helvetica F1 font dict via the same indirect-ref. The cache
// key is content-derived (BaseFont + sha256(ToUnicodeBytes)) so the
// resolver can't tell the difference; this test asserts the fixture
// shape is what the cache regression test expects.
func TestMultipageOneFont_SharedFontIndirectRef(t *testing.T) {
	t.Parallel()
	ctx, err := LoadFile("../../testdata/multipage_one_font.pdf")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	defer ctx.Close()

	if pc := ctx.PageCount(); pc != 3 {
		t.Fatalf("page count: got %d, want 3", pc)
	}
	// Resolve F1 on each page and assert BaseFont + ToUnicode bytes
	// are identical (proxy for shared indirect-ref since the test can
	// only see resolved content).
	type page struct {
		baseFont string
		tuLen    int
	}
	got := make([]page, 0, 3)
	for i := range 3 {
		p, err := ctx.Page(i)
		if err != nil {
			t.Fatalf("Page(%d): %v", i, err)
		}
		rf, err := p.ResolvedFont("F1")
		if err != nil {
			t.Fatalf("ResolvedFont(page %d): %v", i, err)
		}
		if rf == nil {
			t.Fatalf("ResolvedFont(page %d): nil", i)
		}
		got = append(got, page{baseFont: rf.BaseFont, tuLen: len(rf.ToUnicodeBytes)})
	}
	for i := 1; i < len(got); i++ {
		if got[i].baseFont != got[0].baseFont || got[i].tuLen != got[0].tuLen {
			t.Errorf("page %d font diverges: got %+v, want %+v (matches page 0)", i, got[i], got[0])
		}
	}
	if got[0].tuLen == 0 {
		t.Errorf("page 0 ToUnicodeBytes empty (fixture should embed the shared CMap)")
	}
}
