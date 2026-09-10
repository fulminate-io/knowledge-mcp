// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collect_docs_contract_test.go — R5 and R7's CONTENT assertion over the two
// user-facing surfaces that describe how a custom collect is parameterised: the
// live `collect` tool schema an LLM reads, and the two guide pages a collector
// author reads.
//
// WHY A CONTENT ASSERTION AND NOT THE DRIFT GATE. The docs-drift gate compares
// the generated page against the checked-in page. Both are derived from the same
// schema string, so when that string is stale they agree and the gate is SILENT —
// which is exactly how the retired exec contract survived in the collect tool's
// params description and in collect.md after the exec runner was deleted. A gate
// that compares two copies of one source cannot notice that the source is wrong.

// retiredExecVocabulary is the vocabulary of the RETIRED exec contract. Each term
// named an artifact this change deleted: a collector binary, a param transport, a
// hand-written param schema in the record, and the envelope graph identity a
// provider result cannot carry.
var retiredExecVocabulary = []string{
	"binary_path",
	"param_transport",
	"param_schema",
	"collector binary",
	"before exec",
}

// `graph_name` WAS ON THIS LIST AND IS NOT ANY MORE, and the removal is a
// narrowing rather than a retreat. It was banned because the retired contract
// taught a collector to name its own destination graph — an envelope field, with
// an id-to-graph_name default — and a collector author following that page today
// would write a provider this client refuses.
//
// THE TOKEN STOPPED DISCRIMINATING when the collect INPUT gained the declared
// foreign-graph context, whose per-graph key is `graph_name`: the spelling comes
// from the wire that fills it, it names a graph the CLIENT read FROM, and it is
// the opposite of a graph a provider writes INTO. A ban on the bare token now
// fires on the correct documentation of a current feature, which is the shape a
// gate is worst at — an author's only way past it is to document the feature
// wrongly.
//
// WHAT REPLACES IT IS STRONGER THAN THE BAN WAS: an assertion that the page
// still carries the positive instruction, that a result declares no graph
// identity. A ban on a word can be satisfied by silence; a required sentence
// cannot, and anyone reintroducing an envelope-chosen destination would have to
// delete or contradict it. See TestCollectDocs_StillDenyTheResultAGraphIdentity.

// docsLinkDir is the SYMLINK this package owns, pointing at the directory the
// root module owns. Reading a guide page THROUGH it is what puts the page in
// this module's test-cache key.
//
// WHY A SYMLINK AND NOT THE RELATIVE PATH IT REPLACES. `go test` records the
// files a run opened and re-runs when one changes, but computeTestInputsID skips
// any opened name that does not resolve inside the tested package's module root
// (its own guard is `search.InDir(name, a.Package.Root) == ""` -> break). The
// guide pages live in the ROOT module and this test lives in cmd/knowledge, so
// the previous "../../../../docs/..." open was dropped from the key entirely and
// a docs-only edit was served a CACHED PASS — precisely the change class this
// guard exists to catch. hashOpen stats the name with os.Stat, which FOLLOWS the
// link, so a name under this module that resolves elsewhere is tracked at the
// target's size and mtime.
//
// The two alternatives do not work and are not worth re-deriving: a committed
// copy needs a divergence assertion whose own comparison read is outside the
// module and therefore untracked; a CI cache-dependency-path entry fixes CI while
// every local run keeps serving the stored result.
const docsLinkDir = "testdata/docs"

// docsRoot resolves this module's symlink to an ABSOLUTE path. Absolute, not
// relative: the fence depends on the opened name being recognized as inside this
// module's root, and an absolute name is what the proven form used. A relative
// one is an unmeasured variation of the mechanism this whole file exists for.
func docsRoot(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(docsLinkDir)
	require.NoError(t, err)
	return p
}

