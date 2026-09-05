// SPDX-License-Identifier: Apache-2.0

// vocabulary.go is the SINGLE authoritative declaration for every token more
// than one part of this package consumes: the analyzer's name, the one Extra
// key it reads, the two render ceilings, the three finding-metadata keys and
// the five locked finding titles. Everything else in the family CITES these
// constants and none of them re-spells a value.
//
// NOTHING PRIVATE SHIPS IN SOURCE. No knowledge-graph node id and no internal
// tracker id appears in any Go file of this package. State a contract's
// SUBSTANCE — which key, which values, which behavior — and name any owning
// work IN WORDS. The repo's pre-commit OSS-leak gate enforces this with three
// detectors (a tracker-id form, a type-word-next-to-hex form, and a bare 32-hex
// form), and its rules are documented at scripts/oss-leak-check.sh:10-13.

package corpusscan

import (
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
)

// The locked identifiers. AnalyzerName is both the value passed as
// query(algorithm:...) and the value every emitted Finding carries in
// Algorithm; foundation.Register panics on a duplicate registration
// (foundation/registry.go:37), so a name collision is a boot panic rather than
// a silent shadow.
const (
	// AnalyzerName is the corpus scanner's stable registry identifier.
	AnalyzerName = "corpus_scan"
	// ExtraKeyChecks is a Request.Extra key this analyzer reads: a
	// comma-separated list of check node ids selecting a subset of the corpus.
	// Absent means every check in the corpus. Node ids passed as a RUNTIME
	// ARGUMENT are caller data; the prohibition above is about ids written into
	// source.
	ExtraKeyChecks = "checks"
	// ExtraKeyIncludeTests is the run-wide test-file knob: "true" widens every
	// ast check's walk to this language's test files, "false" is the documented
	// default spelled out, and ABSENT means the caller did not ask.
	//
	// THE THREE STATES ARE NOT TWO. An omitted key misleads nobody and is legal
	// for every language; an EXPLICIT value for a language ast carries no
	// test-file convention for is refused, because there the flag would be a
	// control that decides nothing.
	ExtraKeyIncludeTests = "include_tests"
	// MaxFindingsPerCheck caps how many match findings a single check renders
	// before the per-check truncation notice replaces the rest.
	MaxFindingsPerCheck = 50
	// MaxFindingsTotal caps how many match findings a whole run renders before
	// the run-level truncation notice replaces the rest. Both ceilings are
	// DERIVED rather than chosen; the derivation is doc.go's render-ceiling
	// paragraph.
	MaxFindingsTotal = 100
)

// The finding metadata keys, adopted verbatim from the calibration lane at that
// planner's request because its scoring pass joins on them.
//
// MetaKeyCheckID's value is ALWAYS corpus.Check.ID. The contract populates
// Check.ID from the source node's id, so this package never re-reads the node
// for identity: Evidence[0], every refusal title and this metadata key all read
// Check.ID. There is no check-identifier or check-name key in the check
// vocabulary and there never will be — the display name is the source node's
// SymbolName.
//
// This package is the PRODUCER of these three spellings: no other finding
// composite literal under cmd/knowledge/internal/topology populates a file or
// line metadata key, so nothing in the tree conflicts.
const (
	// MetaKeyFile is the repo-relative path of the flagged site.
	MetaKeyFile = "file"
	// MetaKeyLine is the 1-based line of the flagged site, as a decimal string.
	// Emitting it is MANDATORY for ast_pattern findings — Finding.Evidence
	// carries node ids of the form path/file.go:Symbol, which yield a file but
	// never a line. Graph-assertion findings are file-granular and OMIT this key
	// entirely; an absent key is honest where a zero would be a false row in the
	// calibration join.
	MetaKeyLine = "line"
	// MetaKeyCheckID is the corpus check's identity.
	MetaKeyCheckID = "check_id"
)

// MetricTestFilesScanned is the metric key the test-file disclosure carries its
// count on. It is a CONSTANT because two packages read it: this one's fold and
// the disclosure that emits it. A consumer keying on a hand-typed copy is how a
// counter comes to read zero everywhere it is displayed.
const MetricTestFilesScanned = "test_files_scanned"

// The locked finding titles. Each is consumed by more than one part of this
// package — a producer emits it and a test asserts on it — so all five are
// declared once here and cited everywhere else. THE TRAILING SPACE IS
// LOAD-BEARING on the two prefixes that carry one: an id is concatenated
// directly after it.
const (
	// RefusalPrefixUnvalidated titles every per-check refusal in the gate's
	// states (a) contract malformed, (b) fixture validation failed and
	// (c) unexecutable check type.
	RefusalPrefixUnvalidated = "corpus_scan: unvalidated check "
	// RefusalPrefixEnvironment titles the gate's state (d): either the run's
	// os.MkdirTemp precondition probe failing, or an error satisfying
	// errors.Is(err, corpus.ErrFixtureMaterialization).
	RefusalPrefixEnvironment = "corpus_scan: fixture materialization failed — "
	// TruncationPrefixCheck titles the per-check ceiling notice.
	TruncationPrefixCheck = "corpus_scan: truncated check "
	// TruncationTitleRun titles the run-level ceiling notice. No trailing space:
	// nothing is concatenated after it.
	TruncationTitleRun = "corpus_scan: run truncated"
	// DisclosureTitleLLMOnly titles the ONE informational finding disclosing the
	// accepted-llm_only lane. No trailing space: it describes a set rather than
	// one check.
	DisclosureTitleLLMOnly = "corpus_scan: llm_only checks not executed"
	// DisclosureTitleTestFiles titles the ONE informational finding reporting
	// that this run's walk reached test files. No trailing space: it describes a
	// run rather than one check.
	//
	// A NEW TITLE IS NOT FREE. ClassifyRun's default arm counts anything it does
	// not recognize as a FLAGGED SITE, so a disclosure whose title is missing
	// from that switch adds one to sites_flagged on every run, makes Clean()
	// false, and renders a clean corpus as FLAGGED with a non-zero exit.
	DisclosureTitleTestFiles = "corpus_scan: test files scanned"
)

