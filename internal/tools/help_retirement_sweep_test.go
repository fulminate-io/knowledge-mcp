// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// help_retirement_sweep_test.go — SPLIT OUT OF help_currency_claims_test.go,
// which reached the repository's hard 500-line ceiling.
//
// The split is by SUBJECT: the sibling file pins individual operational claims a
// currency sweep found refuted, one row per claim. These two tests are the other
// shape — a sweep over the WHOLE surface for a class — and they are what the
// per-site rows cannot be, because the per-site list is exactly what a currency
// sweep misses.

// ---------------------------------------------------------------------------
// THE RETIREMENT SWEEP. Two properties over the WHOLE help surface and every
// first-class tool schema, asserted structurally rather than as a list of
// hand-written cases.
//
// WHY A SWEEP AND NOT MORE PER-SITE ROWS. The cloud and logs families were
// named in six help topics and four tool schemas, and the twelve deleted
// analyzers in one. A row per site is a row per site FOUND, which is exactly
// what a currency sweep misses; and the next family retired after these two
// would need the whole exercise repeating. These two tests read the retirement
// map and the analyzer registry — the same sources the product answers from —
// so they keep working for a family nobody has retired yet.
//
// THE PRODUCT-CLOUD WORDS ARE NOT HITS AND MUST NOT BECOME ONE. "Fulminate
// Cloud", "cloud account", "cloud backend" and "cloud status" name the PAID
// BACKEND, which is alive. That is why these tests match graph-selector SHAPES
// and analyzer NAMES rather than the bare word cloud: a substring sweep over
// "cloud" would break the sync and manage help and would be reverted on its
// first run, which is worse than no gate at all.

// deletedTopologyAnalyzers are the twelve names R14 removed. They are LISTED
// rather than derived from the registry, because the registry no longer knows
// them — a derived list would be empty and the assertion vacuous. The absence
// control below is what keeps the list honest.
var deletedTopologyAnalyzers = []string{
	"cert_expiry", "cross_provider_blast", "event_chain",
	"monitoring_coverage", "orphan", "serverless_depth",
	"aws_public_exposure", "aws_sg_reachability", "iam_escalation",
	"k8s_public_exposure", "k8s_reachability", "unified_public_exposure",
}

// analyzerOffers returns the spellings in which a help body OFFERS an analyzer
// name — the forms a reader would copy into a query(mode:"topology") call.
//
// IT IS NOT A BARE SUBSTRING MATCH, and the reason is measured rather than
// anticipated. Eleven of the twelve names carry an underscore and appear nowhere
// in English, so a substring is safe for them. `orphan` is an ordinary English
// word: run as a substring it flags "orphaned fixture" in help("manage_checks")
// and "orphaned L2 segments" in help("manage"), neither of which has anything to
// do with the deleted analyzer. A gate that reds on correct prose gets deleted on
// its first run, so it matches the CALL SHAPES instead: the algorithm selector in
// both JSON spacings, and the two-space-indented catalog line the topology
// topic lists analyzers on.
func analyzerOffers(name string) []string {
	return []string{
		`"algorithm": "` + name + `"`,
		`"algorithm":"` + name + `"`,
		"\n  " + name + " ",
		"/ " + name + " ",
	}
}

// TestHelp_NoTopicNamesADeletedAnalyzer sweeps every help topic for the twelve.
//
// THE CONTROL IS A SURVIVING ANALYZER NAME, asserted present in the topology
// topic. Without it, a topic body that became empty — or a walk over an empty
// map — would satisfy every absence below while documenting nothing.
func TestHelp_NoTopicNamesADeletedAnalyzer(t *testing.T) {
	require.NotEmpty(t, helpTopics, "control: the help topic map is populated")

	for topic, body := range helpTopics {
		for _, gone := range deletedTopologyAnalyzers {
			for _, offer := range analyzerOffers(gone) {
				assert.NotContainsf(t, body, offer,
					"help(%q) still documents the analyzer %q as %q, which this release deleted — a "+
						"reader following it gets an unknown-algorithm refusal", topic, gone, offer)
			}
		}
	}

	// CONTROL: a surviving analyzer is still documented, so the sweep is a
	// statement about the twelve rather than about an emptied surface.
	assert.Contains(t, helpTopics["topology"], "pagerank",
		"control: help(\"topology\") must still name an analyzer that exists")
}