// TestDocsLinkKeepsTheGuidePagesInThisModulesTestCache asserts the fence is
// really there. THE COST OF THE SYMLINK, stated rather than hidden: a checkout
// without symlink support materializes the entry as a one-line text stub, and
// the guard above would then read that stub, find no retired vocabulary in it,
// and pass while observing nothing. This test makes such a checkout fail with a
// sentence naming the cause.
func TestDocsLinkKeepsTheGuidePagesInThisModulesTestCache(t *testing.T) {
	info, err := os.Lstat(docsLinkDir)
	require.NoError(t, err, "%s must exist — it is what puts the guide pages in this module's test-cache key", docsLinkDir)
	require.NotZero(t, info.Mode()&os.ModeSymlink,
		"%s is not a symlink (a checkout without symlink support materializes it as a text stub); "+
			"the docs content guard would then read the stub and pass while observing nothing", docsLinkDir)

	// WHAT THIS ASSERTS AND WHAT IT DELIBERATELY DOES NOT. It asserts the link
	// RESOLVES to a real directory serving the real guide pages. It does NOT
	// assert which path that directory sits at, because the link's depth is
	// layout-dependent by nature: this repository keeps the pages five levels up
	// from here, and the OSS mirror — where cmd/knowledge/internal becomes
	// internal/ and docs/guides stays at the root — keeps them three up, so
	// scripts/sync-to-oss.sh authors the mirror's own link. A hardcoded expected
	// path would make this sentinel fail in the mirror for a reason that is not
	// the defect it exists to catch, which is exactly what it did before.
	linkAbs, err := filepath.Abs(docsLinkDir)
	require.NoError(t, err)
	target, err := filepath.EvalSymlinks(linkAbs)
	require.NoError(t, err, "%s must resolve; a dangling link means the layout it was authored for is not this one", docsLinkDir)
	targetInfo, err := os.Stat(target)
	require.NoError(t, err)
	require.True(t, targetInfo.IsDir(), "%s must resolve to the guide DIRECTORY, not a file", docsLinkDir)

	// KNOWN POSITIVE: the pages reached through the link are the real documents,
	// not a stub or an empty directory. A text-stub checkout fails at the Lstat
	// above; an empty or wrong directory fails here.
	for _, page := range []string{"tools/collect.md", "tools/custom_collector.md"} {
		body, err := os.ReadFile(filepath.Join(docsLinkDir, page))
		require.NoError(t, err, "the link must serve %s", page)
		require.NotEmpty(t, body)
		require.Contains(t, string(body), "custom_collector",
			"%s read through the link must be the real guide page", page)
	}

	// THE BYTE-IDENTITY CONTROL, restored in a layout-independent form. Without
	// it a link pointed at a STALE COPY of the guide directory satisfies every
	// assertion above — it is a symlink, it resolves, the target is a directory,
	// the pages contain the substring — while defeating the fence entirely,
	// because a copy's bytes do not change when the owned document does.
	//
	// The owned directory is DISCOVERED by walking up from this package until a
	// docs/guides/tools appears, rather than named by a fixed number of "..".
	// That is what makes the control survive both layouts: five levels up in this
	// repository, three in the OSS mirror where cmd/knowledge/internal becomes
	// internal/. Hardcoding the depth is the mistake that made the previous
	// version of this test fail in the mirror for a reason that was not the
	// defect it exists to catch.
	owned := discoverOwnedGuideDir(t)
	ownedResolved, err := filepath.EvalSymlinks(owned)
	require.NoError(t, err)
	require.Equal(t, ownedResolved, target,
		"%s must resolve to the guide directory this repository owns (%s), not to a copy of it — "+
			"a copy would satisfy every other assertion here while its bytes stopped tracking the document", docsLinkDir, owned)

	for _, page := range []string{"tools/collect.md", "tools/custom_collector.md"} {
		viaLink, err := os.ReadFile(filepath.Join(docsLinkDir, page))
		require.NoError(t, err)
		direct, err := os.ReadFile(filepath.Join(owned, page))
		require.NoError(t, err)
		require.Equal(t, direct, viaLink, "the link must serve the OWNED %s byte for byte", page)
	}
}

// discoverOwnedGuideDir walks up from this package's directory to the first
// ancestor holding docs/guides, and returns that directory. It is the
// layout-independent way to name the document this module fences: the guide
// pages sit at the repository root in both layouts, but at different depths.
//
// It deliberately starts ABOVE the package directory so the link's own target
// cannot be mistaken for the owned directory, and it fails loud rather than
// returning a zero value, because a silent miss here would make the identity
// assertion below compare a path against itself.
func discoverOwnedGuideDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	require.NoError(t, err)
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no ancestor of %s holds docs/guides; the guide pages this module fences could not be located", dir)
		}
		dir = parent
		candidate := filepath.Join(dir, "docs", "guides")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
	}
}

// customCollectorMentions are the spellings that make a guide page part of this
// census. A page that talks about custom collectors is a page a collector author
// reads, whatever directory it sits in.
var customCollectorMentions = []string{"custom collector", "custom_collector"}

