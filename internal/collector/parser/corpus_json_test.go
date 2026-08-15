// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The PER-LANGUAGE corpus artifact. It EXTENDS the corpus verification's walk
// rather than adding a second probe binary: same discovery, same chunking, same
// production resolution order, aggregated by language instead of in total.
//
// THE SCHEMA IS DECLARED HERE AND NOWHERE ELSE. Four top-level keys, and the
// four resolution columns are counted SEPARATELY and never summed — a reader
// who adds dynamic to ambiguous has reported a language property (runtime
// dispatch) as an index gap.
const (
	corpusJSONArtifact = "/tmp/f1338_corpus.json"

	// corpusJSONBaselineEnv points at an artifact produced by THIS SAME test at
	// an earlier code state, over the SAME corpus root. It supplies
	// baseline_calls_edges, which no single run can derive: "this language did
	// not lose edges" is a comparison between two CODE states, and comparing a
	// run against itself would be vacuous.
	//
	// A missing baseline is a HARD FAILURE rather than a default. Defaulting
	// baseline to the run's own count would make the did-not-fall assertion
	// pass by construction on every language forever.
	corpusJSONBaselineEnv = "FUL1338_BASELINE"

	// baselineSelfValue marks a run as the BASELINE itself, which by definition
	// has no predecessor to read.
	baselineSelfValue = "none"
)

type corpusLangRow struct {
	Language               string `json:"language"`
	CallsEdges             int    `json:"calls_edges"`
	BaselineCallsEdges     int    `json:"baseline_calls_edges"`
	UsesTypeEdges          int    `json:"usestype_edges"`
	Bound                  int    `json:"bound"`
	AmbiguousGroups        int    `json:"ambiguous_groups"`
	DynamicGroups          int    `json:"dynamic_groups"`
	DynamicUnbound         int    `json:"dynamic_unbound"`
	CollidedKeyResolutions int    `json:"collided_key_resolutions"`
	// CollidedAliasNarrowed is the SUBSET of CollidedKeyResolutions explained by
	// a DELIBERATE alias narrowing. The total's definition above is unchanged;
	// this splits it so a sanctioned narrowing is distinguishable from the
	// silent-overwrite event the total exists to catch.
	//
	// THE TOTAL WAS DOCUMENTED AS ZERO BY CONSTRUCTION AND IS NOT. It measures
	// 274 on the pinned rust corpus and 398 on the pinned scala one, and the
	// mechanism is closed and CORRECT: classify binds only on a single
	// candidate, and every candidate set is narrow(ix.lookup(key)), so rivals
	// above one is reachable ONLY when filterBySuffixedName filtered — meaning
	// the reference carried a '#' collision suffix. Rust's ordinary
	// `struct Thing` beside `impl Thing` provokes exactly that, and Scala's
	// companion `object X` beside `class X` is the same shape.
	CollidedAliasNarrowed int `json:"collided_alias_narrowed"`
}

type corpusJSON struct {
	Languages []corpusLangRow `json:"languages"`
	// LanguagesAbsentFromCorpus is the LIST, not a count, and it is present
	// even when empty: a per-language assertion has to tell "this language
	// regressed" from "this language is not in this corpus at all".
	LanguagesAbsentFromCorpus []string `json:"languages_absent_from_corpus"`
	// Commit is the HEAD of the KNOWLEDGE repo — the code under measurement —
	// never the corpus tree's.
	Commit string `json:"commit"`
	// CorpusRoot is the absolute path of the tree that was MEASURED. The commit
	// above cannot serve that purpose: it names the other repository.
	CorpusRoot string `json:"corpus_root"`
}

// TestFUL1338CorpusPerLanguage writes the per-language artifact.
func TestFUL1338CorpusPerLanguage(t *testing.T) {
	root := os.Getenv(corpusRootEnv)
	if root == "" {
		t.Skipf("set %s to a repository path to run the per-language corpus measurement", corpusRootEnv)
	}
	root = filepath.Clean(root)
	require.True(t, filepath.IsAbs(root), "%s must be an absolute path, got %q", corpusRootEnv, root)

	ctx := context.Background()
	files, err := DiscoverFiles(ctx, root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "control: discovery found no files under %s", root)

	results, err := ChunkFilesParallel(ctx, root, files)
	require.NoError(t, err)
	require.NotEmpty(t, results, "control: chunking produced no results")

	DeduplicateChunks(results)
	resolveSlotEdges(results)
	modulePath, _ := ReadModulePath(root)
	rc := treesitter.RepoContext{Root: root, ModulePath: modulePath, Files: files}
	fillBinds(&rc, results)

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

	rows := perLanguageRows(t, results, ix, edges, ownerLang)
	out := corpusJSON{
		Languages:                 rows,
		LanguagesAbsentFromCorpus: absentLanguages(rows),
		Commit:                    knowledgeHeadCommit(t),
		CorpusRoot:                root,
	}
	applyBaseline(t, &out)

	body, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(corpusJSONArtifact, append(body, '\n'), 0o600))
	t.Logf("wrote %s (%d languages present, %d absent, commit %s)",
		corpusJSONArtifact, len(rows), len(out.LanguagesAbsentFromCorpus), out.Commit)
}

