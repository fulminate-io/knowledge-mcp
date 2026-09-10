// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// collect_docs_gosource_census_test.go — the GO-SOURCE half of the retired
// vocabulary census, split out of collect_docs_contract_test.go, which was 482
// lines and closing on the 500-line lefthook ceiling (lefthook.yml:126-147, warn
// above 300, error above 500). The docs half stays there; this file owns the
// vocabulary the walk matches, the exemption map, and the walk itself.

// retiredExecGoVocabulary is the Go-source half of the vocabulary, and it is
// DELIBERATELY NOT retiredExecVocabulary.
//
// The docs list carries bare tokens — graph_name, param_schema, before exec —
// which are precise in a guide page about the collector contract and far too
// loose in Go source, where they collide with things that have nothing to do
// with it. Measured over the original three packages, the docs list flagged five
// sites and three were accidents: graph_names.go's own package doc,
// help_patterns.go's "immediately before executing", and a test file named
// param_schema_parity_residue_test.go. Exempting those would have laundered
// three non-instances into a permission list and left the instrument no sharper.
// So the Go arm names the record keys in their QUALIFIED form and the retired
// contract's prose phrases, each of which can only mean the deleted artifact.
//
// THE LIST GREW ONCE PER DEFECT SUB-CLASS A REVIEW FOUND SURVIVING, and the
// sub-classes are named here because each needs a different KIND of term and a
// reader adding a fourth needs to know which kind theirs is.
var retiredExecGoVocabulary = []string{
	// SUB-CLASS 0 — the retired exec contract's own record keys and prose. The
	// original list.
	"collector binary",
	"stdin transport",
	"flag transport",
	"stdout envelope",
	"param transport",
	"collector.binary_path",
	"collector.param_transport",
	"collector.param_schema",

	// SUB-CLASS A(i) — THE DELETED CLIENT PACKAGES, by their qualified in-tree
	// path. A comment pointing a reader at a package that is not there is the
	// same defect class as one naming a retired config key: the next author
	// follows the pointer, finds nothing, and cannot tell a typo from a
	// deletion. Widening the WALK ALONE does not catch these — a wider walk with
	// the same vocabulary just scans more files and stays green — so the
	// vocabulary and the walk moved together. Each path below was confirmed
	// absent with `test -e cmd/knowledge/<path>`, and the same command reports
	// topology/, thought/, tools/, collector/cicd/, externalcollector/ and
	// graphtypecrud/ present, so the check is not asserting against a mistyped
	// root.
	"internal/logwire",
	"internal/collector/logs",
	"internal/cloudresolver",
	"internal/topology/cloud",
	"internal/topology/exposure",

	// SUB-CLASS A(ii) — DELETED SYMBOLS CITED IN PROSE. Each of these names a
	// function, type or constant that exists in NO Go code in this repository;
	// every occurrence is therefore a comment sending a reader after something
	// that is not there. The set was DERIVED rather than hand-picked, which is
	// the correction that produced it: an earlier pass enumerated three names by
	// hand and missed a dozen. The derivation is one pass that splits every line
	// of every tracked .go file at the first "//", tallies CamelCase tokens per
	// side, and reports tokens with comment hits and zero code hits. Re-run it
	// before adding to this list; hand-picking is what left the gap.
	//
	// THE CODE SIDE OF THAT DERIVATION MUST BE REPOSITORY-WIDE. Tallied over the
	// client and server trees alone it reports about fifty subject-matching
	// candidates, most of them real symbols living in gen/ or cmd/collectors.
	// Widened to every tracked .go file the same pass reports only names that
	// are genuinely nowhere.
	"GraphCloud",
	"GraphLogs",
	"InterceptLogsQuery",
	"InterceptLogsManage",
	"MaterializeLogGraph",
	"NodeLogBackend",
	"NodeCloudResource",
	"shipLogsResult",
	"searchLogs",
	"resolveLogs",
	"armLogsQuery",
	"handleLogsQuery",
	"decodeLogBrowseResponse",
	"dropLogGraph",
	"fetchAllLogEdges",
	"logsIngestHandler",
	"qpParityLogGraph",
	"queryCloudResources",
	"tryRegisteredCollect",
	"upsertLogBackend",
	"buildLogGraphSummary",
	"TestInterceptManage_LogsOperationsFallThrough",

	// SUB-CLASS A(iii) — THE cicd RETIREMENT'S OWN DELETED SYMBOLS. Same class as
	// A(ii) above and derived the same way: each names a function, type or
	// constant that exists in NO Go code in this repository, so every occurrence
	// is a comment sending a reader after something that is not there. The list
	// is longer than the cloud and logs ones because the family took a whole
	// per-account resource READER with it, not just a constant block.
	"GraphCICD",
	"NodeCICDResource",
	"InterceptQueryCloudCICD",
	"cicdGraphKind",
	"ResourceKindCICD",
	"ResourceKindCloud",
	"EmbedFamilyCICD",
	"buildCICDProxy",
	"renderCICDForRerank",
	"postCollectLinkerTypes",
	"resourceGraphKind",
	"listResourceGraphs",
	"resourceStats",
	"resourceGetNode",
	"resourceTarget",
	"sampleFailureNotice",
	"composeResourceSearchClient",
	"RenderResourceNode",
	"RenderResourceSearch",
	"EdgeDeploysTo",
	"EdgeRequiresApproval",
	"EdgeUsesSecret",
	"EdgeFederates",
	"EdgeTriggeredBy",
	"EdgeAccessedBy",
	"EdgeStoredIn",
	//
	// FieldAccount, armCloudCICD*, resourceBrowse, resourceQueryText,
	// fetchTypeSamples, RenderResourceBrowse and resolveAccountGraph are
	// DELIBERATELY ABSENT though they are equally gone. Each is named by a
	// PAST-TENSE retirement record that states what left and why — the
	// sha-stamped-parity class the cloud renderers already occupy — and a term
	// for one would need an exemption per record, which is the tell that the
	// term is wrong rather than the sites.

	// SUB-CLASS A(i), continued — the deleted client package.
	"internal/collector/cicd",
	//
	// formatCloudNode / formatCloudSearchResults / formatCloudBrowse are
	// DELIBERATELY ABSENT though the derivation returns them. They name the
	// SERVER's own removed renderers, and render_resource.go cites them as the
	// provenance of the client ports that replaced them, in the past tense and
	// beside the words "since-removed". That is the sha-stamped-parity class: a
	// historical note, not a claim of current behavior. Five exemptions would be
	// needed to keep them, and a permission list that long is the tell that the
	// term is wrong rather than the sites.

	// SUB-CLASS B — STALE COUNTS AND FAMILY LISTS about live maps and
	// predicates, which no symbol census can reach: every token in them is an
	// ordinary word. These are the exact phrases removed, planted here so a
	// revert reds. A count in a comment about a live map is the defect class,
	// and the corrected sites now say "read the map" instead of restating one.
	"aws/gcp/azure/k8s/github/bitbucket/gitlab/code",
	"every cloud and cicd hook",
	"SEVEN OF THE EIGHT ALLOWLISTED NAMES",
	"cloud/CI-CD-shaped",
	"(linkage, logs)",

	// SUB-CLASS D — A RETIRED GRAPH NAME OFFERED IN A LIVE PRODUCT STRING, which
	// is the highest-cost member of the family: it is output an LLM reads and
	// copies, not a comment. A comment misleads the next author; a refusal string
	// misleads every caller immediately, and teaches the caller to retry with a
	// value the product refuses at the door.
	// TestHelpAndSchemas_OfferNoRetiredGraphName sweeps help topics and tool
	// descriptions in six copyable shapes and reaches NONE of these, because an
	// error message and a CLI flag help are neither of its two surfaces.
	//
	// ONE TERM PER SPELLING OF THE ENUMERATION, and that is the whole lesson of
	// this list. The first version carried the PIPE-separated spelling alone and
	// closed the two sites that used it; a review then found six more where the
	// same class had relocated one construct over, into a COMMA-separated
	// enumeration. A term matching one spelling is a term the class walks around.
	// Anything added here should be paired with a planted positive that restores
	// the exact string, so the spelling is covered rather than assumed.
	// EVERY TERM BELOW IS CARRIED BY BOTH MODULES' VOCABULARIES, and the reason
	// is the same mistake one level down. The first comma-separated terms were
	// added only to the module whose sites the review had named — which is a term
	// per SITE wearing a term-per-spelling costume. The seventh site of this class
	// was then found in THIS module by a term that lived only in the server's
	// list: internal/bootstrap/segment_manager_wiring_test.go carried
	// "code, knowledge, cloud". A string is not owned by the module it happens to
	// sit in today, so the lists are kept identical for this sub-class.
	"cloud|cicd|practice|logs",
	"code, cloud, cicd, knowledge",
	"account (cloud, cicd)",
	"code, cloud, cicd, knowledge, web, or pdf",
	"code, cloud, cicd, practice, logs, web, pdf, checks",
	"code, knowledge, cloud",

	// SUB-CLASS D, continued — the cicd retirement's own re-spellings. The terms
	// above name cloud AND cicd together, so they could not reach a string that
	// dropped cloud a release ago and kept cicd. These are the enumerations that
	// did.
	"knowledge, code, cicd, practice",
	"code, cicd, knowledge",
	"code | cicd | knowledge",
	"knowledge|code|practice|cicd",
	"knowledge|code|cicd|practice",
	"account (cicd)",
	"cicd|practice",
	"practice, cloud, cicd",
	"code, cicd, practice",
	"graph=cicd requires",
	"or a CI/CD collect",
}

