// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// multiLangRow is one language's picture inside one corpus.
//
// IT EMBEDS the landed corpusLangRow rather than restating it, so the shared
// columns keep one definition and one set of json tags; embedding an anonymous
// struct with no tag of its own FLATTENS those columns into this row's object.
// baseline_calls_edges rides along from the embedded type and is ALWAYS 0 here:
// this artifact has no predecessor state and asserts no did-not-fall comparison.
//
// WHY BOTH HALVES ARE RECORDED. bound alone cannot say a BINDS ARM worked,
// because local scope rules produce bound references with no arm involved.
// BindsFiles and BindsEntries are the LIVENESS half — what the pass filled;
// BoundByRule is the BEHAVIOR half — which ladder rung consumed it. They are
// separately omissible and therefore separately recorded, mirroring the Go row's
// own split at corpus_verification_go_test.go:47-68.
type multiLangRow struct {
	corpusLangRow
	// References is the DENOMINATOR OF THE SAME POPULATION the embedded bound
	// column and BoundByRule below range over: reference-bearing CALLS and
	// USES_TYPE edges, resolved or not. It is deliberately NOT every ref-bearing
	// edge — a wider reference count beside a narrower bound would make
	// references-minus-bound read as unresolved references when part of the
	// difference is only an edge type bound never counted.
	References int `json:"references"`
	// BindsFiles and BindsEntries are THE LIVENESS CONTROL. Zero here with
	// everything else unchanged is the signature of an arm that never ran —
	// the module path never reaching it, or the pass never running — and
	// without them a flat result reads as "no improvement" rather than "the arm
	// never ran".
	BindsFiles   int `json:"binds_files"`
	BindsEntries int `json:"binds_entries"`
	// BindsScopesUnknown is THE R2X INPUT CONTROL: recorded binds whose Scope
	// the declaration index does not hold, which is what the external-qualifier
	// rung consumes.
	BindsScopesUnknown int `json:"binds_scopes_unknown"`
	// BoundByRule maps the RefRule's recorded string value to the number of
	// bound references that rung produced. It is ALWAYS emitted, including when
	// empty: a map that only appears when non-empty cannot distinguish "no
	// reference bound" from "the census was never wired".
	BoundByRule map[string]int `json:"bound_by_rule"`
	// AmbiguousRefs and AmbiguousByRule are THE RUNG-FIRING HALF, and they exist
	// because BoundByRule alone cannot say a RUNG FIRED. classify returns
	// RefBound only at cardinality one (resolve_walk.go:354-359), so a rung that
	// runs correctly and finds several equally-valid declarations contributes
	// nothing to BoundByRule — a corpus that ships its library twice drives a
	// live rung's bound count toward zero while the rung fires on every
	// reference. FIRING is bound plus ambiguous attributed to the same rung.
	//
	// AmbiguousRefs IS THE AMBIGUOUS MAP'S OWN DENOMINATOR, counted over the
	// identical filtered walk, and it is deliberately NOT the row's
	// ambiguous_groups column: that column counts DISTINCT groupKeys among
	// ambiguous-method EDGES, a different derivation whose equality with this
	// reference count is unmeasured. A split must be checked against the
	// population it is a split of.
	AmbiguousRefs   int            `json:"ambiguous_refs"`
	AmbiguousByRule map[string]int `json:"ambiguous_by_rule"`
	// ImplementsEdges and ImplementsMethodEdges count the DECLARED-CONFORMANCE
	// relationships this language's declarations produced: the type-level pairs
	// and, beneath them, the member pairs.
	//
	// THEY ARE ATTRIBUTED TO THE SUBTYPE'S LANGUAGE, not the supertype's,
	// because the subtype is the declaration that WROTE the clause and whose
	// capture arm read it — which is the thing a per-language column here is
	// measuring.
	//
	// THEY ARE COUNTS AND NOTHING ELSE. The edge's Method vocabulary is the
	// emitter's, and a copy of it here would be a second definition free to
	// drift from the first.
	ImplementsEdges       int `json:"implements_edges"`
	ImplementsMethodEdges int `json:"implements_method_edges"`
}