// perLanguageRows aggregates the emitted edges and re-resolves every reference
// so the four resolution columns can be attributed to the language that emitted
// them rather than reported only in total.
func perLanguageRows(
	t *testing.T, results []*treesitter.Result, ix *declIndex,
	edges []*knowledgev1.Edge, ownerLang map[string]string,
) []corpusLangRow {
	t.Helper()

	calls := map[string]int{}
	usesType := map[string]int{}
	bound := map[string]int{}
	ambiguous := map[string]map[string]bool{}
	dynamic := map[string]map[string]bool{}
	boundTargets := map[string]map[string]bool{}

	for _, e := range edges {
		lang := ownerLang[e.FromId]
		if lang == "" {
			continue
		}
		switch kgtypes.EdgeType(e.Type) {
		case kgtypes.EdgeCalls:
			calls[lang]++
		case kgtypes.EdgeUsesType:
			usesType[lang]++
		default:
			continue
		}
		switch e.Method {
		case kgtypes.EdgeMethodAmbiguousName:
			addKey(ambiguous, lang, e.Evidence)
		case kgtypes.EdgeMethodDynamic:
			addKey(dynamic, lang, e.Evidence)
		default:
			bound[lang]++
			addKey(boundTargets, lang, e.ToId)
		}
	}

	// dynamic_unbound is a property of the RESOLUTION rather than of an emitted
	// edge: a dynamic reference whose scope declares nothing by that name emits
	// no edge and no group at all, so it is invisible in the edge list.
	unbound := map[string]int{}
	for _, r := range results {
		lang := string(r.Language)
		for i := range r.Edges {
			e := &r.Edges[i]
			if e.Ref == nil {
				continue
			}
			got := resolveRef(ix, e.Ref, e.ToID)
			if got.Status == RefDynamic && len(got.Candidates) == 0 {
				unbound[lang]++
			}
		}
	}

	langs := map[string]bool{}
	for _, m := range []map[string]int{calls, usesType, bound, unbound} {
		for l := range m {
			langs[l] = true
		}
	}
	var out []corpusLangRow
	for lang := range langs {
		collided, aliasNarrowed := collidedForLanguage(ix, boundTargets[lang])
		out = append(out, corpusLangRow{
			Language:               lang,
			CallsEdges:             calls[lang],
			UsesTypeEdges:          usesType[lang],
			Bound:                  bound[lang],
			AmbiguousGroups:        len(ambiguous[lang]),
			DynamicGroups:          len(dynamic[lang]),
			DynamicUnbound:         unbound[lang],
			CollidedKeyResolutions: collided,
			CollidedAliasNarrowed:  aliasNarrowed,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Language < out[j].Language })
	return out
}

func addKey(m map[string]map[string]bool, lang, key string) {
	if key == "" {
		return
	}
	if m[lang] == nil {
		m[lang] = map[string]bool{}
	}
	m[lang][key] = true
}

// collidedForLanguage counts the bound targets whose own declaration key has
// RIVALS — a binding that landed on one of several same-keyed declarations —
// and, as a SUBSET of that same population, the ones a deliberate alias
// narrowing explains.
//
// THE TOTAL'S DEFINITION IS UNCHANGED. The subset is read off data already in
// hand: the node ID retains the '#' collision suffix while declRec.Name is
// base-named (indexDeclaration applies baseDeclName), so the suffix on the
// bound target's ID is exactly the record that filterBySuffixedName is what
// narrowed the set.
func collidedForLanguage(ix *declIndex, targets map[string]bool) (total, aliasNarrowed int) {
	for id := range targets {
		rec, ok := ix.byID[id]
		if !ok {
			continue
		}
		if len(ix.lookup(declKey{Scope: rec.Scope, Parent: rec.Parent, Name: rec.Name})) > 1 {
			total++
			if strings.Contains(id, "#") {
				aliasNarrowed++
			}
		}
	}
	return total, aliasNarrowed
}

// absentLanguages is every REGISTERED language for which this collect found no
// participating file at all.
func absentLanguages(rows []corpusLangRow) []string {
	present := map[string]bool{}
	for _, r := range rows {
		present[r.Language] = true
	}
	absent := []string{}
	for _, lang := range treesitter.RegisteredLanguages() {
		if !present[string(lang)] {
			absent = append(absent, string(lang))
		}
	}
	sort.Strings(absent)
	return absent
}

// knowledgeHeadCommit is the HEAD of the repository holding THIS test — the
// code under measurement — which is what a reader needs to know a number was
// produced by.
//
// It DELEGATES to headCommitAt (corpus_multi_lang_test.go), which reads any
// repository's HEAD: the multi-root walk needs the measured corpora's own HEADs
// as well as this one, and the read is identical in both cases.
func knowledgeHeadCommit(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return headCommitAt(t, wd)
}

// applyBaseline fills baseline_calls_edges from a PRIOR run of this same test
// over the SAME corpus root at an earlier code state.
//
// IT STOPS RATHER THAN DEFAULTING when the predecessor's artifact is missing:
// this is a cross-phase hand-off, and a consumer that quietly substituted its
// own numbers would turn the did-not-fall comparison into an identity.
func applyBaseline(t *testing.T, out *corpusJSON) {
	t.Helper()
	path := os.Getenv(corpusJSONBaselineEnv)
	require.NotEmpty(t, path,
		"set %s to the baseline artifact produced by this test at the pre-change code state, "+
			"or to %q when THIS run is that baseline",
		corpusJSONBaselineEnv, baselineSelfValue)
	if path == baselineSelfValue {
		// THE BASELINE RUN ITSELF has no predecessor, and saying so explicitly
		// is what keeps the unset case a hard failure rather than a shrug.
		return
	}

	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied measurement input
	require.NoError(t, err, "the baseline artifact must exist: %s", path)
	var base corpusJSON
	require.NoError(t, json.Unmarshal(raw, &base))
	require.Equal(t, out.CorpusRoot, base.CorpusRoot,
		"a baseline measured over a DIFFERENT corpus root compares two trees, not two code states")

	prior := map[string]int{}
	for _, r := range base.Languages {
		prior[r.Language] = r.CallsEdges
	}
	for i := range out.Languages {
		out.Languages[i].BaselineCallsEdges = prior[out.Languages[i].Language]
	}
}