// guidePagesMentioningCustomCollectors is the CENSUS. It walks the whole fenced
// guide directory and returns every page that mentions custom collectors, keyed
// by its path relative to the link. Every page is read THROUGH the link, so
// every page in the returned set is in this module's test-cache key.
//
// WHY A CENSUS AND NOT A LIST OF PAGES. The list this replaced named two pages,
// and the page that shipped a retired instruction to the public for a full
// review round was a third one that mentioned custom collectors from outside the
// tools/ directory. A named list can only be as complete as the last author's
// memory of which pages exist; deriving the set from the pages' own text means a
// page that starts talking about custom collectors joins the guard by doing so.
func guidePagesMentioningCustomCollectors(t *testing.T) map[string]string {
	t.Helper()
	pages := map[string]string{}
	walkGuidePages(t, docsRoot(t), "", pages)
	return pages
}

// walkGuidePages recurses through the fenced guide directory with os.ReadDir
// rather than filepath.WalkDir, and the difference is the whole fence.
// WalkDir LSTATS its root: handed a symlink it reports one non-directory entry
// and stops, so the census silently selected nothing. os.Open — which ReadDir
// uses — FOLLOWS the link, and every name this builds stays under
// testdata/docs/..., which is what keeps the pages in this module's cache key.
// Resolving the link up front would read the same bytes by a name outside the
// module, and the fence would be gone with every assertion still passing.
func walkGuidePages(t *testing.T, dir, prefix string, pages map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "the guide directory must be readable through %s", docsLinkDir)
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)
		rel := name
		if prefix != "" {
			rel = prefix + "/" + name
		}
		if entry.IsDir() {
			walkGuidePages(t, path, rel, pages)
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		body, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		text := string(body)
		for _, mention := range customCollectorMentions {
			if strings.Contains(text, mention) {
				pages[rel] = text
				break
			}
		}
	}
}

// TestCollectDocs_CarryNoRetiredExecVocabulary is the content assertion over
// every guide page that mentions custom collectors.
func TestCollectDocs_CarryNoRetiredExecVocabulary(t *testing.T) {
	pages := guidePagesMentioningCustomCollectors(t)

	// KNOWN POSITIVES, three of them, because a census whose selection returned
	// nothing would pass every absence assertion below while observing nothing.
	require.NotEmpty(t, pages, "the census selected no guide page at all; the walk or the link is wrong, not the docs")
	for _, known := range []string{"tools/collect.md", "tools/custom_collector.md"} {
		require.Contains(t, pages, known, "the census must reach %s, the page the collector contract is documented on", known)
	}
	// AND IT MUST REACH OUTSIDE tools/. This is the axis, not a page: the guard
	// was blind to a page one directory up, and a link or a walk narrowed back to
	// tools/ would satisfy both assertions above while reintroducing exactly
	// that. web-collection.md is the page that was missed, named here so a
	// failure says which class of page stopped being covered.
	outside := 0
	for page := range pages {
		if !strings.HasPrefix(page, "tools/") {
			outside++
		}
	}
	require.Positive(t, outside,
		"the census reached no guide page outside tools/; docs/guides/web-collection.md is one such page, "+
			"and a census that cannot see it is the blind spot this widening exists to close")

	names := make([]string, 0, len(pages))
	for page := range pages {
		names = append(names, page)
	}
	sort.Strings(names)
	t.Logf("census: %d guide page(s) mention custom collectors: %s", len(names), strings.Join(names, " "))

	for _, page := range names {
		text := pages[page]
		t.Run(page, func(t *testing.T) {
			for _, term := range retiredExecVocabulary {
				// The page body is NOT interpolated into the message: a failure
				// naming the term and the page is the finding, and pasting a
				// whole guide page into a test log buries it.
				if strings.Contains(text, term) {
					t.Errorf("%s still describes the retired exec contract: it contains %q, and a collector author following it would write a provider this client refuses", page, term)
				}
			}
		})
	}
}

// TestCollectToolSchema_ParamsDescribesTheMCPCall pins the LIVE schema string —
// the text a calling LLM reads to decide how to call collect, and the source the
// generated params table is rendered from, so asserting here covers both.
func TestCollectToolSchema_ParamsDescribesTheMCPCall(t *testing.T) {
	params, ok := CollectToolDef().InputSchema.Properties["params"]
	require.True(t, ok, "the collect schema must declare params")
	desc := params.Description
	require.NotEmpty(t, desc)

	for _, term := range retiredExecVocabulary {
		assert.NotContains(t, desc, term,
			"the collect tool's params description still names %q from the retired exec contract", term)
	}
	// The known-positive half: it must say what params ARE now, not merely omit
	// what they were. An empty or gutted description would pass the loop above.
	assert.Contains(t, strings.ToLower(desc), "provider",
		"the params description must say the object is validated against the schema the provider advertised")
	assert.Contains(t, desc, "custom_collector",
		"the params description must still say which collect types read it")
}