// multiLangRows joins the landed per-language resolution columns to this
// ticket's binds-and-rule census.
//
// REUSE, NOT REIMPLEMENTATION: the resolution columns come from
// perLanguageRows — the same function the landed artifact's generator uses —
// called with this root's own results.
func multiLangRows(
	t *testing.T, results []*treesitter.Result, ix *declIndex,
	edges []*knowledgev1.Edge, ownerLang map[string]string,
) []multiLangRow {
	t.Helper()
	census := censusByLanguage(results, ix)
	base := perLanguageRows(t, results, ix, edges, ownerLang)
	types, members := conformanceCounts(ix, ownerLang)
	out := make([]multiLangRow, 0, len(base))
	for _, row := range base {
		c := census[row.Language]
		if c == nil {
			c = newCensusRow()
		}
		out = append(out, multiLangRow{
			corpusLangRow:         row,
			References:            c.references,
			BindsFiles:            c.bindsFiles,
			BindsEntries:          c.bindsEntries,
			BindsScopesUnknown:    c.bindsScopesUnknown,
			BoundByRule:           c.boundByRule,
			AmbiguousRefs:         c.ambiguous,
			AmbiguousByRule:       c.ambiguousByRule,
			ImplementsEdges:       types[row.Language],
			ImplementsMethodEdges: members[row.Language],
		})
	}
	return out
}

// conformanceCounts derives the per-language declared-conformance pair counts
// from the COMPLETE index.
//
// It calls the production derivation rather than counting emitted edges,
// because the derivation is where the type level and the member level are still
// distinguishable — the emitter flattens them into one slice whose entries
// carry byte-identical Method values by design.
func conformanceCounts(ix *declIndex, ownerLang map[string]string) (types, members map[string]int) {
	types = map[string]int{}
	members = map[string]int{}
	pairs, _ := deriveDeclaredConformance(ix)
	for _, p := range pairs {
		lang := ownerLang[p.subtype.NodeID]
		types[lang]++
		members[lang] += len(p.members)
	}
	return types, members
}

// censusByLanguage is an ADAPTER over censusWalk
// (corpus_census_walk_test.go), with a NIL language set so every language the
// corpus holds gets a row, and the narrower admitted population.
//
// IT WAS THE TEMPLATE THE SHARED WALK WAS GENERALISED FROM: it existed because
// the two committed-artifact walks — goRows (hardcoded to LangGo) and
// ecmaScriptRows (hardcoded to the three ECMAScript languages) — could not be
// called for an arbitrary language. All three now call the one walk.
//
// THE ADMITTED POPULATION IS THE NARROW ONE AND MUST STAY SO: see
// admitBoundPopulation, which carries the reason and names the test that goes
// red if it is widened. It is a parameter rather than the walk's own rule
// precisely because the two committed-artifact walks census the WIDER
// population, and neither may be moved onto the other's.
//
// One pass over results and one resolveRef per reference, reusing the index
// already built: no second index build and no second chunking pass.
func censusByLanguage(results []*treesitter.Result, ix *declIndex) map[string]*censusRow {
	return censusWalk(results, ix, censusSpec{admits: admitBoundPopulation})
}

// ful1347CensusFixture runs one in-memory fixture through the SAME census entry
// point measureCorpus uses — multiLangRows — over inputs built in the
// production ORDER (DeduplicateChunks, then resolveSlotEdges, then fillBinds,
// then the index build, then resolveEdgesWithStats), mirroring
// corpus_multi_lang_test.go:133-158. The order is not decorative: taking node
// IDs before DeduplicateChunks yields pre-rename IDs, and resolving slots after
// the chunk sort invalidates every slot index.
//
// IT RETURNS THE RESOLVED EDGES ALONGSIDE THE ROWS because the known-positive
// control the equality below needs is a property of the EDGE LIST rather than
// of the rows: sum(bound_by_rule) == bound is satisfied trivially by any corpus
// that emits no third ref-bearing edge type at all, so the third population has
// to be shown present and bound in the same run.
func ful1347CensusFixture(
	t *testing.T, files []fixtureFile,
) ([]multiLangRow, []*knowledgev1.Edge) {
	t.Helper()
	results := chunkFixture(t, files)
	DeduplicateChunks(results)
	resolveSlotEdges(results)
	fillBinds(&treesitter.RepoContext{ModulePath: "example.com/fixture"}, results)

	ix := newDeclIndex(0)
	nodeIDs := map[string]bool{}
	ownerLang := map[string]string{} // node ID → the language of the file that declared it
	for _, r := range results {
		nodeIDs[r.FilePath] = true
		ownerLang[r.FilePath] = string(r.Language)
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			id := ChunkNodeID(chunk)
			nodeIDs[id] = true
			ownerLang[id] = string(r.Language)
			indexDeclaration(ix, r, chunk, id)
		}
	}

	edges, _ := resolveEdgesWithStats(results, ix, nodeIDs)
	return multiLangRows(t, results, ix, edges, ownerLang), edges
}

