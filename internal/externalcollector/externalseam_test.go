// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// externalseam_test.go — THE SEAM that lets the provider-dialing contract tests
// run against an EXTERNAL collector command instead of this test binary.
//
// WHY IT EXISTS. The stdio provider these tests drive is this test binary,
// re-execed (see stubprovider_test.go). That proves the contract against a Go
// stub and nothing else, so a collector written in another language has no way
// to be held to the same contract: its author can read the tests and reimplement
// their assertions, which is a copy that drifts. With the variable below set to
// a command, the ELEVEN tests that actually dial a provider dial THAT command,
// and the contract is proven against it directly.
//
// THE SCOPE IS THE DIALING TESTS, AND IT IS NOT THE SAME AS "every test that
// builds a stdio registration". Six further tests share the same construction
// point and
// are ABOUT the Go harness — the producer identity stamps, the environment
// baseline, the http transport's command, the uncapped result — so they keep the
// re-execed Go stub whatever the variable says. dialingTests and
// stdioTestsKeepingTheGoStub are the two halves, and
// TestExternalCollectorSeam_TheTwoListsCoverEveryTestThatBuildsAStdioStub reads
// the package's own source to prove no third case exists.
//
// WHAT AN EXTERNAL COMMAND HAS TO BE. Not a port's README sample collector: the
// dialing tests assert the Go stub's exact payloads and strings (ISSUE-1..3 with an
// absent-versus-present-and-empty boundary, the collect_graph tool name, the
// literal refusal text, the misspelled "summry" key, two process exits, and a
// result over the former 64 MiB cap), and one of them asserts the stdio result
// renders byte-equal to the in-process http stub's. The artifact is a test-only
// CONFORMANCE STUB implementing every stub mode, which this ticket does not
// ship: each port supplies its own and points this seam at it.

const (
	// externalCollectorEnv carries the external collector command, with optional
	// arguments separated by spaces. Unset, this package is byte-identical to
	// what it was before the seam existed.
	externalCollectorEnv = "FUL1819_EXTERNAL_COLLECTOR"
	// unemulatedModesEnv names, comma-separated, the stub modes the external
	// collector does not emulate. Every test that would drive one is SKIPPED BY
	// NAME with the reason printed — never silently, and never by an assertion
	// quietly relaxed. A name that is not one of the sixteen modes is refused.
	unemulatedModesEnv = "FUL1819_UNEMULATED_MODES"
	// seamNestedEnv marks a child this package spawned to observe a whole run of
	// the eleven. The tests that spawn such a child skip inside it, so a nested
	// run can never spawn its own.
	seamNestedEnv = "FUL1819_NESTED"
	// runIDEnv carries a value that changes every invocation and is read for
	// nothing else.
	//
	// IT EXISTS BECAUSE A CACHED RUN SPAWNS NOTHING. Go's test cache serves an
	// identical `go test` from a stored result, so a second invocation of the
	// runner against the same collector replays its per-test report having dialed
	// nobody — measured: `ok … 0.736s` then `ok … (cached)` with no child process
	// at all. Every assertion the runner makes, including the one that exists
	// because a -run expression matching nothing exits 0, is evaluated only when
	// the test actually runs. The cache keys on the environment variables a test
	// READS, so reading a run-unique value here moves the key by construction and
	// needs no forcing flag. `make test-collector-contract` sets it; a caller
	// driving the runner by hand may leave it unset and take the cache.
	runIDEnv = "FUL1819_RUN_ID"
)

// dialingTests are the tests that dial a provider over stdio, and the whole
// subject of the seam. They were eleven when the seam was written and are
// fifteen with the describe contract's four. THE LIST IS THE ONE DECLARATION: the seam's scoping
// predicate, the runner's -run filter and the runner's completeness assertion all
// read it, so no second copy can drift from it.
var dialingTests = []string{
	"TestRunMCP_ConformingProvider_BothTransports",
	"TestRunMCP_SchemaGateRefusals",
	"TestVerifyRegistration_RefusesAtRegisterToo",
	"TestRunMCP_CompletenessAssertion",
	"TestRunMCP_StdioEnvironmentIsTheEntrysBlock",
	"TestRunMCP_StdioNameAbsentFromTheBlockIsAbsentInTheChild",
	"TestRunMCP_StdioBlockValueBeatsTheDaemonsOwn",
	"TestRunMCP_StdioEmptyValueArrivesPresentAndEmpty",
	"TestRunMCP_DanglingEdgeIsAdmitted",
	"TestRunMCP_EmptyGraphIsAdmitted",
	"TestRunMCP_ProviderRuntimeFailures",
	// THE DESCRIBE CONTRACT'S OWN DIALING TESTS, which landed with the describe
	// tool after this list was first written. They dial a provider over stdio and
	// assert what it SERVES — a required describe tool, a declaration that
	// satisfies the schema it advertised, and the two call failures only a dial
	// can reach — so they are the port's to satisfy exactly as the eleven above
	// are, and a port's CI leg calls this runner to prove it. The package's own
	// source census is what made this a decision rather than an omission: it
	// named all four the moment they landed.
	"TestRunMCP_RefusesAProviderThatStoppedServingDescribe",
	"TestRunMCP_RefusesADescribeToolWhoseSchemaFallsShort",
	"TestVerifyRegistration_RefusesTheTwoDescribeCallFailures",
	"TestVerifyRegistration_ReturnsTheProvidersDeclaration",
}