// TestCollectDocs_StillDenyTheResultAGraphIdentity is the positive form of the
// retired `graph_name` ban, and it asserts the instruction rather than the
// absence of a word.
//
// THE RETIRED CONTRACT'S WORST SURVIVING SENTENCE would be one telling a
// collector author to name the graph their result lands in. The page says the
// opposite in so many words, and it has to keep saying it: a page that simply
// stopped mentioning the subject would satisfy any ban on the old spelling while
// leaving an author to guess, and guessing wrong here produces a provider whose
// every collect is refused.
func TestCollectDocs_StillDenyTheResultAGraphIdentity(t *testing.T) {
	pages := guidePagesMentioningCustomCollectors(t)
	page, ok := pages["tools/custom_collector.md"]
	require.True(t, ok, "the census must reach the contract page")

	assert.Contains(t, page, "Your result carries no graph identity",
		"the contract page must keep saying that a provider does not choose its destination graph")

	// AND THE RETIRED DEFAULT MUST STAY GONE. This is the half of the old ban
	// that still discriminates: the id-to-destination default was the mechanism,
	// and its name cannot collide with the input block's per-graph key.
	assert.NotContains(t, page, "default graph_name",
		"the retired id-to-destination default must not return")
	assert.NotContains(t, page, "graph_name default",
		"nor under its other spelling")
}

// TestCollectDocs_TheGuideStatesCollectorTrafficIsUncapped is the documentation
// half of the removed size caps, and it is a REQUIRED-PRESENCE assertion rather
// than a ban on the old number.
//
// WHY THE SENTENCE HAS TO BE THERE RATHER THAN THE OLD ONE MERELY GONE. The
// guide told collector authors their result was capped at 64 MiB, and some of
// them built to it — chunking a walk, or refusing their own oversize source. A
// page that simply deleted the bullet leaves them with a limit they still
// believe in and no way to learn otherwise. This asserts the replacement says
// the opposite in so many words, in both directions.
func TestCollectDocs_TheGuideStatesCollectorTrafficIsUncapped(t *testing.T) {
	pages := guidePagesMentioningCustomCollectors(t)
	page, ok := pages["tools/custom_collector.md"]
	require.True(t, ok, "the census must reach the contract page")

	assert.Contains(t, page, "Collector traffic is uncapped, in both directions",
		"the guide must state plainly that neither direction carries a size limit")

	// AND THE RETIRED FIGURE MUST BE GONE, which is the other half: a page that
	// carried both statements would contradict itself and an author would follow
	// whichever they read first.
	assert.NotContains(t, page, "capped at 64 MiB",
		"the retired result cap must not survive beside its own retraction")
	assert.NotContains(t, page, "over-cap payload",
		"nor in the failure list")
}

// contextDeclarationKeys are every key a context declaration can carry. Each one
// can produce a refusal, so each one has to be documented — an operator reading
// a refusal that names a key the guide never mentions has nowhere to go.
var contextDeclarationKeys = []string{
	"node_types", "node_fields", "metadata_keys", "edge_fields", "path_basenames",
}

// TestCollectDocs_TheGuideDocumentsEveryContextDeclarationKeyAndWhenItIsRefused
// is the drift guard between the validator and the page that describes it.
//
// WHY IT EXISTS. The first version of this feature shipped a guide promising
// that an unsupplyable declaration errors "at registration", against code that
// checked it only at collect time. The prose was ahead of the code by one
// review round, and nothing in the suite could tell. This does not adjudicate
// the promise — a test cannot read English — but it does hold the page to
// naming every key that can produce a refusal and to naming registration as one
// of the moments a refusal happens, so silently dropping either is a red.
func TestCollectDocs_TheGuideDocumentsEveryContextDeclarationKeyAndWhenItIsRefused(t *testing.T) {
	pages := guidePagesMentioningCustomCollectors(t)
	page, ok := pages["tools/custom_collector.md"]
	require.True(t, ok, "the census must reach the contract page")

	for _, key := range contextDeclarationKeys {
		assert.Contains(t, page, key,
			"every declaration key can produce a refusal naming it, so every one has to be documented")
	}

	assert.Contains(t, page, "`knowledge collector add`",
		"the page must name the command that refuses an unsupplyable declaration before the entry is written")

	// THE CONTROL for the assertions above: a key that is NOT part of the
	// declaration is absent, so the checks are reading the declaration's own
	// vocabulary rather than passing on a page that mentions everything.
	assert.NotContains(t, page, "node_predicates",
		"control: a key the declaration does not carry must not appear")
}