// TestFUL1347RuleSplitSumsToBound proves the two halves of one row COUNT THE
// SAME EDGE POPULATION, on a fixture whose language emits a THIRD ref-bearing
// edge type beyond the CALLS and USES_TYPE pair the bound column ranges over.
//
// WHY A THIRD TYPE IS THE WHOLE FIXTURE. bound is counted for CALLS and
// USES_TYPE only (corpus_json_test.go:168-175, default: continue). Every OTHER
// ref-bearing edge — one that is neither CONTAINS nor IMPORTS and carries a
// Ref — resolves through the same ladder and would land in the rule split while
// never reaching bound, making sum(bound_by_rule) exceed bound by that
// population's size. Measured on the pinned corpora at plan time as cpp +178,
// elixir +454 and scala +127, with java exactly equal in all three corpora that
// hold it.
//
// TSX IS THE FIXTURE LANGUAGE because TEST_CALLS is that third type and is
// emitted only where a language's TestBlocks query is non-empty — tsx has one,
// and java's annotation-based JUnit gives it none, which is exactly why java
// was the language the artifact spared. A call inside an it(...) body emits a
// ref-bearing TEST_CALLS edge that binds to a declaration in the same file.
//
// WHICH TEST GOES RED IF THE SCOPING IS DROPPED: this one, and only this one.
func TestFUL1347RuleSplitSumsToBound(t *testing.T) {
	rows, edges := ful1347CensusFixture(t, []fixtureFile{{
		path: "app/Widget.test.tsx",
		src: "" +
			"export function widgetProduce(): number {\n\treturn 2;\n}\n\n" +
			"export function widgetCaller(): number {\n\treturn widgetProduce();\n}\n\n" +
			"it(\"widget case\", () => {\n\twidgetProduce();\n});\n",
	}})

	// THE KNOWN POSITIVE. A bound TEST_CALLS edge is the population that
	// separates the two counts, so without one here the equality below holds by
	// the fixture's emptiness rather than by the scoping under test.
	thirdType := 0
	for _, e := range edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeTestCalls {
			continue
		}
		if e.Method == kgtypes.EdgeMethodAmbiguousName || e.Method == kgtypes.EdgeMethodDynamic {
			continue
		}
		thirdType++
	}
	require.Positive(t, thirdType,
		"control: the fixture must emit a BOUND third-type (TEST_CALLS) edge, or this test cannot "+
			"tell a scoped rule split from an unscoped one")

	require.NotEmpty(t, rows, "control: the fixture produced no per-language row at all")
	for _, row := range rows {
		require.Positive(t, row.Bound,
			"%s: control: bound is zero, so the equality below accounts for nothing", row.Language)
		total := 0
		for _, n := range row.BoundByRule {
			total += n
		}
		require.Equal(t, row.Bound, total,
			"%s: the rule split must range over the SAME edge population as bound — "+
				"per-rule %d against bound %d is the census accounting gap, not a resolution defect",
			row.Language, total, row.Bound)
	}
}

// ful1347FlavorDuplicationFixture is the guava mechanism in miniature: ONE
// class declared TWICE under the SAME java package from two directory trees,
// and one importer of it.
//
// It is the fixture shape the corpus forced. google/guava mirrors guava/src,
// guava-tests/ and guava-testlib/ wholesale under android/, so 3,182 of its
// 3,229 java files (98.5%) share a (package, class) pair with another file.
// java indexes a declaration under the package it DECLARES rather than the
// directory it sits in, so both copies land in one scope and every lookup
// through them returns cardinality two.
//
// THE THIRD FILE IS THE REFERENCE SITE. Its bare `Preconditions` return type
// resolves through the unqualified-import rung, which finds both copies — so
// the rung fires and binds nothing, which is exactly the outcome the ambiguous
// census exists to make visible.
func ful1347FlavorDuplicationFixture() []fixtureFile {
	decl := "package com.acme.base;\n\n" +
		"public final class Preconditions {\n" +
		"\tpublic static Object checkNotNull(Object o) {\n\t\treturn o;\n\t}\n}\n"
	return []fixtureFile{
		{path: "guava/src/com/acme/base/Preconditions.java", src: decl},
		// BYTE-IDENTICAL, and that is the point: the android flavor is the same
		// class, not a different one.
		{path: "android/guava/src/com/acme/base/Preconditions.java", src: decl},
		{path: "guava/src/com/acme/collect/Lists.java", src: "" +
			"package com.acme.collect;\n\n" +
			"import com.acme.base.Preconditions;\n\n" +
			"public final class Lists {\n" +
			"\tpublic static Preconditions guard() {\n\t\treturn null;\n\t}\n}\n"},
	}
}

