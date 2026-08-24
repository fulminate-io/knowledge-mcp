// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// THE ONE CENSUS WALK. Three per-language measurement walks existed before this
// file — goRows (hardcoded to LangGo), ecmaScriptRows (hardcoded to the three
// ECMAScript languages) and censusByLanguage (language-generic already) — and
// each re-implemented the same pass: for every file in a language set, census
// what the binds pass filled into its reference site, then push every reference
// edge through resolveRef and attribute the outcome by STATUS and by RULE.
//
// All three are now ADAPTERS over censusWalk. They keep their own names,
// signatures and row types, because renderGo, renderECMAScript and the JSON
// artifact's row shape consume those types and the committed artifacts' byte
// layout is produced by those renderers. This is one MEASUREMENT
// implementation, not one rendering implementation.

// censusFixtureCaptureEnv gates the expectation capture. The capture is an
// INSTRUMENT and not a gate: it writes the two-code-state expectation that
// TestCensusWalkMatchesLegacyRows reads, and it must be run against the walks
// BEFORE they were made adapters — which is a property of the tree it ran in,
// recorded in the file's captured-at header, not of anything a later run can
// re-derive.
// ONE LINE OF THE EXPECTATION WAS TRANSCRIBED BY HAND, NOT RE-CAPTURED.
// The reference group key went position-independent, so the go_ambiguous_group_0
// listing re-spells the SAME group under its new identity:
// `dup/use.go:13:CALLS:Twin` became `Twin:CALLS:dup/use.go:UseTwin:0`. Same
// group, same candidate files, every COUNT in the file untouched. It was edited
// by hand rather than re-captured for the reason stated above — the file's
// validity comes from the tree the capture ran in, so re-running the capture
// against the adapters would destroy the property it exists to prove. The
// expectation body is compared BYTE-IDENTICALLY and only line 1 is a header, so
// this note lives here rather than in the artifact.
const (
	censusFixtureCaptureEnv = "FUL1351_CAPTURE"
	censusLegacyRowsFile    = "testdata/ful1351_legacy_rows.txt"
)

// censusRow is the UNION of what the three walks each record. Every field any
// of them keeps is a field here, so no adapter has to re-walk to fill one.
//
// THE PER-RULE COUNTERS ARE ONE MAP, not the six named integers the two landed
// row types carried. boundByRule keyed by the RefRule's own string value is the
// form censusByLanguage already used, and the named counters were only ever
// that map read at six fixed keys — which is what the adapters now do.
type censusRow struct {
	references int
	bound      int
	// collided counts BOUND resolutions that chose one declaration while rivals
	// under the same key survived. It must be ZERO by construction: cardinality
	// above one classifies as ambiguous and emits a group instead of binding.
	collided int
	external int
	// ambiguous counts references classify made an AMBIGUOUS GROUP of. It is the
	// single field the three walks spelled three ways: goRow.ambiguousGroups,
	// ecmaRow.ambiguousGroups and the JSON row's ambiguous_refs are one count of
	// RefAmbiguous over each walk's own admitted population.
	ambiguous int

	dynamicGroups  int
	dynamicUnbound int
	// dynamicMemberAccess and dynamicComputedAccess split the dynamic residue by
	// whether the qualifier was a member access or a computed one. A computed
	// access is beyond static analysis by construction; a member access on a
	// local value is the honest open set the rung exists for.
	dynamicMemberAccess   int
	dynamicComputedAccess int

	// externalQualifier counts terminations at R2X — recorded under two names by
	// the landed rows (ecmaRow.externalQualifier, goRow.r2xTerminations) for one
	// measurement.
	externalQualifier int

	usesTypeTotal int
	usesTypeBound int

	// bindsFiles, bindsEntries and bindsScopesUnknown are THE LIVENESS CONTROL
	// and the R2X INPUT CONTROL, censused off the file's reference site rather
	// than off any edge.
	bindsFiles         int
	bindsEntries       int
	bindsScopesUnknown int
	dotScopeFiles      int

	// boundByRule and ambiguousByRule are attributed by string(got.Rule) over the
	// SAME admitted population, so the pair is a partition of it by outcome. Both
	// are non-nil together so neither can be emitted as a JSON null while the
	// other is an object.
	boundByRule     map[string]int
	ambiguousByRule map[string]int
}

