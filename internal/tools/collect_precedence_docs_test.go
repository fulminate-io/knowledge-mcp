// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collect_precedence_docs_test.go — the CONTENT assertions over the guide and
// the live collect schema, each a REQUIRED POSITIVE over a named surface rather
// than an absence over the census.
//
// WHY SCOPED AND NOT CENSUS-WIDE. The sibling census
// (TestCollectDocs_CarryNoRetiredExecVocabulary) is an ABSENCE assertion, which
// is correct to run over every page that mentions custom collectors. A required
// positive is the opposite: demanding a precedence sentence of every such page
// would make an unrelated page fail for not repeating it. These rows name their
// surfaces.

// collectPrecedenceStatement is the FIXED SUBSTRING all three R5 surfaces must
// carry. Naming it once here is what makes the three assertions one requirement
// rather than three paraphrases that can drift apart.
const collectPrecedenceStatement = "a registered custom_collector family wins over a built-in collector of the same name"

// declaredOnlyEnvStatement is the FIXED SUBSTRING the guide's environment
// section must carry. The spawn passes exactly the names a registration
// declares, and the guide is the only place a collector author learns that a
// proxy or a certificate variable is theirs to declare rather than something
// the daemon supplies — a baseline the daemon passed on its own was built on
// this branch and withdrawn, so the guide saying so is what stops an author
// writing a collector that assumes one.
//
// IT DELIBERATELY DOES NOT SPAN A LINE BREAK. The sentence is wrapped in the
// guide, and a matcher carrying that wrap would fail on a reflow that changed
// nothing an author reads.
const declaredOnlyEnvStatement = "daemon adds no environment of its own"

// gatedTailStatements are the FIXED SUBSTRINGS the guide's post-collect-tail
// paragraph must carry: what the client does NOT do for a registered family, and
// what the collector's own result can and cannot carry instead. They are the
// operator-visible half of the tail gate — the code half is
// TestRunPostCollectLinker_RegisteredCustomCollectRunsNoLinker — and without the
// paragraph an author has no way to learn that proxy nodes and edges to them are
// theirs to emit, and that a cross-graph edge is theirs to NAME while nothing
// derives one for them, since the guide otherwise promises the same tail a code
// collect gets.
//
// TWO ROWS WERE RETIRED HERE AND WHY THEY OUTLIVED THEIR TRUTH. This list used to
// pin "endpoint lives in another graph is not expressible today" and "That gap
// closes with a later contract revision". Both were TRUE when written and both
// became false when the contract gained its graph-family fields — and because a
// gate demands its sentence be PRESENT, the gate written at the moment of a
// limitation kept the stale sentence in place and would have redded on anyone's
// correction. A gate on a statement about a limitation is a gate with an expiry
// date, and this one is now aimed at the capability instead.
//
// EACH ROW DELIBERATELY FITS ON ONE LINE of the wrapped guide, for the reason
// declaredOnlyEnvStatement records: a matcher spanning the wrap fails on a reflow
// that changed nothing an author reads.
var gatedTailStatements = []string{
	"the client runs no post-collect enrichment",
	"the cross-graph linker does not run",
	"no built-in post-populate hook runs, whatever your family is named",
	"A proxy node standing for a foreign",
	"endpoint lives in another graph is expressible too: name the family in",
	"What the tail does not do is DERIVE such an edge for you",
	"The pipeline wake still fires, so summarization and embedding are unaffected",
}

// retractedTailPromises are the sentences a revert or a careless rewrite would
// bring back, each of which was FALSE when the guide carried it. They are
// asserted absent for the same reason the rows above are asserted present: the
// paragraph's job is to leave an author with a true picture, and a true sentence
// sitting beside a false one does not do that.
//
// THE SECOND ROW WAS ADDED WHEN THE PARAGRAPH WAS FIRST CORRECTED. The first
// version of this paragraph told an author to emit cross-graph links and proxy
// edges in the result their tool returns, at a time when the contract could
// express neither: the result is stamped with one family and the edge carried no
// graph selector, so the instructed edge landed dangling inside the collector's
// own graph. Instructing it was a promise with no carrier, which is a worse
// failure than the silence it replaced. The contract can express it now, through
// the two family fields — but it is still not DERIVED, so the row stays: the
// sentence it forbids promises the tail does the work.
//
// THE THIRD AND FOURTH ROWS ARE THE RETIRED LIMITATION ITSELF, asserted absent
// rather than only replaced. A positive row on the corrected sentence passes on a
// page that carries the correction in one paragraph and the retired claim in the
// next, which is exactly the shape a partial edit leaves behind and exactly how
// this sentence survived the contract revision that falsified it.
var retractedTailPromises = []string{
	"post-collect linker, enrichment and pipeline wake",
	"emit the cross-graph links and the proxy edges you want",
	"endpoint lives in another graph is not expressible today",
	"That gap closes with a later contract revision",

	// THE FIFTH AND SIXTH ROWS NAME A MECHANISM THAT NO LONGER EXISTS, and they
	// are the same failure as the two above it seen from the other retirement.
	// The paragraph used to say "the log materializer is not reachable — and that
	// holds even when your family is named after a built-in collector, such as
	// `gcp`, whose collect would otherwise trigger them". Both halves were true
	// of a tree with built-in cloud and log collectors in it; neither is true of
	// this one. The materializer went with those collectors, and no built-in
	// family is named gcp any more, so the sentence tells an author their family
	// is being held back from something that is not there — and worse, that a
	// built-in collector by that name would have triggered it. A vacuous gate
	// reads as a live one, so it is asserted ABSENT rather than merely dropped
	// from the required list: dropping it would let a revert bring it back in
	// silence.
	//
	// FOUR OF THE SIX ROWS ARE NOW RETIRED CLAIMS RATHER THAN FALSE PROMISES, and
	// the two retirements arrived from opposite directions: the contract GAINED
	// the ability this paragraph said it lacked, and the product LOST the
	// mechanisms it said were holding a family back. A gate that demands a
	// sentence about a limitation has an expiry date either way, which is the one
	// lesson both halves of this list teach.
	"the log materializer is not reachable",
	"such as `gcp`, whose collect would",
}