// retiredVocabularyExemptions are the (file, term) pairs where a retired term is
// CORRECT: a refusal path or a retirement record describing, in the past tense,
// the thing it names.
//
// THE KEY IS THE REPO-RELATIVE PATH, NOT THE BASE FILENAME. The walk covers a
// whole module, so two files sharing a base name across packages would share an
// exemption keyed on the name alone — a permission granted for one site silently
// covering another the author never saw. The failure message already reported
// the path, so the key and the message now name the same thing.
// AN EXEMPTION THAT NEVER FIRES IS DELETED, NOT KEPT FOR TIDINESS. This map
// once carried internal/tools/graphtype_crud.go:collector.binary_path for a
// nested-param guard that had already stopped naming the key — the term left
// that file two commits before this census existed, so the entry granted the
// retired exec key a standing pass in the graph-type registration path, the one
// file most likely to reintroduce it, and cost nothing to remove. The class
// stays covered by the vocabulary term itself. Every entry below reds the census
// when removed; that is the test for whether one belongs here.
var retiredVocabularyExemptions = map[string]string{
	// A RETIREMENT RECORD IS NOT A HIT. This one states, in the past tense and
	// with the count it moved, that the arm left with the built-in log
	// collectors — which is the disclosure the sweep wanted and the opposite of
	// the defect.
	"internal/tools/query_arm_registry.go:armLogsQuery": "the registry's arm-count comment records that armLogsQuery WAS the logs arm and left with the built-in log collectors, with the 51->50 count it moved",

	// A COLLISION THE WIDER WALK INTRODUCED, kept as an exemption rather than
	// fixed by narrowing the term. "stdout envelope" is the retired exec
	// contract's own phrase for what a collector binary printed, and it is ALSO
	// ordinary English for the JSON object the Claude CLI writes to stdout — a
	// different subprocess, a different contract, the same two words. Narrowing
	// the term to keep these silent would blunt it for the sites it was added
	// for; two named exemptions cost less and say which is which.
	"internal/llm/claudecli/subprocess.go:stdout envelope":      "the CLI subprocess classifies its exit from the JSON object claude writes to stdout; unrelated to the retired collector contract",
	"internal/llm/claudecli/subprocess_test.go:stdout envelope": "the test for the classification above, naming the same object",
}