func newCensusRow() *censusRow {
	return &censusRow{boundByRule: map[string]int{}, ambiguousByRule: map[string]int{}}
}

// censusHooks carry the two FAMILY-SPECIFIC attributions that cannot live in
// the shared layer without encoding one family's semantics there.
//
// THEY ARE A SEAM AND NOT A POLICY. Each hook's body lives in the adapter's own
// file: ECMAScript's four-way external cause split keys on a FILE-scope test
// that Go — whose scopes are directories — would have to re-derive a judgment
// for, and Go's ambiguous-group listing is an artifact row no other family
// renders. A hook rather than a second walk because the perf shape is one pass
// over results and one resolveRef per reference, which is what each walk did
// alone.
type censusHooks struct {
	// onAmbiguous fires once per reference classify made an ambiguous group of.
	onAmbiguous func(lang string, e *treesitter.Edge, got refResolution)
	// onExternal fires once per reference that terminated as external.
	onExternal func(lang string, ref *treesitter.RefSite, target string)
}

// censusSpec parameterises one walk.
type censusSpec struct {
	// languages is the set to census. A NIL set means EVERY language
	// encountered, with a row created on first sight — the shape
	// censusByLanguage needs. A non-nil set pre-creates a row per named
	// language, so a language absent from the corpus still renders its zeros
	// rather than vanishing from the artifact.
	languages []string
	// admits decides which edges enter the census, and it is a PARAMETER
	// because the two populations are not the same and neither may be widened
	// into the other. See admitAllReferences and admitBoundPopulation.
	admits func(kgtypes.EdgeType) bool
	hooks  censusHooks
}

// admitAllReferences is the population the two COMMITTED-ARTIFACT walks census:
// every edge that is neither CONTAINS nor IMPORTS. The artifact's reference
// rows count all references, test-origin ones included, and the render header
// says so.
func admitAllReferences(et kgtypes.EdgeType) bool {
	return et != kgtypes.EdgeContains && et != kgtypes.EdgeImports
}

// admitBoundPopulation is the NARROWER population the JSON artifact's rule
// split censuses: CALLS and USES_TYPE only.
//
// IT IS SCOPED TO THE PAIR bound RANGES OVER, and that is the whole filter.
// perLanguageRows counts bound for CALLS and USES_TYPE only
// (corpus_json_test.go:168-175, default: continue), so a walk admitting every
// ref-bearing edge would attribute a THIRD population — TEST_CALLS, emitted
// wherever a language's TestBlocks query is non-empty — to a rung while bound
// never sees it, and the split would then exceed the total it is a split OF.
// TestFUL1347RuleSplitSumsToBound is the test that goes red if this narrowing
// is dropped.
func admitBoundPopulation(et kgtypes.EdgeType) bool {
	return et == kgtypes.EdgeCalls || et == kgtypes.EdgeUsesType
}

// censusWalk is the single per-language measurement pass.
//
// One pass over results and one resolveRef per reference — identical to what
// each of the three walks did alone, with no second index build and no second
// chunking pass. A caller needing more than one family now pays one traversal
// instead of one per family.
func censusWalk(results []*treesitter.Result, ix *declIndex, spec censusSpec) map[string]*censusRow {
	rows := make(map[string]*censusRow, len(spec.languages))
	everyLanguage := spec.languages == nil
	for _, lang := range spec.languages {
		rows[lang] = newCensusRow()
	}

	for _, result := range results {
		lang := string(result.Language)
		row, wanted := rows[lang]
		if !wanted {
			if !everyLanguage {
				continue
			}
			row = newCensusRow()
			rows[lang] = row
		}
		row.censusBinds(result, ix)
		for i := range result.Edges {
			e := &result.Edges[i]
			if !spec.admits(kgtypes.EdgeType(e.Type)) || e.Ref == nil {
				continue
			}
			row.references++
			isUsesType := kgtypes.EdgeType(e.Type) == kgtypes.EdgeUsesType
			if isUsesType {
				row.usesTypeTotal++
			}
			row.attribute(ix, resolveRef(ix, e.Ref, e.ToID), e, isUsesType, lang, spec.hooks)
		}
	}
	return rows
}