// TestFUL1347AmbiguousRuleSplitSumsToAmbiguous proves the SECOND rule map
// counts the ambiguous half of the SAME filtered population its bound sibling
// counts the bound half of — and that a firing rung is visible in it.
//
// WHY THE SECOND MAP EXISTS AT ALL. bound_by_rule credits a rung only when
// classify returned RefBound, which happens only at cardinality one. A rung
// that fires on thousands of references and finds two equally-valid
// declarations each time credits nothing, and a floor read off bound_by_rule
// then reports a live rung as inert. The property a non-vacuity floor wants is
// RUNG FIRING — bound plus ambiguous — so the ambiguous half has to be counted
// before any gate can name it.
//
// WHICH TEST GOES RED IF THE AMBIGUOUS CENSUS IS OMITTED OR MISWIRED: this
// one, and only this one.
func TestFUL1347AmbiguousRuleSplitSumsToAmbiguous(t *testing.T) {
	rows, _ := ful1347CensusFixture(t, ful1347FlavorDuplicationFixture())

	require.NotEmpty(t, rows, "control: the fixture produced no per-language row at all")
	var java *multiLangRow
	for i := range rows {
		if rows[i].Language == string(treesitter.LangJava) {
			java = &rows[i]
		}
	}
	require.NotNil(t, java, "control: the java fixture must produce a java row at all")

	// THE KNOWN POSITIVE, and it is the whole reason this fixture duplicates a
	// file. Without a genuinely ambiguous reference in the counted population,
	// the equality below is satisfied by zero == zero and proves nothing.
	require.Positive(t, java.AmbiguousRefs,
		"control: the duplicated declaration must make at least one counted reference ambiguous, "+
			"or the equality below holds by emptiness")
	require.Positive(t, java.AmbiguousByRule[string(RuleUnqualifiedImport)],
		"the bare Preconditions reference fires the unqualified-import rung and finds BOTH flavor "+
			"copies, so that rung must be credited in the ambiguous split — zero here is an unwired "+
			"firing half")

	// BOTH SPLITS, EACH AGAINST ITS OWN POPULATION, in one run.
	ambiguousTotal := 0
	for _, n := range java.AmbiguousByRule {
		ambiguousTotal += n
	}
	require.Equal(t, java.AmbiguousRefs, ambiguousTotal,
		"the ambiguous rule split must range over the SAME filtered population as ambiguous_refs — "+
			"per-rule %d against %d ambiguous references is a census accounting gap",
		ambiguousTotal, java.AmbiguousRefs)

	boundTotal := 0
	for _, n := range java.BoundByRule {
		boundTotal += n
	}
	require.Equal(t, java.Bound, boundTotal,
		"the bound rule split must still range over the SAME edge population as bound — "+
			"per-rule %d against bound %d", boundTotal, java.Bound)

	// THE FIRING CLAIM ITSELF, stated on the row a gate would read: the rung is
	// live here even though it bound nothing through it.
	require.Zero(t, java.BoundByRule[string(RuleUnqualifiedImport)],
		"control: on a duplicated corpus the unqualified-import rung binds NOTHING, which is the "+
			"measurement error a bound-only floor makes — a non-zero here means the fixture stopped "+
			"reproducing the mechanism")
}

// TestFUL1347BindsCensusOnAFilledSite is THE CATCHER, and it is the only wiring
// proof this ticket has.
//
// A counter that is only ever read as zero cannot be told from one that was
// never wired — and every gated language's binds number is legitimately
// uninformative on the pinned corpora: five of the seven have a binds arm that
// resolves nothing on real source, and the remaining two register no arm at all.
// So the census is driven NON-ZERO here instead, on a fixture whose import
// genuinely binds.
//
// GO IS THE FIXTURE LANGUAGE because its arm is registered at init and is known
// live on real source. WHICH TEST GOES RED IF THE CENSUS IS OMITTED OR
// MISWIRED: this one, and only this one.
func TestFUL1347BindsCensusOnAFilledSite(t *testing.T) {
	results, ix := goFixtureSites(t, []fixtureFile{
		{path: "lib/lib.go", src: "" +
			"package lib\n\nfunc Helper() int {\n\treturn 1\n}\n"},
		{path: "app/main.go", src: "" +
			"package main\n\nimport \"example.com/fixture/lib\"\n\n" +
			"func Run() int {\n\treturn lib.Helper()\n}\n"},
	})

	census := censusByLanguage(results, ix)
	got := census[string(treesitter.LangGo)]
	require.NotNil(t, got, "control: the Go fixture must produce a Go census entry at all")

	require.Positive(t, got.bindsFiles,
		"a file whose import binds must be counted as a filled site — zero here is an unwired liveness half")
	require.Positive(t, got.bindsEntries,
		"the recorded bind must be counted as an entry — zero here is an unwired liveness half")
	require.Positive(t, got.boundByRule[string(RuleQualifiedImport)],
		"lib.Helper binds THROUGH the import table, so the qualified-import rung must be credited — "+
			"zero here is an unwired behavior half")
	require.Positive(t, got.references,
		"control: the fixture emitted references at all, so the two zeros above could not be vacuous")
}
