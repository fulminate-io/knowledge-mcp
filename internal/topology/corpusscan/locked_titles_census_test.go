// SPDX-License-Identifier: Apache-2.0

// locked_titles_census_test.go — the locked finding titles, censused FROM THE
// DECLARATION rather than from a list a test author retyped.
//
// WHY THIS FILE EXISTS. Two tests in this package walked the title constants by
// hand, and both went stale the moment a title was added: the agreement between
// IsLeadFinding and ClassifyRun was asserted over eight of the block's titles,
// and the trailing-space rule over five of them. A title missing from that walk
// is exactly the drift ClassifyRun's own default arm punishes in production — an
// unrecognized title counts as a flagged site, so a clean corpus renders FLAGGED
// with a non-zero exit — and a hand list cannot catch it, because the same
// omission that breaks the product breaks the test's coverage of it.
//
// SO THE LIST COMES OUT OF THE SOURCE. The census parses vocabulary.go, takes
// the const block whose doc comment declares it the locked finding titles, and
// returns every name and value in it. A constant added to that block is walked
// by every row below on the next run with no test edit at all.
//
// THE PARSE IS PROVEN AGAINST THE COMPILER before anything is derived from it: a
// control asserts that the value the parser read for a known constant is the
// value the package compiled. Without it a parser that silently returned nothing
// would make every row below vacuous.

package corpusscan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// lockedTitleBlockMarker is the sentence in the const block's doc comment that
// identifies it. It is matched rather than the block's position, because a block
// found by index moves the moment a const is added above it.
const lockedTitleBlockMarker = "The locked finding titles."

// lockedTitles parses vocabulary.go and returns every constant declared in the
// locked-title block, as name to value.
func lockedTitles(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "vocabulary.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse vocabulary.go: %v", err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST || gen.Doc == nil ||
			!strings.Contains(gen.Doc.Text(), lockedTitleBlockMarker) {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				t.Fatalf("the locked-title block holds a spec this census cannot read: %v", vs)
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Fatalf("%s is not a string literal; every locked title is one", vs.Names[0].Name)
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", vs.Names[0].Name, err)
			}
			out[vs.Names[0].Name] = value
		}
	}
	if len(out) == 0 {
		t.Fatalf("the census found no locked-title block — it matches on %q in the block's doc comment", lockedTitleBlockMarker)
	}
	return out
}

// TestVocabulary_LockedTitlesAreCensusedFromTheDeclaration is the census's own
// control, and it is what every other row here rests on.
func TestVocabulary_LockedTitlesAreCensusedFromTheDeclaration(t *testing.T) {
	titles := lockedTitles(t)

	// THE PARSE AGREES WITH THE COMPILER. Two constants, one prefix and one whole
	// title, read out of the source and compared against the values this test
	// binary was built with. A census that read the wrong file, or nothing, fails
	// here rather than passing every row below vacuously.
	if got := titles["RefusalPrefixUnvalidated"]; got != RefusalPrefixUnvalidated {
		t.Fatalf("the census read RefusalPrefixUnvalidated as %q; the compiler has %q", got, RefusalPrefixUnvalidated)
	}
	if got := titles["DisclosureTitleLLMOnly"]; got != DisclosureTitleLLMOnly {
		t.Fatalf("the census read DisclosureTitleLLMOnly as %q; the compiler has %q", got, DisclosureTitleLLMOnly)
	}

	// EVERY TITLE IS THIS ANALYZER'S OWN, which is the property that makes the
	// prefix matching in both taxonomies safe.
	for name, value := range titles {
		if !strings.HasPrefix(value, AnalyzerName+": ") {
			t.Errorf("%s=%q must open with the analyzer's own name, or a consumer cannot tell whose finding it is", name, value)
		}
	}
	t.Logf("the locked-title block declares %d titles: %s", len(titles), strings.Join(sortedNames(titles), ", "))
}

// TestVocabulary_LockedTitleNamingRuleDecidesTheTrailingSpace replaces the
// hand-listed trailing-space test.
//
// THE NAME IS THE RULE. A constant whose name says Prefix has an id
// concatenated directly after it and MUST end in a space; one whose name says
// Title describes a set or a run and must NOT. Deriving the expectation from the
// name means a constant added later is judged by the same rule with no edit
// here, and it makes the naming convention itself load-bearing rather than
// decorative.
func TestVocabulary_LockedTitleNamingRuleDecidesTheTrailingSpace(t *testing.T) {
	titles := lockedTitles(t)
	prefixes, wholes := 0, 0
	for name, value := range titles {
		switch {
		case strings.Contains(name, "Prefix"):
			prefixes++
			if !strings.HasSuffix(value, " ") {
				t.Errorf("%s=%q is named a Prefix, so an id is concatenated after it and it must end in a space", name, value)
			}
		case strings.Contains(name, "Title"):
			wholes++
			if strings.HasSuffix(value, " ") {
				t.Errorf("%s=%q is named a Title, so nothing follows it and it must not end in a space", name, value)
			}
		default:
			t.Errorf("%s is neither a Prefix nor a Title by name, so this rule cannot judge it — name it for what it is", name)
		}
	}
	// BOTH CLASSES MUST BE POPULATED or the rule above is asserted in one
	// direction only and a bug in the other is invisible.
	if prefixes == 0 || wholes == 0 {
		t.Fatalf("control: the block must hold both classes, got %d prefixes and %d whole titles", prefixes, wholes)
	}
	t.Logf("%d titles carry an id and end in a space; %d are whole titles and do not", prefixes, wholes)
}

// TestVocabulary_EveryLockedTitleIsLeadOnBothTaxonomies is the agreement test,
// now walking every declared title rather than the eight somebody listed.
//
// IT IS THE DRIFT GATE THE HAND LIST COULD NOT BE. ClassifyRun's default arm
// counts an unrecognized title as a flagged site, so a disclosure added without
// an arm in the fold turns every clean run FLAGGED — and the hand-listed walk
// stayed green through exactly that, because a title nobody added to the
// production switch is a title nobody added to the test either.
func TestVocabulary_EveryLockedTitleIsLeadOnBothTaxonomies(t *testing.T) {
	titles := lockedTitles(t)
	for name, value := range titles {
		// A prefix is never emitted bare: an id follows it, so the title under
		// test is the concatenation a producer actually builds.
		title := value
		if strings.Contains(name, "Prefix") {
			title += "chk-1"
		}
		f := foundation.Finding{Algorithm: AnalyzerName, Title: title}
		if !IsLeadFinding(f) {
			t.Errorf("%s produces %q, which is one of this analyzer's own findings and must not be compacted into a site row", name, title)
		}
		if v := ClassifyRun([]foundation.Finding{f}); v.SitesFlagged != 0 {
			t.Errorf("%s produces %q, which must have an arm in the verdict fold — without one every run carrying it reads FLAGGED", name, title)
		}
	}

	// THE FALSIFYING CONTROL: a real site is lead on neither side, so the
	// agreement above is a classification rather than a predicate that says yes.
	site := astSite("pkg/a.go", "7", "chk-1", "no naked defer Close")
	if IsLeadFinding(site) {
		t.Error("control: a flagged site must compact")
	}
	if v := ClassifyRun([]foundation.Finding{site}); v.SitesFlagged != 1 {
		t.Errorf("control: and it must fold as one flagged site, got %d", v.SitesFlagged)
	}
}

// sortedNames renders a census's keys in a stable order for a log line.
func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