// censusBinds records what the binds pass actually filled into one file's
// reference site, and how many of those scopes the declaration index holds.
func (row *censusRow) censusBinds(result *treesitter.Result, ix *declIndex) {
	if result.Ref == nil {
		return
	}
	if len(result.Ref.Binds) > 0 {
		row.bindsFiles++
	}
	for _, b := range result.Ref.Binds {
		row.bindsEntries++
		if !ix.hasScope(b.Scope) {
			row.bindsScopesUnknown++
		}
	}
	if len(result.Ref.DotScopes) > 0 {
		row.dotScopeFiles++
	}
}

// attribute records one resolution against the row, by STATUS and by RULE.
func (row *censusRow) attribute(
	ix *declIndex, got refResolution, e *treesitter.Edge,
	isUsesType bool, lang string, hooks censusHooks,
) {
	switch got.Status {
	case RefBound:
		row.bound++
		if isUsesType {
			row.usesTypeBound++
		}
		if c := got.Candidates[0]; len(ix.lookup(declKey{Scope: c.Scope, Parent: c.Parent, Name: c.Name})) > 1 {
			row.collided++
		}
		row.boundByRule[string(got.Rule)]++
	case RefAmbiguous:
		row.ambiguous++
		row.ambiguousByRule[string(got.Rule)]++
		if hooks.onAmbiguous != nil {
			hooks.onAmbiguous(lang, e, got)
		}
	case RefDynamic:
		if len(got.Candidates) == 0 {
			row.dynamicUnbound++
			return
		}
		row.dynamicGroups++
		if strings.ContainsAny(e.ToID, "[]") {
			row.dynamicComputedAccess++
		} else {
			row.dynamicMemberAccess++
		}
	case RefExternal:
		row.external++
		if got.Rule == RuleExternalQualifier {
			row.externalQualifier++
		}
		if hooks.onExternal != nil {
			hooks.onExternal(lang, e.Ref, e.ToID)
		}
	}
}

