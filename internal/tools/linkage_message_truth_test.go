// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// linkage_message_truth_test.go — the two operator-facing messages about the
// LINKAGE graph say what fills it today, and never what used to.
//
// WHY THESE TWO AND NOT A COMMENT SWEEP. Both are product output, and both are
// reached at the exact moment an operator is looking for guidance: the first
// when the linkage graph reads zero nodes and zero edges, the second when a
// metadata-stats read on it returns no rows. Until this change they told the
// reader to wait for, or to run, a code-to-cloud collect — a relationship the
// product stopped deriving when the built-in cloud collectors were removed. An
// operator following either one waits for something that will not happen.
//
// WHAT ACTUALLY FILLS THE GRAPH, read at this tree rather than assumed:
// linker/client.go:66 RunAll dispatches exactly one pass, and
// linker/dockerfile.go:23 LinkDockerfiles is it — for every code graph it
// parses each Dockerfile's COPY and ADD directives and emits a BUILDS edge into
// the linkage graph for the file or package each names. It runs from
// manage(operation:"link") (manage.go:150 handleClientLinker) and from the
// post-collect tail of a CODE collect, scoped to the graph that collect just
// produced. THE TRIGGER MOVED HERE from the CI/CD families when they were
// retired: the pass reads code graphs on both sides, so it fires where its input
// changes. Both messages therefore name a code collect, and the retired wording
// below keeps the old one from coming back in silence.
//
// EACH ASSERTION HAS TWO HALVES, and the second is the one that survives a
// careless rewrite. The positive half requires the message to still NAME what
// fills the graph — an absence check alone is satisfied by a message that says
// nothing. The negative half requires the retired wording to be absent, so a
// revert cannot restore it in silence, which is the shape three earlier locking
// assertions in this sweep had to be rewritten into after they pinned the stale
// text instead.

// retiredLinkageClaims are the phrases the two messages carried before the
// built-in cloud collectors were removed. Each described a producer the product
// no longer has.
var retiredLinkageClaims = []string{
	"tier-1 linker",
	"code-to-cloud",
	"code/cloud collect",
	// THE CI/CD TRIGGER IS RETIRED WORDING NOW. It was true for one release and
	// is the phrase both constants carried until this ticket moved the trigger to
	// the code collect; naming it here is what stops a revert restoring a route an
	// operator would follow into a refusal.
	"CI/CD collect",
}

// TestLinkageEmptyMessage_NamesTheProducerThatExists pins the message returned
// when the linkage graph is empty, which is precisely when an operator reads it.
func TestLinkageEmptyMessage_NamesTheProducerThatExists(t *testing.T) {
	msg := emptyLinkageMessage
	require.NotEmpty(t, msg, "the empty-linkage message must exist for the assertions below to mean anything")

	// POSITIVE HALF: it names the pass that fills the graph and how to run it.
	for _, want := range []string{
		"cross-graph linker",
		"BUILDS",
		"Dockerfile",
		`manage(operation: "link")`,
		"code collect",
	} {
		assert.Containsf(t, msg, want,
			"the empty-linkage message must tell the operator what fills the graph and how to run it. Missing %q in: %s", want, msg)
	}

	// NEGATIVE HALF: the retired producers are gone and stay gone.
	for _, gone := range retiredLinkageClaims {
		assert.NotContainsf(t, msg, gone,
			"the empty-linkage message names %q, a producer this product no longer has — an operator reading it waits for "+
				"a relationship nothing will derive", gone)
	}
}

// TestLinkageStatsHint_NamesTheProducerThatExists pins the repopulation hint the
// empty metadata-stats message interpolates for the linkage graph.
func TestLinkageStatsHint_NamesTheProducerThatExists(t *testing.T) {
	hint := metadataStatsCollectHintClient(queryArgs{Graph: "linkage"})
	require.NotEmpty(t, hint)

	for _, want := range []string{
		"cross-graph linker",
		`manage(operation: \"link\")`,
		"code collect",
	} {
		assert.Containsf(t, hint, strings.ReplaceAll(want, `\"`, `"`),
			"the linkage stats hint must name the pass that refreshes them. Missing %q in: %s", want, hint)
	}
	for _, gone := range retiredLinkageClaims {
		assert.NotContainsf(t, hint, gone,
			"the linkage stats hint names %q, which describes a collect this product refuses at the door", gone)
	}
}

