// SPDX-License-Identifier: Apache-2.0

// regression_guards_test.go — the defect map's Appendix (i-a) verified passes,
// re-proved against the rebuilt fidelity core.
//
// WHY THIS EXISTS SEPARATELY FROM THE PHASE TESTS. Everything here was
// ground-truthed by an audit arm and found CORRECT before the rebuild, so no
// phase of the rebuild has a test that would go red if it were traded away.
// That is exactly the shape a silent regression hides in: the fix's own tests
// stay green while something the fix never mentioned stops working.
//
// THE FOUR FAMILIES:
//
//   - ENCODING EXACTNESS. Byte offsets must stay true byte offsets under
//     multibyte UTF-8, a 3-byte BOM and CRLF line endings. This is the most
//     plausible way the source-anchored splice breaks something that was right:
//     every bound it computes comes from a tree-sitter StartByte/EndByte, and a
//     single rune-index or line/column round trip anywhere in the path would
//     corrupt precisely the files whose byte and rune offsets diverge.
//   - OVERLAP REFUSAL. Overlapping and nested matches refuse the file WHOLE.
//     Driven end to end through ApplyReplace here, where the landed unit test
//     drives buildFileEdits directly — a refusal that stops working between the
//     two is invisible to the unit test.
//   - WHERE-TREE LEAVES, including the same_node/same_text distinction. They are
//     evaluated after the structural match, so a matcher change moves what they
//     are handed. Defect map Appendix (iii) item 12 records that these leaves
//     were never exercised in the JVM grammars, so one row is Java.
//   - JSX DISCRIMINATION. Named attributes still exclude a bare-element pattern,
//     and jsx_element and jsx_self_closing_element remain distinct kinds.
//
// EVERY ROW CARRIES A KNOWN-POSITIVE CONTROL IN THE SAME RUN. A guard asserting
// an absence — no diff, no match, a refusal — is indistinguishable from a probe
// that silently stopped working; the audit's own vacuous zeros happened exactly
// that way. So each absence row is paired with a presence row over the same
// fixture: the identity rows have a rewriting sibling, the refusal row has a
// disjoint-match sibling, each where-tree leaf has a filter that passes beside
// one that rejects, and the JSX exclusion has the bare element it must still
// match.

package ast

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// guardMultibyteFixture mixes CJK, Cyrillic and emoji so byte offsets and rune
// offsets diverge from the first string literal onward.
const guardMultibyteFixture = "package p\n\n" +
	"func f() {\n" +
	"\tcjk := \"日本語のテキスト\"\n" +
	"\tcyr := \"Привет, мир\"\n" +
	"\temo := \"🚀🔥\"\n" +
	"\tuse(cjk, cyr, emo)\n" +
	"}\n"

// guardBOMFixture opens with the 3-byte UTF-8 BOM, which shifts every offset in
// the file by three.
const guardBOMFixture = "\uFEFF" + "package p\n\nfunc f() {\n\tuse(alpha)\n}\n"

// guardCRLFFixture uses CRLF throughout: a splice that reassembled lines rather
// than copying byte ranges would silently normalize them.
const guardCRLFFixture = "package p\r\n\r\nfunc f() {\r\n\tuse(alpha)\r\n\tuse(beta)\r\n}\r\n"

// guardNilCloseFixture binds two captures whose TEXT is equal but whose NODES
// are distinct occurrences — the fixture the same_node/same_text distinction
// needs. The second function is the negative: same shape, different names.
//
// The bodies are multi-line, like guardJavaFixture below: that is the shape
// real Go source has, and a where-tree row that only ever ran against a
// one-line body would leave the leaves unexercised over the layout the matcher
// actually meets.
const guardNilCloseFixture = "package p\n\n" +
	"func same() {\n\tif conn != nil {\n\t\tconn.Close()\n\t}\n}\n\n" +
	"func other() {\n\tif conn != nil {\n\t\tsocket.Close()\n\t}\n}\n"