// ful1351CharacterizationFixture drives EVERY attributed outcome the two
// committed-artifact renderers can emit, across Go and all three ECMAScript
// languages.
//
// IT IS BUILT AND NOT ASSUMED. The real corpus reports
// ecma_typescript_ambiguous_groups=0, so an ECMAScript ambiguous case would be
// absent from a fixture that merely sampled the corpus's shapes; the Go
// ambiguous pair below is the one the value criterion names for exactly that
// reason. Each file's comment states which outcome it exists to produce.
func ful1351CharacterizationFixture() []fixtureFile {
	return []fixtureFile{
		// Go, qualified-import BOUND: the import binds `lib` to the lib
		// directory's scope, and R1's top-level lookup finds Helper there.
		{path: "lib/lib.go", src: "" +
			"package lib\n\nfunc Helper() int {\n\treturn 1\n}\n"},
		// Go, three outcomes in one file: the qualified-import bind above, an
		// external bare name declared nowhere, and a DYNAMIC group — `v.Process`
		// has an unbound qualifier, so the ladder stops at R3 and enumerates
		// every Process declared in this directory's scope.
		{path: "app/main.go", src: "" +
			"package main\n\nimport \"example.com/fixture/lib\"\n\n" +
			"func Run() int {\n\treturn lib.Helper()\n}\n\n" +
			"func Process() int {\n\treturn 3\n}\n\n" +
			"func Dyn(v Widget) int {\n\treturn v.Process()\n}\n\n" +
			"func Missing() int {\n\treturn undeclaredThing()\n}\n"},
		// Go, AMBIGUOUS: one name declared twice in one directory scope, reached
		// by a bare reference from that scope. R6 returns both and classify makes
		// a CLOSED group of them rather than picking a winner.
		{path: "dup/a.go", src: "" +
			"package dup\n\nfunc Twin() int {\n\treturn 1\n}\n"},
		{path: "dup/b.go", src: "" +
			"package dup\n\nfunc Twin() int {\n\treturn 2\n}\n"},
		{path: "dup/use.go", src: "" +
			"package dup\n\nfunc UseTwin() int {\n\treturn Twin()\n}\n"},
		// TypeScript: the unqualified-import rung (helper, bound through the
		// module arm), a dynamic group (box.localTarget), and an external.
		{path: "web/lib.ts", src: "" +
			"export function helper(): number {\n\treturn 1;\n}\n"},
		{path: "web/app.ts", src: "" +
			"import { helper } from \"./lib\";\n\n" +
			"export function localTarget(): number {\n\treturn 2;\n}\n\n" +
			"export function run(): number {\n\treturn helper();\n}\n\n" +
			"export function dyn(box: Box): number {\n\treturn box.localTarget();\n}\n\n" +
			"export function missing(): number {\n\treturn undeclaredThing();\n}\n"},
		// javascript and tsx keep the other two ECMAScript rows off the
		// all-zero floor, so a consolidation that dropped a language from the
		// walk's set moves this expectation rather than passing unnoticed.
		{path: "web/util.js", src: "" +
			"export function jsHelper() {\n\treturn 1;\n}\n\n" +
			"export function jsRun() {\n\treturn jsHelper();\n}\n"},
		{path: "web/comp.tsx", src: "" +
			"export function tsxHelper(): number {\n\treturn 1;\n}\n\n" +
			"export function TsxRun(): number {\n\treturn tsxHelper();\n}\n"},
	}
}

// ful1351RenderFixtureRows runs the fixture through the SAME two walks and the
// SAME two renderers the committed artifact is produced by, and returns the
// rendered row block.
//
// THE CALL SITES BELOW ARE WHAT MAKES THIS A TWO-CODE-STATE COMPARISON: this
// function is unchanged across the extraction, so the expectation it captured
// against the ORIGINAL goRows and ecmaScriptRows is compared against the
// ADAPTER forms of those same two functions.
func ful1351RenderFixtureRows(t *testing.T) string {
	t.Helper()
	files := ful1351CharacterizationFixture()
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.path)
	}

	// THE PRODUCTION ORDER, mirroring the corpus instrument: DeduplicateChunks,
	// then resolveSlotEdges, then fillBinds, then the index build. Taking node
	// IDs before DeduplicateChunks yields pre-rename IDs, and resolving slots
	// after the chunk sort invalidates every slot index.
	results := chunkFixture(t, files)
	DeduplicateChunks(results)
	resolveSlotEdges(results)
	// ROOT AND FILES ARE BOTH SUPPLIED, and neither is optional. jsmodule's
	// NewResolver REFUSES an empty root outright, so the whole ECMAScript arm
	// returns no binds at all without one; Files is what a relative specifier is
	// resolved against. With either missing the `helper` import binds nothing,
	// ecma_typescript_bound_rule_unqualified_import reads zero, and every other
	// row still looks healthy — which is exactly the vacuity the value criterion
	// on this expectation exists to catch. The root is a bare path rather than a
	// temp dir because the fixture declares no tsconfig, jsconfig or
	// package.json, so nothing is ever read off disk under it.
	fillBinds(&treesitter.RepoContext{
		Root: "/ful1351-fixture", ModulePath: "example.com/fixture", Files: paths,
	}, results)

	ix := newDeclIndex(0)
	for _, r := range results {
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			indexDeclaration(ix, r, chunk, ChunkNodeID(chunk))
		}
	}

	rep := newCorpusReport()
	rep.ecma = ecmaScriptRows(results, ix, paths)
	rep.goRows = goRows(results, ix)

	var b strings.Builder
	rep.renderECMAScript(&b)
	rep.renderGo(&b)
	return b.String()
}