// TestMetadataStatsHint_OffersNoRetiredFamily is the arm-level half: the hint
// switch itself carries no arm for a retired family.
//
// THE ARMS FOR cloud, logs AND cicd WERE DEAD, not merely stale. Every caller
// reaches this through a graph selector the client and server both refuse for a
// retired name, so none of them could be entered; the cloud arm shared its case
// label with cicd and outlived it by one ticket, and the cicd arm went when that
// family was retired too. Deleting an unreachable arm is a zero-behaviour
// cleanup, and this test is what keeps a later edit from reintroducing one: a
// retired name must fall to the default, whose text names no family at all.
func TestMetadataStatsHint_OffersNoRetiredFamily(t *testing.T) {
	// CONTROL FIRST, in the same run: a SURVIVING family must still get its own
	// arm. Without it a switch collapsed to nothing but a default would satisfy
	// every assertion below while telling every caller the same useless thing.
	for _, live := range []string{"practice", "linkage"} {
		assert.NotEqualf(t, metadataStatsCollectHintClient(queryArgs{Graph: "unknown-family"}),
			metadataStatsCollectHintClient(queryArgs{Graph: live}),
			"%s must still have its own hint arm; if it fell to the default the assertions below would be vacuous", live)
	}

	fallback := metadataStatsCollectHintClient(queryArgs{Graph: "unknown-family"})
	for _, retired := range []string{"cloud", "logs", "cicd"} {
		assert.Equalf(t, fallback, metadataStatsCollectHintClient(queryArgs{Graph: retired}),
			"the hint switch still has an arm for the retired %q family; it is unreachable, and an unreachable arm is a "+
				"standing invitation to treat the name as live", retired)
	}
}

// TestDomainGraphLabel_OffersNoRetiredFamily is the same shape for the composite
// modes' per-graph label.
func TestDomainGraphLabel_OffersNoRetiredFamily(t *testing.T) {
	// CONTROL: a surviving instance-keyed family still takes the instance-key
	// branch, so the assertions below are about the retired names and not about a
	// switch that stopped working. It used to be a practice read qualified by its
	// `language`; that field is refused on every practice arm, so the control
	// moved to `code`, which is the instance-keyed family left.
	require.Equal(t, "code:myrepo", domainGraphLabel(queryArgs{Graph: "code", Repo: "myrepo"}),
		"the label switch must still qualify a surviving instance-keyed family with its instance")
	require.Equal(t, "practice:hub-go", domainGraphLabel(queryArgs{Graph: "practice", Source: "hub-go"}),
		"and a hub-scoped practice read still names the hub that answered")

	// A retired name falls to the default, which renders the graph string bare.
	// That is the same rendering any unregistered name gets, which is correct:
	// the client refuses the name before a caller can see a label at all.
	//
	// THE ACCOUNT IS SUPPLIED ON PURPOSE in each row: with the account-keyed arm
	// gone there is no field left that could qualify these labels, so a label that
	// came back qualified would mean the arm returned.
	for _, retired := range []string{"cloud", "cicd"} {
		assert.Equalf(t, retired, domainGraphLabel(queryArgs{Graph: retired, Account: "acme"}),
			"the label switch still has an arm qualifying the retired %q family with an account", retired)
	}
}

// TestDomainTarget_CarriesThePracticeLanguageToTheWire is the observer the
// composite-mode selector composer did not have.
//
// WHY IT IS NEEDED, measured rather than assumed. domainTarget copies `language`
// onto the Target RAW for every family, which is what lets the SERVER refuse a
// practice read on the composite modes — mode=metadata_stats, mode=topology,
// mode=pivot and mode=correlations are generic arms with no practice-specific
// gate of their own, by the same reasoning that keeps a practice check off each
// of them. Dropping the field here would leave those reads carrying no selector
// at all and served SILENTLY from the combined graph, which is the redirect the
// per-family partition exists to close.
//
// NOTHING READ IT. Removing `Language: a.Language` from domainTarget left every
// package in this module green — the same hole class the topology/foundation
// composers had, in the opposite direction: there the correct behavior is not to
// compose, here it is to compose. engine.buildTarget, the read twin, IS observed
// (TestBuildTarget, TestCompileQuery_ModulesMode and
// TestPracticeTraverse_LoudAndCharacterized all red when its copy is dropped),
// so this closes the one of the pair that was open.
//
// THE CONTROLS ARE IN THE SAME TEST. A composer that copied every field for
// every family satisfies the practice row alone, and one that dropped every
// field satisfies nothing here — so the code and name-keyed rows are what make
// the practice row a statement about the FIELD rather than about the composer
// being verbatim or empty.
func TestDomainTarget_CarriesThePracticeLanguageToTheWire(t *testing.T) {
	prac := domainTarget(queryArgs{Graph: "practice", Language: "go"})
	require.NotNil(t, prac)
	assert.Equal(t, "practice", prac.GetGraph())
	assert.Equal(t, "go", prac.GetLanguage(),
		"the caller's language must reach the wire, where validateGraphSelector refuses it; "+
			"dropping it here serves the composite read from the combined graph and says nothing")

	// THE CONTROLS: the fields a family genuinely consumes still ride, so the row
	// above is the raw copy rather than a composer that fills everything.
	code := domainTarget(queryArgs{Graph: "code", Repo: "myrepo"})
	assert.Equal(t, "myrepo", code.GetRepo(), "control: a code composite read carries its repo")
	assert.Empty(t, code.GetLanguage(), "control: and no language, because the caller supplied none")

	web := domainTarget(queryArgs{Graph: "web", Name: "docs-site"})
	assert.Equal(t, "docs-site", web.GetName(), "control: a name-keyed family carries its name")
}