// contractMetaKeys is the check-node metadata vocabulary this analyzer's corpus
// is written in, mapping the WIRE SPELLING a corpus author puts on a check
// node to the contract constant that owns it.
//
// It is a transcription for the reader AND a live binding, which is why it is a
// map rather than a comment: TestVocabulary_ContractKeysMatchTheContract walks
// every pair and fails when one stops agreeing, so a rename inside the contract
// package cannot leave this file quietly describing a vocabulary the parser no
// longer reads. The check nodes themselves are kgtypes.NodeFinding in a
// SINGLE CHECKS graph, narrowed to one language by a metadata predicate on the
// contract's own `language` key, and their fixtures are
// kgtypes.NodeExample in that SAME graph, resolved BY NODE ID FROM METADATA —
// never by edge, and never across a graph boundary.
//
// THIS PACKAGE NEVER READS THESE KEYS DIRECTLY. Every decode goes through
// corpus.ParseCheck, whose four-row return table this family handles in full.
// The map exists so an operator error can enumerate the vocabulary and so the
// drift test has something to walk.
var contractMetaKeys = map[string]string{
	"applies_to_tests":   corpus.MetaAppliesToTests,
	"check_type":         corpus.MetaCheckType,
	"severity":           corpus.MetaSeverity,
	"language":           corpus.MetaLanguage,
	"dsl_pattern":        corpus.MetaDSLPattern,
	"check_where":        corpus.MetaCheckWhere,
	"check_fixture_bad":  corpus.MetaFixtureBad,
	"check_fixture_good": corpus.MetaFixtureGood,
	"llm_only":           corpus.MetaLLMOnly,
}

// checkVocabulary renders the check-node metadata keys, sorted, for an error
// message that has to tell an operator what makes a node a check.
// Derived from contractMetaKeys at call time so the message can never enumerate
// a vocabulary the parser does not actually read.
func checkVocabulary() string {
	keys := make([]string, 0, len(contractMetaKeys))
	for k := range contractMetaKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// THE CHANNEL DECISION, stated once here so nothing else in this family
// re-decides it. Two of the three parameters already have first-class routed
// channels on foundation.Request and this analyzer uses those rather than
// inventing Extra keys for them:
//
//	CORPUS LANGUAGE FILTER = req.Language. armTopology already consumes
//	`language` and runLocalTopology threads it into Request.Language. The corpus
//	language and the ast walk language are the SAME value, so one field carries
//	it and there is no second stamper. It does NOT select a graph: checks is a
//	singleton, so the value becomes a metadata predicate over the contract's
//	`language` key, whose vocabulary is the treesitter.Language constant (LangGo
//	is the literal "go", collector/treesitter/language.go:50) — the same slug
//	practice graphs are NAMED by, used here as a label instead.
//
//	SCOPE = req.PathPrefix, the field this changeset adds and routes behind a
//	declared honoring-analyzer allowlist.
//
//	CHECK SUBSET = Extra[ExtraKeyChecks].
//
//	TEST-FILE SCOPE = Extra[ExtraKeyIncludeTests], the run-wide knob, folded
//	per check with the check node's own applies_to_tests declaration. It is an
//	Extra key rather than a Request field because foundation.Request is ONE
//	shape shared by every analyzer in the suite: a field there that this
//	analyzer alone honors is exactly what PathPrefix's honoring allowlist in
//	the topology dispatcher exists to prevent, and the topology dispatcher
//	forwards Extra verbatim (tools/intercept_topology.go), so this knob is
//	honored on that face too rather than dropped there — which a first-class
//	field could not have been without an allowlist entry. It is parsed
//	strictly, like the check subset, and never defaulted.
//
// THIS FILE DECLARES NO FLOW-EDGE NAMES, DELIBERATELY. The flow-fact edge
// constants are not landed in kgtypes, and declaring an unlanded vocabulary
// here would contradict this changeset's own rule against designing against
// unlanded edges. The flow vocabulary belongs where it is consumed — the flow
// executor, authored once the collector artifact lands.
//
// THERE IS NO FIXTURE-VALIDATION MARKER, also deliberately. The contract
// persists NOTHING about a passed validation, because a stamp would create a
// drift class where editing a fixture's Content leaves the stamp silently
// over-claiming. Every check is re-validated on every run by CALLING the
// validator; see gate.go.