// TestFUL1351CaptureLegacyRows writes the expectation, and is SKIPPED unless
// its env var is set.
//
// IT IS A MEASUREMENT INSTRUMENT, not a gate, and it is deliberately not
// re-runnable for value: a capture taken after the extraction turns the guard
// into an identity that holds however wrong the consolidation is. The
// captured-at header records the commit the capture ran at, and a landed
// criterion requires that commit NOT to hold this file — so a re-capture from
// a tree already carrying the generator fails loudly rather than quietly
// re-baselining.
// THE SHA IS READ FROM GIT AND NEVER SUPPLIED BY THE OPERATOR. A capture that
// took the commit from its own environment could label itself with any sha at
// all, including one that predates the generator while running in a tree that
// carries it — which is precisely the vacuity the provenance criterion exists
// to close. Reading HEAD makes the header a measurement of the tree the capture
// actually ran in.
func TestFUL1351CaptureLegacyRows(t *testing.T) {
	if os.Getenv(censusFixtureCaptureEnv) == "" {
		t.Skipf("set %s=1 to capture the legacy-row expectation", censusFixtureCaptureEnv)
	}
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	require.NoError(t, err, "the captured-at header records HEAD, so git must answer")
	sha := strings.TrimSpace(string(out))
	require.Len(t, sha, 40, "expected a full commit sha, got %q", sha)

	body := "# captured-at: " + sha + "\n" + ful1351RenderFixtureRows(t)
	require.NoError(t, os.WriteFile(censusLegacyRowsFile, []byte(body), 0o600))
	t.Logf("wrote %s\n%s", censusLegacyRowsFile, body)
}

// TestCensusWalkMatchesLegacyRows is THE CHARACTERIZATION GUARD: the
// consolidated walk must produce row-for-row identical output to the three
// originals on the same fixed inputs.
//
// BYTE EQUALITY OF THE RENDERED BLOCK, not a subset and not a tolerance. This
// plan's whole safety argument is that no row semantics change, so a moved
// value is a consolidation defect and never a number to absorb.
func TestCensusWalkMatchesLegacyRows(t *testing.T) {
	want, err := os.ReadFile(censusLegacyRowsFile)
	require.NoError(t, err, "the pre-extraction expectation must exist")

	header, block, found := strings.Cut(string(want), "\n")
	require.True(t, found, "the expectation must carry a captured-at header line")
	require.True(t, strings.HasPrefix(header, "# captured-at: "),
		"the first line records the commit the capture ran at, got %q", header)

	// THE KNOWN POSITIVE. The renderers emit their full fixed row set whatever
	// the values are, so a row COUNT cannot see an all-zero capture and neither
	// can a byte comparison against one. Naming the outcomes here is what makes
	// the equality below account for something.
	// The leading newline makes the line anchor hold for the block's FIRST row
	// too, so the check does not depend on render order.
	anchored := "\n" + block
	for _, key := range []string{
		"go_bound_rule_qualified_import", "go_ambiguous_groups", "go_dynamic_groups", "go_external",
		"ecma_typescript_bound_rule_unqualified_import", "ecma_typescript_dynamic_groups",
		"ecma_typescript_external",
	} {
		require.Contains(t, anchored, "\n"+key+"=",
			"control: %s is absent from the expectation entirely", key)
		require.NotContains(t, anchored, "\n"+key+"=0\n",
			"control: %s is zero in the expectation, so the fixture drives that outcome nowhere "+
				"and the equality below cannot see it move", key)
	}

	require.Equal(t, block, ful1351RenderFixtureRows(t),
		"the consolidated walk must render byte-identically to the pre-extraction walks — "+
			"a moved row is a consolidation defect, never a value to re-baseline")
}