// goCensusRoot is the module root this census walks, relative to this package.
const goCensusRoot = "../.."

// goCensusSkipDirs are directories the walk does not descend into. testdata
// holds fixture trees that deliberately capture OLD source (scripts/testdata
// carries whole captured checkouts), and assets is the gitignored embed mirror
// sync-assets writes.
var goCensusSkipDirs = map[string]bool{
	"testdata": true,
	"assets":   true,
}

// goCensusSkipFiles are the two files that DEFINE the vocabularies, and a file
// that defines the needle cannot be its own subject: every term appears in it as
// a Go string literal, so a census that read them would report one hit per term
// forever and drown the real ones. This is not an exemption — an exemption says
// a named term is CORRECT at a named site — it is the walk declining to grade
// its own instrument.
//
// NOTHING ELSE IS SKIPPED BY NAME. Any other file that grows a retired term is a
// hit, and the only way to keep one is an exemption above with a sentence.
var goCensusSkipFiles = map[string]bool{
	"internal/tools/collect_docs_gosource_census_test.go": true,
	"internal/tools/collect_docs_contract_test.go":        true,
}

// TestGoSources_CarryNoRetiredExecVocabulary is the census's other half: the
// package documentation and comments that ship to the mirror alongside the
// guides. It found the graphtypecrud package doc still calling the record "the
// external collector binary" a full review round after the artifact was deleted,
// in the very package whose validation the change rewrote.
//
// IT WALKS THE WHOLE CLIENT MODULE, and the widening is the fix for a measured
// miss rather than thoroughness for its own sake. The first version named four
// packages; a review then found surviving sites in internal/engine,
// internal/graphsel and internal/linker, none of which any package list had
// thought to name. A list of packages is a guess about where the next defect
// will be. The module root is not.
//
// IT WALKS TEST FILES TOO, which the first version did not. Several of the sites
// that review found were in _test.go files — a parity test's arm inventory, a
// fake handler's doc comment, a fixture constant's comment — and a gate that
// cannot see them certifies half the tree.
//
// THE MODULE BOUNDARY IS REAL AND IS NOT PAPERED OVER. cmd/knowledge-server is a
// different module; a walk into it from here would read files outside this
// package's module root, which `go test`'s cache key silently drops, so a fixed
// site there would be served a cached pass. The server's own census lives in the
// server module and restates this vocabulary, because the two modules cannot
// import each other.
func TestGoSources_CarryNoRetiredExecVocabulary(t *testing.T) {
	// THE ROOT IS ANCHORED BEFORE THE WALK, and this assertion exists because a
	// mistyped root does not fail cleanly — it fails MISLEADINGLY. Both the skip
	// set and the exemption map are keyed on paths relative to this root, so a
	// wrong root stops matching every key, un-skips the two vocabulary-defining
	// files, and buries the real known-positive under one self-hit per term.
	// Measured: pointing the root at "." produced thirty-odd hits and never
	// reached the scanned-count floor below. Anchoring on the module's own
	// go.mod turns that into one sentence naming the cause.
	root, err := os.OpenRoot(goCensusRoot)
	require.NoErrorf(t, err, "goCensusRoot %q must be the client module root — it is what every exemption and skip key is relative to", goCensusRoot)
	defer func() { _ = root.Close() }()
	modfile, err := fs.ReadFile(root.FS(), "go.mod")
	require.NoErrorf(t, err, "goCensusRoot %q must be the client module root — it is what every exemption and skip key is relative to", goCensusRoot)
	require.Containsf(t, string(modfile), "module github.com/fulminate-io/knowledge-mcp",
		"goCensusRoot %q resolved to a go.mod that is not the client module's", goCensusRoot)

	// THE WALK IS ROOT-SCOPED, through os.Root rather than through raw paths.
	// A filepath.WalkDir callback that reads with os.ReadFile(path) re-resolves
	// the name it was handed, so a symlink swapped in between the walk's stat and
	// the read is followed out of the tree — gosec G122, and a real property of
	// a walk over a directory anything else can write. os.Root refuses a
	// traversal outside itself, and its fs.FS hands the callback a path already
	// relative to the root with forward slashes, which is exactly the spelling
	// the exemption map and the skip set are keyed on. The root also makes the
	// anchor above and the walk read the SAME directory handle.
	scanned := 0
	rootFS := root.FS()
	err = fs.WalkDir(rootFS, ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if goCensusSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		if goCensusSkipFiles[rel] {
			return nil
		}
		body, rerr := fs.ReadFile(rootFS, rel)
		if rerr != nil {
			return rerr
		}
		scanned++
		text := string(body)
		for _, term := range retiredExecGoVocabulary {
			if !strings.Contains(text, term) {
				continue
			}
			if reason, exempt := retiredVocabularyExemptions[rel+":"+term]; exempt {
				t.Logf("exempt: %s carries %q — %s", rel, term, reason)
				continue
			}
			t.Errorf("%s still names a retired artifact: it contains %q. In a comment that misleads the next "+
				"author; in a live string it misleads every caller, who follows the advice and is refused. "+
				"This tree ships to the public mirror either way", rel, term)
		}
		return nil
	})
	require.NoError(t, err, "the census walk must complete; a failed walk makes every absence above vacuous")

	// KNOWN POSITIVE: the walk read the real module. The floor sits well under
	// the real count so an ordinary package addition or removal never trips it.
	require.Greater(t, scanned, 500, "the census scanned %d Go files under %s, which is too few to be the client module", scanned, goCensusRoot)
}
