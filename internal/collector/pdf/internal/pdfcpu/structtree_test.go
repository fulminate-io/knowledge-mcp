package pdfcpu

import (
	"path/filepath"
	"testing"
)

// TestStructTreeRoot_NilOnUntagged exercises the basic find-vs-absent
// shape against testdata/onepage.pdf. onepage.pdf is the T1 untagged
// fixture (CreateDemoXRef does not emit /StructTreeRoot), so the
// catalog lookup must return (nil, nil) — not an error.
func TestStructTreeRoot_NilOnUntagged(t *testing.T) {
	t.Parallel()
	ctx, err := LoadFile(onePageFixture)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", onePageFixture, err)
	}
	defer ctx.Close()

	if ctx.IsTagged() {
		t.Fatalf("onepage.pdf reports IsTagged=true; expected untagged baseline")
	}
	root, err := ctx.StructTreeRoot()
	if err != nil {
		t.Fatalf("StructTreeRoot on untagged PDF returned error: %v", err)
	}
	if root != nil {
		t.Fatalf("StructTreeRoot on untagged PDF returned non-nil ref: %#v", root)
	}
}

// TestStructTreeRoot_NonNilOnTagged loads the synthetic tagged fixture
// emitted by Phase 7's WriteTaggedPDF helper and asserts the catalog
// lookup yields a non-nil ref whose /K array carries the synthetic
// /Document root.
func TestStructTreeRoot_NonNilOnTagged(t *testing.T) {
	t.Parallel()
	ctx := loadTaggedFixture(t, "simple_tagged.pdf")
	defer ctx.Close()

	root, err := ctx.StructTreeRoot()
	if err != nil {
		t.Fatalf("StructTreeRoot err: %v", err)
	}
	if root == nil {
		t.Fatalf("StructTreeRoot returned nil on tagged PDF")
	}
	kArr, err := root.KArray()
	if err != nil {
		t.Fatalf("KArray err: %v", err)
	}
	if len(kArr) != 1 {
		t.Errorf("root /K len = %d, want 1 (Document)", len(kArr))
	}
}

// TestStructElem_TypeAndKids walks the synthetic tagged fixture and
// asserts a /Document root with three children (/H1 + /P + /P) in
// order — the basic shape Phase 4's Walk depends on.
func TestStructElem_TypeAndKids(t *testing.T) {
	t.Parallel()
	ctx := loadTaggedFixture(t, "simple_tagged.pdf")
	defer ctx.Close()

	doc := mustResolveRootDoc(t, ctx)
	if got, want := doc.Type(), "Document"; got != want {
		t.Errorf("root.Type = %q, want %q", got, want)
	}
	kids, err := doc.Kids()
	if err != nil {
		t.Fatalf("Kids err: %v", err)
	}
	if len(kids) != 3 {
		t.Fatalf("Document.Kids len = %d, want 3", len(kids))
	}
	wantTypes := []string{"H1", "P", "P"}
	for i, k := range kids {
		se, ok := k.(KidStructElem)
		if !ok {
			t.Errorf("kid[%d] kind=%q, want struct", i, k.Kind())
			continue
		}
		if got := se.Ref.Type(); got != wantTypes[i] {
			t.Errorf("kid[%d].Type = %q, want %q", i, got, wantTypes[i])
		}
	}
}

// TestStructElem_PageIndex_Resolved asserts a leaf element with /Pg
// pointing at the first page resolves to PageIndex() == 0. Verifies
// the lazy obj-number → page-index cache populates correctly.
func TestStructElem_PageIndex_Resolved(t *testing.T) {
	t.Parallel()
	ctx := loadTaggedFixture(t, "simple_tagged.pdf")
	defer ctx.Close()

	doc := mustResolveRootDoc(t, ctx)
	kids, err := doc.Kids()
	if err != nil {
		t.Fatalf("Kids err: %v", err)
	}
	if len(kids) == 0 {
		t.Fatalf("Document has no kids")
	}
	se, ok := kids[0].(KidStructElem)
	if !ok {
		t.Fatalf("first kid is not a struct element")
	}
	idx, ok := se.Ref.PageIndex()
	if !ok {
		t.Fatalf("PageIndex() reports unresolvable for first kid")
	}
	if idx != 0 {
		t.Errorf("PageIndex() = %d, want 0", idx)
	}
}