// TestHelpAndSchemas_OfferNoRetiredGraphName is the family half, and it reads
// the retirement map rather than a literal pair of names.
//
// THE SHAPES IT MATCHES are the ones a reader would COPY: a JSON selector
// ("graph": "cloud"), a pipe-separated vocabulary list (code | cloud | logs),
// and a slash-separated one (code/cloud/logs). Prose that merely contains the
// word is not matched, which is the whole reason the paid-backend wording
// survives this test unharmed.
func TestHelpAndSchemas_OfferNoRetiredGraphName(t *testing.T) {
	retired := []string{"cloud", "logs", "transformers"}
	for _, name := range retired {
		_, isRetired := kgtypes.RetiredGraphTypeReason(name)
		require.Truef(t, isRetired,
			"control: %q must be recorded as retired, or this test is asserting about nothing", name)
	}

	// The offer shapes, built per name so a failure names the exact spelling.
	offers := func(name string) []string {
		return []string{
			`"graph": "` + name + `"`, `"graph":"` + name + `"`,
			`| ` + name + ` |`, `| ` + name + `)`, `(` + name + ` | `,
			`/` + name + `/`,
		}
	}

	surfaces := map[string]string{}
	for topic, body := range helpTopics {
		surfaces["help("+topic+")"] = body
	}
	for _, def := range sweptToolDefs() {
		surfaces["schema:"+def.Name+":description"] = def.Description
		for pname, prop := range def.InputSchema.Properties {
			surfaces["schema:"+def.Name+":"+pname] = prop.Description
		}
	}
	require.NotEmpty(t, surfaces, "control: there are surfaces to sweep")
	require.GreaterOrEqual(t, len(sweptToolDefs()), 16,
		"control: the swept tool-schema list has not been silently truncated")

	for where, body := range surfaces {
		for _, name := range retired {
			for _, offer := range offers(name) {
				assert.NotContainsf(t, body, offer,
					"%s OFFERS the retired graph name %q as a value (%q). The family is refused by "+
						"the product, so this is an instruction to make a call that errors. Note the "+
						"paid backend's own wording (Fulminate Cloud, cloud account, cloud backend, "+
						"cloud status) is deliberately NOT matched here and must stay.", where, name, offer)
			}
		}
	}

	// CONTROL A: a SURVIVING family is still offered in these shapes, so the
	// sweep cannot be satisfied by a surface that offers no graph at all.
	joined := strings.Join([]string{helpTopics["topology"], helpTopics["search"], helpTopics["query"]}, "\n")
	assert.Contains(t, joined, "code",
		"control: the surviving families must still be offered somewhere in the swept surface")

	// CONTROL B: the matcher actually fires. A retired name in an offer shape,
	// checked against the same predicate the loop uses, must be caught — without
	// this the loop could be walking an empty offer list and passing silently.
	require.NotEmpty(t, offers("cloud"), "control: the offer shapes are non-empty")
	assert.Contains(t, `x "graph": "cloud" y`, offers("cloud")[0],
		"control: the offer matcher recognizes a retired-name selector when one is present")
}

// TestMutateSurfaces_UpsertVocabularyIsTheRealAllowlist pins the one schema
// claim the sweep above cannot see, because it names NODE types rather than
// graph names.
//
// THE LIST IS RESTATED HERE AND THAT IS DELIBERATE. The allowlist itself lives
// in the SERVER module, and the repository's architecture invariant forbids a
// shared package between the two binaries, so the client cannot import it. What
// this test can do — and does — is keep the client's two statements of the list
// agreeing with each other and with the count they claim, so a future edit to
// one of them cannot leave the other behind.
func TestMutateSurfaces_UpsertVocabularyIsTheRealAllowlist(t *testing.T) {
	var mutateDesc string
	for _, def := range sweptToolDefs() {
		if def.Name == "mutate" {
			mutateDesc = def.Description
		}
	}
	require.NotEmpty(t, mutateDesc, "control: the mutate tool schema was found")

	for _, gone := range []string{"log-backend", "worker"} {
		assert.NotContainsf(t, mutateDesc, "graph_type_def, "+gone,
			"the mutate schema still lists %q in the upsert allowlist; the server's "+
				"upsertAllowedTypes holds proxy, graph_type_def and criterion", gone)
	}
	assert.NotContains(t, helpTopics["mutate"], "log-backend",
		"help(\"mutate\") still names the removed log-backend node type in the upsert allowlist")

	// POSITIVE, so neither assertion is satisfied by deleting the paragraph: both
	// surfaces must still state the vocabulary, and state the same one.
	for where, body := range map[string]string{"schema": mutateDesc, "help": helpTopics["mutate"]} {
		for _, live := range []string{"proxy", "graph_type_def", "criterion"} {
			assert.Containsf(t, body, live, "%s must still name the admitted upsert type %q", where, live)
		}
		assert.NotContainsf(t, body, "Five types", "%s still claims FIVE admitted types", where)
	}
}