// TestCustomCollectorGuide_StatesTheGatedPostCollectTail is the required positive
// for that paragraph. The guide's limits section previously promised a custom
// collect "the same post-collect linker, enrichment and pipeline wake" a code
// collect gets, which the tail gate made false; this is what stops the correction
// being lost again.
func TestCustomCollectorGuide_StatesTheGatedPostCollectTail(t *testing.T) {
	pages := guidePagesMentioningCustomCollectors(t)
	page, ok := pages["tools/custom_collector.md"]
	require.True(t, ok, "the census must reach the custom-collector guide")

	for _, want := range gatedTailStatements {
		assert.Contains(t, page, want,
			"the guide must state what the post-collect tail does not do for a registered family, and what the collector's own result can and cannot carry instead")
	}
	for _, gone := range retractedTailPromises {
		assert.NotContains(t, page, gone,
			"a promise the contract does not keep must not reappear in the guide")
	}
}

// TestCustomCollectorGuide_StatesTheDeclaredOnlyEnvironment is the required
// positive for that bullet. Without it the guide's environment section could
// lose the sentence and no test would notice, which is exactly the hole the
// withdrawn baseline's own guide paragraph left when it was removed.
func TestCustomCollectorGuide_StatesTheDeclaredOnlyEnvironment(t *testing.T) {
	pages := guidePagesMentioningCustomCollectors(t)
	page, ok := pages["tools/custom_collector.md"]
	require.True(t, ok, "the census must reach the custom-collector guide")

	assert.Contains(t, page, declaredOnlyEnvStatement,
		"the guide must state that the daemon adds no environment of its own, or an author cannot know a proxy or certificate variable is theirs to declare")
	// The two names an author is most likely to assume arrive for free are named
	// in that bullet, so a rewrite that keeps the rule but drops the examples
	// still fails.
	assert.Contains(t, page, "`HTTPS_PROXY`")
	assert.Contains(t, page, "`SSL_CERT_FILE`")
}

// TestCollectPrecedence_StatedOnAllThreeSurfaces is R5. The precedence a
// registered family now has over a compiled-in collector of the same name is a
// USER-VISIBLE CONTRACT: an operator registering a family under "gcp" gets their
// provider, not the internal collector, and the only way to know that in advance
// is to be told.
//
// THE THREE SURFACES, one matcher: the custom-collector guide, the collect
// tool's `type` description, and its `params` description. The two schema
// strings are the LIVE ones an LLM reads, and they are also what the generated
// params table in collect.md is rendered from, so asserting here covers the
// generated page too.
func TestCollectPrecedence_StatedOnAllThreeSurfaces(t *testing.T) {
	def := CollectToolDef()

	t.Run("the collect tool's type description", func(t *testing.T) {
		typeProp, ok := def.InputSchema.Properties["type"]
		require.True(t, ok, "the collect schema must declare type")
		assert.Contains(t, typeProp.Description, collectPrecedenceStatement,
			"the type description is what a calling LLM reads to decide what a collect type means")
	})

	t.Run("the collect tool's params description", func(t *testing.T) {
		params, ok := def.InputSchema.Properties["params"]
		require.True(t, ok, "the collect schema must declare params")
		assert.Contains(t, params.Description, collectPrecedenceStatement,
			"params is read by a shadowing collect too, so its description must key on the DISPATCH rather than on the name")
		assert.NotContains(t, params.Description, "Built-in types ignore it",
			"that sentence became false the moment a collect(type:\"gcp\") carrying a registration reads params")
	})

	t.Run("the custom-collector guide", func(t *testing.T) {
		pages := guidePagesMentioningCustomCollectors(t)
		page, ok := pages["tools/custom_collector.md"]
		require.True(t, ok, "the census must reach the custom-collector guide, the page a collector author reads")
		assert.Contains(t, page, collectPrecedenceStatement,
			"the guide is where an author registering a family under a built-in collector's name learns which one runs")
	})
}