// TestStructElem_ActualText_Override loads testdata/actualtext_tagged.pdf
// and confirms ActualText() returns the override string set on the
// /A << /ActualText (...) >> entry.
func TestStructElem_ActualText_Override(t *testing.T) {
	t.Parallel()
	ctx := loadTaggedFixture(t, "actualtext_tagged.pdf")
	defer ctx.Close()

	doc := mustResolveRootDoc(t, ctx)
	kids, err := doc.Kids()
	if err != nil || len(kids) == 0 {
		t.Fatalf("Document.Kids err=%v len=%d", err, len(kids))
	}
	se, ok := kids[0].(KidStructElem)
	if !ok {
		t.Fatalf("first kid is not a struct element")
	}
	if got, want := se.Ref.ActualText(), "replaced text"; got != want {
		t.Errorf("ActualText() = %q, want %q", got, want)
	}
}

// TestStructElem_KidsMCID asserts a leaf /P with /K [1] yields a
// KidMCID with ID 1 and PageIndex inherited from parent /Pg.
func TestStructElem_KidsMCID(t *testing.T) {
	t.Parallel()
	ctx := loadTaggedFixture(t, "simple_tagged.pdf")
	defer ctx.Close()

	doc := mustResolveRootDoc(t, ctx)
	docKids, err := doc.Kids()
	if err != nil || len(docKids) < 2 {
		t.Fatalf("doc.Kids err=%v len=%d", err, len(docKids))
	}
	// Kid[1] is the first /P (mcid 2 per gen.go).
	pNode, ok := docKids[1].(KidStructElem)
	if !ok {
		t.Fatalf("doc.Kids[1] not a struct element")
	}
	pKids, err := pNode.Ref.Kids()
	if err != nil {
		t.Fatalf("/P.Kids err: %v", err)
	}
	if len(pKids) != 1 {
		t.Fatalf("/P.Kids len = %d, want 1", len(pKids))
	}
	mc, ok := pKids[0].(KidMCID)
	if !ok {
		t.Fatalf("/P.Kids[0] kind = %q, want mcid", pKids[0].Kind())
	}
	if mc.ID != 2 {
		t.Errorf("KidMCID.ID = %d, want 2", mc.ID)
	}
	if mc.PageIndex != 0 {
		t.Errorf("KidMCID.PageIndex = %d, want 0", mc.PageIndex)
	}
}

// loadTaggedFixture loads a fixture from collector/pdf/testdata/. The
// pdfcpu wrapper's testdata/ dir holds only onepage.pdf; tagged
// fixtures live under the parent collector/pdf/testdata/.
func loadTaggedFixture(t *testing.T, name string) *Context {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", name)
	ctx, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", path, err)
	}
	return ctx
}

// mustResolveRootDoc walks the StructTreeRoot's /K to the first child
// (the synthetic /Document root) and resolves it to a StructElemRef.
// Helper used by every test that needs a starting structure element.
func mustResolveRootDoc(t *testing.T, ctx *Context) *StructElemRef {
	t.Helper()
	root, err := ctx.StructTreeRoot()
	if err != nil || root == nil {
		t.Fatalf("StructTreeRoot err=%v root=%v", err, root)
	}
	kArr, err := root.KArray()
	if err != nil || len(kArr) == 0 {
		t.Fatalf("KArray err=%v len=%d", err, len(kArr))
	}
	doc, err := ctx.ResolveStructElem(kArr[0])
	if err != nil || doc == nil {
		t.Fatalf("ResolveStructElem err=%v doc=%v", err, doc)
	}
	return doc
}