// guardJavaFixture is the JVM-grammar row's target: the same null-check shape,
// once closing the checked reference and once closing another.
const guardJavaFixture = "class Guard {\n" +
	"\tvoid same() {\n\t\tif (conn != null) {\n\t\t\tconn.close();\n\t\t}\n\t}\n" +
	"\tvoid other() {\n\t\tif (conn != null) {\n\t\t\tsocket.close();\n\t\t}\n\t}\n}\n"

// guardJSXFixture holds an attributed element, a bare element and a
// self-closing one, so each JSX row's want set is the other rows' exclusion.
const guardJSXFixture = "function App() {\n  return <div className=\"x\">{attributed}</div>;\n}\n" +
	"function Bare() {\n  return <div>{plain}</div>;\n}\n" +
	"function Self() {\n  return <Widget />;\n}\n"

// runGuardWhere compiles pattern under cfg, walks target, and counts the
// matches its where-tree accepts. The language-parameterized twin of
// go_where_test.go's runWhere, which now delegates here.
func runGuardWhere(t *testing.T, cfg LangConfig, pattern, target, whereJSON string) (int, error) {
	t.Helper()
	pt, err := compilePattern(context.Background(), mustParse(t, pattern), cfg)
	if err != nil {
		t.Fatalf("compilePattern(lang=%q, %q): %v", cfg.Lang, pattern, err)
	}
	defer pt.Close()

	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(target), cfg.Lang)
	if err != nil {
		t.Fatalf("parse %q target: %v", cfg.Lang, err)
	}
	defer tree.Close()

	where, err := ParseWhere([]byte(whereJSON))
	if err != nil {
		t.Fatalf("ParseWhere(%s): %v", whereJSON, err)
	}

	cache := map[string][]patternVariant{}
	mu := &sync.Mutex{}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, variants := range cache {
			closeVariants(variants)
		}
	}()

	outerScope := newOuterScope(cfg.Lang, cache, mu)
	src := []byte(target)
	matches, finalErr := 0, error(nil)
	walkAll(tree.RootNode(), func(n *sitter.Node) {
		caps := newCaptures()
		nodes := map[string]*sitter.Node{}
		if !matchTreeWithNodes(pt, n, src, caps, nodes) {
			return
		}
		ok, evalErr := evalWhere(context.Background(), where, outerScope.withMatchCaptures(caps, nodes, src))
		if evalErr != nil {
			if finalErr == nil {
				finalErr = evalErr
			}
			return
		}
		if ok {
			matches++
		}
	})
	return matches, finalErr
}

// wantWhereCount runs one where-tree and asserts the accepted-match count.
func wantWhereCount(t *testing.T, cfg LangConfig, pattern, target, whereJSON string, want int, why string) {
	t.Helper()
	got, err := runGuardWhere(t, cfg, pattern, target, whereJSON)
	if err != nil {
		t.Fatalf("where eval: %v\n  where: %s", err, whereJSON)
	}
	if got != want {
		t.Errorf("accepted %d matches, want %d — %s\n  where: %s", got, want, why, whereJSON)
	}
}

// requireNoDiff asserts every rendered diff is EMPTY. ApplyReplace records an
// entry for each file it touched even when the splice reproduced that file byte
// for byte, so an entry's presence is not the signal — its content is.
func requireNoDiff(t *testing.T, res ReplaceResult, why string) {
	t.Helper()
	for path, diff := range res.Diffs {
		if diff != "" {
			t.Errorf("identity template rewrote %s (%s):\n%s", path, why, diff)
		}
	}
}

// outerKinds collects the deduplicated, sorted set of outer node kinds a
// pattern matched, which is how a row asserts that two grammar kinds stayed
// distinct rather than collapsing into one.
func outerKinds(matches []walkerMatch) []string {
	seen := map[string]bool{}
	for _, m := range matches {
		seen[m.outer] = true
	}
	out := make([]string, 0, len(seen))
	for kind := range seen {
		out = append(out, kind)
	}
	slices.Sort(out)
	return out
}