// sweptToolDefs is the tool-schema half of the surface the retirement sweep
// walks. It is written out rather than derived, because the package publishes
// each schema as its own constructor and has no registry of them — and a helper
// that silently returned fewer than it should would weaken every assertion
// above. The length control in the sweep is what catches a truncated list.
func sweptToolDefs() []kgtools.MCPTool {
	return []kgtools.MCPTool{
		CollectToolDef(), QueryToolDef(), SearchToolDef(), TraverseToolDef(),
		MutateToolDef(), ManageToolDef(), GraphTypeToolDef(), HelpToolDef(),
		SyncToolDef(), DeleteToolDef(), AssembleToolDef(), AstToolDef(),
		ThoughtsToolDef(), FileSymbolsToolDef(), RecordDecisionToolDef(),
		ManageChecksToolDef(),
	}
}

// TestSurfaces_OfferNoToolOperationOutsideItsOwnEnum is the THIRD class this
// sweep covers, and it is the ONLY test covering it — a narrower sibling that
// looped a hard-coded list of five operation names was deleted rather than kept
// beside this one, because it was strictly weaker and two copies of one decision
// cannot be told apart when one of them rots.
//
// WHAT GOT THROUGH, and why the first two legs could not see it. The retirement
// sentence for cloud and logs — rendered by collect, the search rail, the stats
// arm and drop_graph — told the reader to
// `custom_collector(operation:"register")`. That operation does not exist: the
// tool's enum is [list], and its own description says REGISTRATION IS A CONFIG
// FILE installed with `knowledge collector add`. The graph-name leg sweeps for a
// retired FAMILY offered as a selector value and the analyzer leg for a deleted
// ANALYZER offered as an algorithm; an offered TOOL OPERATION that the tool's own
// enum rejects is a third class neither models, which is why a help topic
// carrying it sailed through a sweep written in the same commit.
//
// IT IS DERIVED FROM THE SCHEMAS THE PRODUCT PUBLISHES, over EVERY tool that
// declares an operation enum rather than the one that failed: mutate, manage,
// custom_collector, sync, ast, thoughts and manage_checks all do. So it keeps
// working for the next operation retired from any of them, and needs no edit
// when one is added.
//
// A REFUSAL NAMING NO ROUTE IS UNACTIONABLE; ONE NAMING A ROUTE THAT ERRORS IS
// WORSE, because the reader spends a call finding out and then distrusts the
// next sentence they are given.
//
// IT MATCHES THE CALL SHAPE AND ALWAYS NAMES THE TOOL, for the reason the
// analyzer leg records for the word `orphan`: an earlier draft matched a bare
// `"operation": "update"` pair, which flagged help("mutate") and
// help("statuses") documenting mutate's own perfectly good update operation. A
// gate that reds on a correct example of a different tool gets deleted on its
// first run.
func TestSurfaces_OfferNoToolOperationOutsideItsOwnEnum(t *testing.T) {
	// THE DECLARED VOCABULARY, per tool, off the published schema.
	declared := map[string]map[string]bool{}
	for _, def := range sweptToolDefs() {
		prop, ok := def.InputSchema.Properties["operation"]
		if !ok || len(prop.Enum) == 0 {
			continue
		}
		set := map[string]bool{}
		for _, op := range prop.Enum {
			set[op] = true
		}
		declared[def.Name] = set
	}
	require.NotEmpty(t, declared,
		"control: some tool declares an operation enum, or this sweep asserts nothing")
	require.Contains(t, declared, "custom_collector",
		"control: the tool whose retired operation motivated this leg is among them")

	surfaces := sweptSurfaces(t)

	// EVERY `<tool>(operation:"X")` OFFER IN EVERY SURFACE IS CAPTURED AND
	// CHECKED AGAINST THAT TOOL'S OWN ENUM. The direction is what matters and it
	// is the direction an earlier draft got backwards.
	//
	// THE DRAFT LOOPED A HARD-CODED LIST of five names that had ever been
	// advertised and skipped the ones the enum declared. That is a DENYLIST
	// wearing an enum's clothes: it reds on `remove` and is SILENT on `bogus`,
	// which is every operation nobody thought to list — measured, planting
	// `custom_collector(operation:"bogus")` in a help topic left it green. Capture
	// the name out of the offer and require MEMBERSHIP, and the question becomes
	// "is this real" rather than "is this one of five I remembered".
	//
	// THE CAPTURE CLASS ADMITS WHAT REAL OPERATION NAMES CONTAIN: letters,
	// digits, underscores and HYPHENS. manage declares `prune-cache`; a
	// letters-only class would not capture it, and an offer it cannot capture is
	// an offer it silently blesses.
	offer := regexp.MustCompile(`([a-z_]+)\(\s*\{?\s*"?operation"?\s*:\s*"([a-zA-Z_][a-zA-Z0-9_-]*)"`)
	var checked int
	for where, body := range surfaces {
		for _, m := range offer.FindAllStringSubmatch(body, -1) {
			tool, op := m[1], m[2]
			enum, known := declared[tool]
			if !known {
				continue // not a tool that declares an operation vocabulary
			}
			checked++
			assert.Truef(t, enum[op],
				"%s offers %s(operation:%q), which that tool does not declare. Its enum is %v. "+
					"A reader copies this and gets a refusal; an instruction that cannot be "+
					"executed is worse than none.", where, tool, op, sortedEnumValues(enum))
		}
	}

	// THE CONTROL THAT THE SWEEP READ ANYTHING AT ALL. Without it a regex that
	// stopped matching would pass every surface silently, which is the exact
	// failure shape this whole file exists to prevent.
	assert.Positive(t, checked,
		"control: at least one tool-operation offer was found and checked; zero means the "+
			"matcher stopped matching and this sweep is asserting nothing")

	// AND THE CONTROL THAT A REAL OPERATION IS STILL DOCUMENTED. The assertion
	// above is a membership test, so a product that documented NO operation at
	// all would satisfy it while telling a reader nothing. custom_collector is
	// the tool this leg was written for and `list` is the one operation it has.
	var namesList bool
	for _, body := range surfaces {
		if strings.Contains(body, `custom_collector(operation:"list")`) {
			namesList = true
		}
	}
	assert.True(t, namesList,
		"control: some surface must still show an operation that DOES exist, or this sweep is "+
			"satisfied by a product that documents nothing")
}

// sweptSurfaces is the shipped text this file sweeps: every help topic, every
// first-class tool description and parameter description, and the retirement
// sentences — which are none of those three and are exactly where the operation
// defect lived, reaching a caller through four different refusals.
func sweptSurfaces(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for topic, body := range helpTopics {
		out["help("+topic+")"] = body
	}
	for _, def := range sweptToolDefs() {
		out["schema:"+def.Name+":description"] = def.Description
		for pname, prop := range def.InputSchema.Properties {
			out["schema:"+def.Name+":"+pname] = prop.Description
		}
	}
	for _, family := range []string{"cloud", "logs", "transformers"} {
		reason, retired := kgtypes.RetiredGraphTypeReason(family)
		require.Truef(t, retired, "control: %q is recorded as retired", family)
		out["retirement-reason:"+family] = reason
	}
	require.NotEmpty(t, out, "control: there are surfaces to sweep")
	return out
}

// sortedEnumValues renders an enum set for a failure message, so a reader is
// told the vocabulary that WOULD have worked rather than only the value that did
// not. It is not named sortedKeys because the package already has one.
func sortedEnumValues(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
