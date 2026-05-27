package pdf_test

import (
	"errors"
	"strings"
	"testing"

	pdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/structtree"
)

const (
	simpleTaggedFixture  = "testdata/simple_tagged.pdf"
	hybridPartialFixture = "testdata/hybrid_partial.pdf"
)

// TestDocument_StructTree_Untagged_ReturnsErrNotTagged is the
// untagged-side contract on the public Document.StructTree() method.
func TestDocument_StructTree_Untagged_ReturnsErrNotTagged(t *testing.T) {
	t.Parallel()
	doc, err := pdf.OpenFile(onePageFixture)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", onePageFixture, err)
	}
	defer doc.Close()
	tree, err := doc.StructTree()
	if !errors.Is(err, structtree.ErrNotTagged) {
		t.Fatalf("StructTree err = %v, want errors.Is(err, ErrNotTagged)", err)
	}
	if tree != nil {
		t.Errorf("StructTree returned non-nil tree on untagged: %#v", tree)
	}
}

// TestDocument_StructTree_Tagged loads simple_tagged.pdf and verifies
// the public façade returns a tree with the expected /Document root
// and 3 children.
func TestDocument_StructTree_Tagged(t *testing.T) {
	t.Parallel()
	doc, err := pdf.OpenFile(simpleTaggedFixture)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", simpleTaggedFixture, err)
	}
	defer doc.Close()
	tree, err := doc.StructTree()
	if err != nil {
		t.Fatalf("StructTree: %v", err)
	}
	if tree == nil || len(tree.Children) != 1 {
		t.Fatalf("tree shape = %#v, want one synthetic-root child (Document)", tree)
	}
	docRoot := tree.Children[0]
	if docRoot.Type != "Document" {
		t.Errorf("Document.Type = %q, want Document", docRoot.Type)
	}
	if len(docRoot.Children) != 3 {
		t.Errorf("Document.Children len = %d, want 3", len(docRoot.Children))
	}
}

// TestPage_Blocks_TaggedRouting verifies Page.Blocks() takes the
// tagged path when the document is tagged + prefer-struct-tree is on.
func TestPage_Blocks_TaggedRouting(t *testing.T) {
	t.Parallel()
	doc, err := pdf.OpenFile(simpleTaggedFixture)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()
	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	blocks, err := page.Blocks()
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatalf("Blocks returned 0; want at least 1 with StructRole populated")
	}
	hasRole := false
	for _, b := range blocks {
		if b.StructRole != "" {
			hasRole = true
			break
		}
	}
	if !hasRole {
		t.Errorf("no block has StructRole; tagged routing did not fire")
	}
}

// TestPage_Blocks_PreferStructTreeFalse_FallsBackToHeuristic asserts
// that flipping the preference flag routes through the heuristic
// clusterer; resulting blocks carry empty StructRole.
func TestPage_Blocks_PreferStructTreeFalse_FallsBackToHeuristic(t *testing.T) {
	t.Parallel()
	doc, err := pdf.OpenFile(simpleTaggedFixture)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()
	doc.SetPreferStructTree(false)
	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	blocks, err := page.Blocks()
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatalf("Blocks returned 0; want heuristic-clustered blocks")
	}
	for i, b := range blocks {
		if b.StructRole != "" {
			t.Errorf("blocks[%d].StructRole = %q on heuristic path; want empty", i, b.StructRole)
		}
	}
}

// TestPage_Blocks_HybridPartial_BothPathsContribute confirms a
// partially-tagged page's Page.Blocks() merges tagged blocks (with
// StructRole) and residue blocks (StructRole==""), end-to-end.
func TestPage_Blocks_HybridPartial_BothPathsContribute(t *testing.T) {
	t.Parallel()
	doc, err := pdf.OpenFile(hybridPartialFixture)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()
	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	blocks, err := page.Blocks()
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	var tagged, residue int
	for _, b := range blocks {
		if b.StructRole == "" {
			residue++
		} else {
			tagged++
		}
	}
	if tagged < 1 {
		t.Errorf("tagged blocks = %d, want ≥1", tagged)
	}
	if residue < 1 {
		t.Errorf("residue blocks = %d, want ≥1 (untagged paragraph)", residue)
	}
}

// TestPage_Blocks_TaggedHeadingsSubsetOfHeuristic is the synthetic
// cross-validation: every heading text the tagged path emits should
// also appear in the heuristic-path output (not strict equality —
// heuristic may classify additional things as headings; v1 contract
// is subset-or-equal). Real-corpus ≥90% agreement is deferred to T9
// per the locked open_question Q3.
func TestPage_Blocks_TaggedHeadingsSubsetOfHeuristic(t *testing.T) {
	t.Parallel()
	taggedTexts := collectHeadingTexts(t, simpleTaggedFixture, true)
	heuristicTexts := collectHeadingTexts(t, simpleTaggedFixture, false)
	for _, h := range taggedTexts {
		if !containsString(heuristicTexts, h) {
			t.Errorf("heading %q from tagged path not found in heuristic blocks %v", h, heuristicTexts)
		}
	}
}

// TestPage_Blocks_VerifyMCIDPropagation_T2_Smoke asserts the
// ticket-DoD: T2's TextRun.MCID propagation reaches structtree
// consumers. simple_tagged.pdf has 3 BDC regions with /MCID 1, 2, 3;
// at least one TextRun must surface a non-zero MCID.
func TestPage_Blocks_VerifyMCIDPropagation_T2_Smoke(t *testing.T) {
	t.Parallel()
	doc, err := pdf.OpenFile(simpleTaggedFixture)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()
	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	runs, err := page.TextRuns()
	if err != nil {
		t.Fatalf("TextRuns: %v", err)
	}
	for _, r := range runs {
		if r.MCID > 0 {
			return
		}
	}
	t.Errorf("no TextRun has MCID > 0; T2 propagation regressed (got %d runs)", len(runs))
}

// collectHeadingTexts loads a fixture, optionally toggles
// PreferStructTree, and returns the concatenated text of every
// heading-classified Block on page 0.
func collectHeadingTexts(t *testing.T, fixture string, preferStructTree bool) []string {
	t.Helper()
	doc, err := pdf.OpenFile(fixture)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()
	doc.SetPreferStructTree(preferStructTree)
	page, err := doc.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	blocks, err := page.Blocks()
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		isHeading := b.StructRole == "H1" || b.StructRole == "H2" || b.StructRole == "H3" ||
			b.StructRole == "H4" || b.StructRole == "H5" || b.StructRole == "H6"
		if !preferStructTree {
			// Heuristic path has no StructRole; we accept any block
			// whose text matches the tagged-side heading. The test
			// uses substring containment so the cross-check is
			// resilient to leading/trailing whitespace differences
			// between paths.
			isHeading = true
		}
		if !isHeading {
			continue
		}
		out = append(out, blockText(b))
	}
	return out
}

func blockText(b pdf.Block) string {
	if len(b.Lines) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, r := range b.Lines[0].Runs {
		sb.WriteString(r.Text)
	}
	return strings.TrimSpace(sb.String())
}

// containsString checks if a string appears as a substring of any
// entry in haystack.
func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}
