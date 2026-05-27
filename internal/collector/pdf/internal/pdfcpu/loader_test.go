package pdfcpu

import (
	"errors"
	"strings"
	"testing"
)

const onePageFixture = "testdata/onepage.pdf"

// TestLoad_GarbageInput_ReturnsTypedError verifies that Load surfaces a
// non-encrypted parse error when the input is not a PDF at all. The error
// must NOT be ErrEncrypted — that signals only the password-protection
// branch.
func TestLoad_GarbageInput_ReturnsTypedError(t *testing.T) {
	t.Parallel()
	_, err := Load(strings.NewReader("not a pdf"))
	if err == nil {
		t.Fatal("expected error for garbage input, got nil")
	}
	if errors.Is(err, ErrEncrypted) {
		t.Fatalf("expected non-encrypted parse error, got ErrEncrypted: %v", err)
	}
}

// TestLoad_OnePagePDF_PageCountAndMetadata loads the synthetic 1-page
// fixture and checks that PageCount, the per-page MediaBox, and Rotation
// match expectations. MediaBox is checked for non-degeneracy (width and
// height > 0) rather than exact pt because the fixture's exact size is a
// generator-implementation detail; we just need a working metadata path.
func TestLoad_OnePagePDF_PageCountAndMetadata(t *testing.T) {
	t.Parallel()
	ctx, err := LoadFile(onePageFixture)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", onePageFixture, err)
	}
	defer ctx.Close()

	if got := ctx.PageCount(); got != 1 {
		t.Fatalf("PageCount = %d, want 1", got)
	}
	page, err := ctx.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	if page.Index() != 0 {
		t.Fatalf("Index = %d, want 0", page.Index())
	}
	mb := page.MediaBox()
	width := mb.X1 - mb.X0
	height := mb.Y1 - mb.Y0
	if width <= 0 || height <= 0 {
		t.Fatalf("MediaBox is degenerate: %+v (w=%v, h=%v)", mb, width, height)
	}
	// Sanity check: the synthetic fixture is US-Letter (612x792) per
	// testdata/gen.go. Loose tolerance leaves room for unit choices.
	if width < 100 || height < 100 {
		t.Fatalf("MediaBox too small to be US-Letter: %+v", mb)
	}
	if page.Rotation() != 0 {
		t.Fatalf("Rotation = %d, want 0", page.Rotation())
	}
}

// TestPage_OutOfRange_ReturnsError covers both negative and over-the-end
// indices on the 1-page fixture.
func TestPage_OutOfRange_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx, err := LoadFile(onePageFixture)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", onePageFixture, err)
	}
	defer ctx.Close()

	for _, i := range []int{-1, 1, 99} {
		if _, err := ctx.Page(i); err == nil {
			t.Errorf("Page(%d) returned nil error on a 1-page doc", i)
		}
	}
}

// TestIsTagged_UntaggedPDF_ReturnsFalse confirms IsTagged returns false on
// the synthetic untagged fixture. Tagged-true coverage is deferred to T6
// (the structtree ticket builds its own tagged fixture).
func TestIsTagged_UntaggedPDF_ReturnsFalse(t *testing.T) {
	t.Parallel()
	ctx, err := LoadFile(onePageFixture)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", onePageFixture, err)
	}
	defer ctx.Close()
	if ctx.IsTagged() {
		t.Fatal("IsTagged() = true on synthetic untagged fixture, want false")
	}
}