//nolint:gocognit,maintidx // a flat table of independent guard rows; splitting it would scatter the appendix it mirrors
func TestRegressionGuards(t *testing.T) {
	// ---- ENCODING EXACTNESS -----------------------------------------------

	t.Run("encoding_multibyte_identity_is_a_no_op", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"main.go": guardMultibyteFixture})
		res, matches := runSplice(t, dir, treesitter.LangGo, "use($$$A)", "use($$$A)", true)
		if matches != 1 {
			t.Fatalf("matched %d call sites, want 1 — an identity replace over zero matches proves nothing", matches)
		}
		requireNoDiff(t, res, "multibyte content")
	})

	t.Run("encoding_multibyte_rewrite_hits_the_intended_bytes", func(t *testing.T) {
		got, matches := spliceRewrite(t, treesitter.LangGo, "main.go", guardMultibyteFixture,
			"use($$$A)", "log($$$A)")
		if matches != 1 {
			t.Fatalf("matched %d call sites, want 1", matches)
		}
		want := strings.Replace(guardMultibyteFixture, "\tuse(cjk", "\tlog(cjk", 1)
		if got != want {
			t.Errorf("rewrite under multibyte content produced:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("encoding_bom_identity_is_a_no_op", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"main.go": guardBOMFixture})
		res, matches := runSplice(t, dir, treesitter.LangGo, "use($X)", "use($X)", true)
		if matches != 1 {
			t.Fatalf("matched %d call sites behind a BOM, want 1", matches)
		}
		requireNoDiff(t, res, "behind a 3-byte BOM")
	})

	t.Run("encoding_bom_survives_a_rewrite", func(t *testing.T) {
		got, matches := spliceRewrite(t, treesitter.LangGo, "main.go", guardBOMFixture,
			"use($X)", "log($X)")
		if matches != 1 {
			t.Fatalf("matched %d call sites behind a BOM, want 1", matches)
		}
		if !strings.HasPrefix(got, "\uFEFF") {
			t.Errorf("the BOM did not survive the rewrite; output starts %q", got[:min(9, len(got))])
		}
		if want := strings.Replace(guardBOMFixture, "use(alpha)", "log(alpha)", 1); got != want {
			t.Errorf("rewrite behind a BOM produced:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("encoding_crlf_identity_is_a_no_op", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"main.go": guardCRLFFixture})
		res, matches := runSplice(t, dir, treesitter.LangGo, "use($X)", "use($X)", true)
		if matches != 2 {
			t.Fatalf("matched %d call sites, want 2", matches)
		}
		requireNoDiff(t, res, "CRLF line endings")
	})

	t.Run("encoding_crlf_line_endings_survive_a_rewrite", func(t *testing.T) {
		got, matches := spliceRewrite(t, treesitter.LangGo, "main.go", guardCRLFFixture,
			"use($X)", "log($X)")
		if matches != 2 {
			t.Fatalf("matched %d call sites, want 2", matches)
		}
		if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
			t.Errorf("a bare LF survived in the rewritten CRLF file:\n%q", got)
		}
		want := strings.ReplaceAll(guardCRLFFixture, "use(", "log(")
		if got != want {
			t.Errorf("CRLF rewrite produced:\n%q\nwant:\n%q", got, want)
		}
	})

	// ---- OVERLAP REFUSAL --------------------------------------------------

	t.Run("overlap_nested_matches_refuse_the_file_whole", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{
			"main.go": "package p\n\nfunc f() {\n\tuse(use(alpha))\n}\n",
		})
		res, matches := runSplice(t, dir, treesitter.LangGo, "use($X)", "log($X)", true)
		if matches != 2 {
			t.Fatalf("matched %d call sites, want 2 (the nested pair that must trigger the refusal)", matches)
		}
		if !slices.Contains(res.RefusedFiles, "main.go") {
			t.Errorf("RefusedFiles = %v, want main.go — nested matches must refuse the file whole", res.RefusedFiles)
		}
		if res.FilesMatched != 0 || len(res.Diffs) != 0 {
			t.Errorf("a refused file was still edited: FilesMatched=%d Diffs=%v", res.FilesMatched, res.Diffs)
		}
	})

	t.Run("overlap_disjoint_matches_are_applied", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{
			"main.go": "package p\n\nfunc f() {\n\tuse(alpha)\n\tuse(beta)\n}\n",
		})
		res, matches := runSplice(t, dir, treesitter.LangGo, "use($X)", "log($X)", true)
		if matches != 2 {
			t.Fatalf("matched %d call sites, want 2", matches)
		}
		if len(res.RefusedFiles) != 0 {
			t.Errorf("RefusedFiles = %v, want none — disjoint matches must not be refused", res.RefusedFiles)
		}
		// A real rewrite: matched and changed carry the same number here, and
		// asserting the CHANGED one is what this guard actually means.
		if res.FilesChanged != 1 || res.MatchesChanged != 2 {
			t.Errorf("FilesChanged=%d MatchesChanged=%d, want 1 and 2", res.FilesChanged, res.MatchesChanged)
		}
		if res.FilesMatched != 1 || res.MatchesReplaced != 2 {
			t.Errorf("FilesMatched=%d MatchesReplaced=%d, want 1 and 2", res.FilesMatched, res.MatchesReplaced)
		}
	})

	// ---- WHERE-TREE LEAVES ------------------------------------------------

	const closeTarget = "package p\nfunc f() { db.Close(); httpClient.Close(); x.Close() }\n"

	t.Run("where_matches_leaf", func(t *testing.T) {
		wantWhereCount(t, goLangConfig, "$X.Close()", closeTarget,
			`{"matches": {"of": "X", "regex": "^db"}}`, 1, "only db.Close matches the regex")
		wantWhereCount(t, goLangConfig, "$X.Close()", closeTarget,
			`{"matches": {"of": "X", "regex": "^zz"}}`, 0, "no receiver starts with zz")
	})

	t.Run("where_equals_and_not_leaves", func(t *testing.T) {
		wantWhereCount(t, goLangConfig, "$X.Close()", closeTarget,
			`{"equals": {"of": "X", "value": "x"}}`, 1, "exactly one receiver is spelled x")
		wantWhereCount(t, goLangConfig, "$X.Close()", closeTarget,
			`{"not": {"equals": {"of": "X", "value": "x"}}}`, 2, "not must invert the same leaf, not reject everything")
	})

	t.Run("where_kind_leaf", func(t *testing.T) {
		const mixed = "package p\nfunc f() { pkg.Type.Close(); x.Close() }\n"
		wantWhereCount(t, goLangConfig, "$X.Close()", mixed,
			`{"kind": {"of": "X", "is": "identifier"}}`, 1, "one receiver is a bare identifier")
		wantWhereCount(t, goLangConfig, "$X.Close()", mixed,
			`{"kind": {"of": "X", "is": "selector_expression"}}`, 1, "the other is a selector")
	})

	t.Run("where_inside_pattern_leaf", func(t *testing.T) {
		const nested = "package p\nfunc keep() { x.Close() }\nfunc drop() { y.Close() }\n"
		wantWhereCount(t, goLangConfig, "$X.Close()", nested,
			`{"inside_pattern": {"of": "$match", "pattern": "func keep() { $$$B }"}}`, 1,
			"only the call inside keep() has that ancestor")
		wantWhereCount(t, goLangConfig, "$X.Close()", nested,
			`{"inside_pattern": {"of": "$match", "pattern": "func absent() { $$$B }"}}`, 0,
			"no such ancestor exists")
	})

	t.Run("where_contains_pattern_leaf", func(t *testing.T) {
		const bodies = "package p\nfunc a() { x.Close() }\nfunc b() { x.Open() }\n"
		wantWhereCount(t, goLangConfig, "$_", bodies,
			`{"all": [{"kind": {"of": "$match", "is": "function_declaration"}},
			           {"contains_pattern": {"of": "$match", "pattern": "$Y.Close()"}}]}`, 1,
			"one function body contains a Close call")
		wantWhereCount(t, goLangConfig, "$_", bodies,
			`{"all": [{"kind": {"of": "$match", "is": "function_declaration"}},
			           {"contains_pattern": {"of": "$match", "pattern": "$Y.Flush()"}}]}`, 0,
			"neither body contains a Flush call")
	})

	// THE COLLAPSE-CATCHER. $A and $B bind DIFFERENT occurrences of the same
	// identifier text in the `same` function, so same_text accepts it and
	// same_node must not. A same_node that had collapsed into same_text would
	// accept it too, and every other row here would stay green.
	t.Run("where_same_node_and_same_text_stay_distinct", func(t *testing.T) {
		const pattern = "if $A != nil { $B.Close() }"
		wantWhereCount(t, goLangConfig, pattern, guardNilCloseFixture,
			`{"same_text": {"captures": ["A", "B"]}}`, 1,
			"one function checks and closes the same NAME")
		wantWhereCount(t, goLangConfig, pattern, guardNilCloseFixture,
			`{"same_node": {"captures": ["A", "B"]}}`, 0,
			"two occurrences of one name are two NODES; a same_node that accepts here has collapsed into same_text")
		wantWhereCount(t, goLangConfig, pattern, guardNilCloseFixture,
			`{"same_node": {"captures": ["A", "A"]}}`, 2,
			"known positive: same_node still accepts a capture against itself, so the zero above is a refusal and not a dead probe")
	})

	t.Run("where_outer_scope_chain", func(t *testing.T) {
		wantWhereCount(t, goLangConfig, "if $A != nil { $$$B }", guardNilCloseFixture,
			`{"contains_pattern": {"of": "$match", "pattern": "$Y.Close()",
			   "where": {"same_text": {"captures": ["Y", "$outer.A"]}}}}`, 1,
			"only one guard closes the reference it checked")
		wantWhereCount(t, goLangConfig, "if $A != nil { $$$B }", guardNilCloseFixture,
			`{"contains_pattern": {"of": "$match", "pattern": "$Y.Close()",
			   "where": {"not": {"same_text": {"captures": ["Y", "$outer.A"]}}}}}`, 1,
			"and the other closes a different one — the two together prove $outer.A resolved rather than always failing")
	})

	// Defect map Appendix (iii) item 12: these leaves were never exercised in a
	// JVM grammar. Java's null-check shape is the same probe in a different
	// tree, so a leaf that depends on Go's node shapes shows up here.
	t.Run("where_leaves_in_a_jvm_grammar", func(t *testing.T) {
		cfg, ok := langConfigFor(treesitter.LangJava)
		if !ok {
			t.Fatalf("no LangConfig registered for Java")
		}
		const pattern = "if ($A != null) { $B.close(); }"
		wantWhereCount(t, cfg, pattern, guardJavaFixture,
			`{"same_text": {"captures": ["A", "B"]}}`, 1,
			"one Java guard closes the reference it checked")
		wantWhereCount(t, cfg, pattern, guardJavaFixture,
			`{"same_node": {"captures": ["A", "B"]}}`, 0,
			"same_node must still reject two occurrences under a JVM grammar")
		wantWhereCount(t, cfg, pattern, guardJavaFixture,
			`{"equals": {"of": "B", "value": "socket"}}`, 1,
			"known positive: the equals leaf reads Java captures at all")
	})

	// ---- JSX DISCRIMINATION -----------------------------------------------

	t.Run("jsx_named_attribute_still_excludes_a_bare_element_pattern", func(t *testing.T) {
		matches := runLongTailWalker(t, tsxLangConfig, "<div>$$$C</div>", guardJSXFixture)
		got := capturedTexts(matches, "C")
		if want := []string{"{plain}"}; !slices.Equal(got, want) {
			t.Errorf("$C bound %v, want %v — the bare pattern must match the bare element and exclude the attributed one",
				got, want)
		}
	})

	t.Run("jsx_element_and_self_closing_element_stay_distinct_kinds", func(t *testing.T) {
		selfClosing := runLongTailWalker(t, tsxLangConfig, "<Widget />", guardJSXFixture)
		if got, want := outerKinds(selfClosing), []string{"jsx_self_closing_element"}; !slices.Equal(got, want) {
			t.Errorf("`<Widget />` matched kinds %v, want %v", got, want)
		}
		paired := runLongTailWalker(t, tsxLangConfig, "<div>$$$C</div>", guardJSXFixture)
		if got, want := outerKinds(paired), []string{"jsx_element"}; !slices.Equal(got, want) {
			t.Errorf("`<div>$$$C</div>` matched kinds %v, want %v — the two kinds must not collapse", got, want)
		}
	})
}