// stdioTestsKeepingTheGoStub are the six tests that build a stdio registration
// and are NOT redirected. Each is about the Go harness itself rather than about
// the contract a provider must satisfy: two stamp the producer identity of the
// re-execed child, two are the environment baseline whose subject is what the
// spawn does NOT carry, one asserts the http transport builds no command at all,
// and one feeds 68 MiB back through the pipe.
var stdioTestsKeepingTheGoStub = []string{
	"TestRunMCP_StampsBothProducerIdentities",
	"TestRunMCP_StampsAreStableAcrossCollects",
	"TestRunMCP_StdioChildSeesOnlyTheBlocksNames",
	"TestRunMCP_TransportFailureIsLoudNotAnEmptyCompleteGraph",
	"TestProviderTransport_HTTPBuildsNoCommand",
	"TestRunMCP_AcceptsAResultOverTheFormerCapOverStdio",
}

// stubModes is the mode vocabulary the skip list is checked against, and it is
// exactly the stubMode* constants the const block declares — sixteen when the
// seam was written, twenty with the describe contract's four.
// TestExternalCollectorSeam_TheModeVocabularyIsTheConstBlock reads the source to
// prove this list and that block agree, so a seventeenth mode cannot land here
// unnoticed.
var stubModes = []string{
	stubModeConforming,
	stubModeEnvReport,
	stubModeNoOutputSchema,
	stubModeBadOutputSchema,
	stubModeBadInputSchema,
	stubModeBreaksOwnWord,
	stubModeToolError,
	stubModeEmptyNodeType,
	stubModeUnknownField,
	stubModeDanglingEdge,
	stubModeIncompleteWalk,
	stubModeEmptyGraph,
	stubModeOverFormerCap,
	stubModeExitBeforeHandshake,
	stubModeExitMidSession,
	stubModeNoSuchTool,
	// THE DESCRIBE MODES, which landed with the describe tool. A port's
	// conformance stub serves them like the rest, and the skip list is checked
	// against this vocabulary, so a mode missing here would be refused as a typo.
	stubModeNoDescribe,
	stubModeBadDescribeSchema,
	stubModeBadDeclaration,
	stubModeDescribeError,
}

// topLevelTestName is the test whose body a subtest runs under: t.Name() renders
// a subtest as "Parent/child", and the seam's subject is the parent.
func topLevelTestName(name string) string {
	if before, _, ok := strings.Cut(name, "/"); ok {
		return before
	}
	return name
}

// dialsExternalCollector reports whether the named top-level test is one of the
// eleven the seam redirects.
func dialsExternalCollector(name string) bool {
	return slices.Contains(dialingTests, name)
}

// externalCollector reads the seam variable: the command, its arguments, and
// whether the seam is on at all.
//
// A VARIABLE SET TO WHITESPACE IS AN ERROR rather than a quiet "off". Bad input
// always errors: a CI leg that computed an empty command would otherwise run the
// eleven against the Go stub and report them as proof of its own collector.
func externalCollector() (command string, args []string, set bool, err error) {
	raw, present := os.LookupEnv(externalCollectorEnv)
	if !present {
		return "", nil, false, nil
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return "", nil, false, fmt.Errorf(
			"%s is set to %q, which names no command; unset it to run against the re-execed Go stub",
			externalCollectorEnv, raw)
	}
	return fields[0], fields[1:], true, nil
}

// parseUnemulatedModes reads a comma-separated skip list, refusing any name that
// is not one of the sixteen stub modes. A typo that silently skipped nothing
// would report a collector as conforming on a mode it never served.
func parseUnemulatedModes(list string) (map[string]bool, error) {
	out := map[string]bool{}
	for raw := range strings.SplitSeq(list, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		known := slices.Contains(stubModes, name)
		if !known {
			return nil, fmt.Errorf(
				"%s names %q, which is not one of the %d stub modes (%s)",
				unemulatedModesEnv, name, len(stubModes), strings.Join(stubModes, ", "))
		}
		out[name] = true
	}
	return out, nil
}

// validateSeamEnvironment refuses a seam configuration that cannot mean what it
// says, BEFORE any test runs. TestMain calls it: a skip list naming a mode that
// does not exist, or naming modes with no external collector to skip them for,
// is a misconfiguration whose only quiet outcome is a green run that proved less
// than it claims.
func validateSeamEnvironment() error {
	_, _, set, err := externalCollector()
	if err != nil {
		return err
	}
	raw, present := os.LookupEnv(unemulatedModesEnv)
	if !present {
		return nil
	}
	if _, err := parseUnemulatedModes(raw); err != nil {
		return err
	}
	if !set && strings.TrimSpace(raw) != "" {
		return fmt.Errorf(
			"%s names modes but %s is unset; there is no external collector for those modes to be unemulated BY",
			unemulatedModesEnv, externalCollectorEnv)
	}
	return nil
}

// unemulatedModes is parseUnemulatedModes over the environment.
func unemulatedModes() (map[string]bool, error) {
	return parseUnemulatedModes(os.Getenv(unemulatedModesEnv))
}
