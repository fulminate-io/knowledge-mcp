// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_daemon_route_census_test.go — THE CLASS IS COUNTED FROM THE TYPE
// CHECKER.
//
// WHY THIS FILE EXISTS. Four rounds of this work enumerated the routed face's
// failure arms with an instrument one level coarser than the language: a hand
// list that missed seven arms, a second hand list that missed two more, a walk
// over return statements that was silent on five refusal shapes, then a walk
// over error CONSTRUCTIONS whose idea of a construction was a two-name
// whitelist — so a package-local constructor, a composite literal of a local
// error type, a sentinel returned from a second site and a second import alias
// of fmt each added a refusal in silence. Each miss was a refusal that could be
// deleted with the whole suite still green.
//
// So the enumeration is no longer a spelling. check_daemon_route.go is
// type-checked in its package (the enumeration lives in
// check_daemon_route_census_types_test.go, the keying in
// check_daemon_route_census_keys_test.go, the package load in
// check_daemon_route_census_load_test.go), and the site set it finds must equal
// the declared table below: one row per site, each reachable row naming the test
// that reds when the site stops refusing, each unreachable row saying why nothing
// can reach it.
//
// WHAT THIS CENSUS COVERS, stated so a reader can check it rather than take it.
// It governs ONE FILE, check_daemon_route.go, and it covers exactly:
//
//   - RESULTS, POSITIONALLY. Every return operand that fills a result whose type
//     IMPLEMENTS error and is not nil, with a tuple-valued single operand
//     unpacked against the signature. Implements, not spelled `error`: a result
//     typed as an interface embedding error, or as a concrete type carrying an
//     Error method, is covered on the same terms.
//   - WRITES INTO A NAMED ERROR RESULT, BY STATEMENT FORM. An assignment whose
//     left-hand side NAMES the result is keyed by its right-hand side, which is
//     how a deferred func literal refuses. Every other statement form that can
//     write such a result — a range assignment into it, that same range inside a
//     deferred literal, a write through a pointer to it, and a write through a
//     slice or map holding its address — is REFUSED BY LINE rather than keyed,
//     because none of them carries a single construction to name.
//   - KEYS BY CONSTRUCTION. fmt.Errorf and errors.New by their static message;
//     any other call by the function go/types says it reaches; a composite
//     literal by its type; a package-level var as a sentinel; a local by the one
//     expression its function assigns into it, one hop.
//   - REFUSALS BY LINE for everything it cannot key: a message that is not a
//     static string literal; a call into the errors package that is not
//     errors.New; a call whose callee it cannot name; an error arriving from a
//     PARAMETER or a receiver, built outside this file; a local the file assigns
//     from several places or from none; and any expression outside the shapes
//     above. Each refusal fails this test, because a dropped site is a refusal
//     that can be deleted with the suite green.
//
// AND IT CLAIMS NOTHING BEYOND THAT. It does not say a refusal is correct, that
// its message is good, or anything at all about another file. Two arms of its own
// machinery are observed by rows here rather than by argument: the classify
// fallthrough by a site whose expression kind the switch does not name, and the
// package loader's fail-loud import by
// TestRefusalCensusRefusesAnImportItsPackageLoadDoesNotHold.
//
// WHAT MAKES IT MORE THAN A TABLE AGREEING WITH ITSELF. Four couplings, each
// against something other than the table: the site set comes from go/types over
// the file; the reachable/unreachable split is DERIVED from the code, and
// derived by IDENTITY rather than adjacency — a site counts as unreachable only
// when it carries the very error a json.Marshal assigned, inside the
// `if err != nil` that tests that same variable, so a dead marshal cannot
// launder a drivable refusal; the marshaled VALUE decides whether the code alone
// may be believed, and a marshal of any identifier may not — its row must name
// the test that proves what can be in it; and every test a row names is looked
// up in this package's own test files.

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// routeSourceFile is the one file this census governs.
const routeSourceFile = "check_daemon_route.go"

// refusalRow declares one refusal site of routeSourceFile.
//
// construction is the site's identity and is spelled by the enumeration. For a
// refusal this file BUILDS it is the constructor's first argument exactly as the
// source writes it — the fmt.Errorf format, or the text of an errors.New
// sentinel — which is unique per arm and is the text a reader sees. A substring
// key would not do, because the tool-result arm's whole format is a strict
// prefix of the envelope-error arm's. For a refusal this file HANDS BACK rather
// than builds, it is a sentence naming what produced the value.
//
// Exactly one of observedBy and unreachableBecause is set. observedBy names the
// test that goes red when this site stops refusing; unreachableBecause says why
// the site cannot be driven, and is admissible only for a site the derivation
// independently agrees carries a json.Marshal's error.
//
// valueProvenanceBy is set only, and required only, when that Marshal encodes a
// value carrying an IDENTIFIER — a bare name, or a composite literal any leaf of
// which reads one. The code alone cannot then say the site is unreachable,
// because the answer is what was put in that name, so the row must name the test
// that pins it. A literal every leaf of which is a constant carries nothing a
// caller could have placed there, and owes no such test.
type refusalRow struct {
	construction       string
	observedBy         string
	unreachableBecause string
	valueProvenanceBy  string
}

// refusalRows is the declared class. Adding a refusal to check_daemon_route.go
// without adding a row here fails TestRefusalReturnCensus.
var refusalRows = []refusalRow{
	{
		construction: "check run: the knowledge daemon is not reachable at %s, so the checks corpus was not read and no scan ran (%w) — " +
			"start it with `knowledge serve`, or name a running daemon with --http-port",
		observedBy: "TestCheckRun_RefusesWhenTheDaemonIsUnreachable",
	},
	{
		construction: "check run: the knowledge daemon at %s refused the MCP handshake with HTTP %d, so the checks corpus was not read",
		observedBy:   "TestCheckRun_RefusesARefusedHandshake",
	},
	{
		construction: "check run: the knowledge daemon at %s answered the handshake without an %s header, so no session could be opened and the checks corpus was not read",
		observedBy:   "TestCheckRun_RefusesAHandshakeThatMintsNoSession",
	},
	{
		construction: "check run: encode the %s arguments: %w",
		unreachableBecause: "the argument map is built by this package and holds only string, []string and bool, " +
			"every one of which json.Marshal encodes without error",
		valueProvenanceBy: "TestCheckRunToolArgsCarryOnlyEncodableValues",
	},
	{
		construction: "check run: encode the %s call: %w",
		unreachableBecause: "CallToolParams holds a tool name and the json.RawMessage produced by the encode above, " +
			"and a RawMessage that already parsed re-encodes without error",
		valueProvenanceBy: "TestCheckRunCallParamsCarryOnlyEncodableValues",
	},
	{
		construction: "check run: encode the %s request: %w",
		unreachableBecause: "JSONRPCRequest holds two strings and two json.RawMessages, all of them built directly above " +
			"from values that already encoded",
		valueProvenanceBy: "TestCheckRunRequestEnvelopeCarriesOnlyEncodableValues",
	},
	{
		construction: "check run: the %s call to the knowledge daemon at %s did not complete, so no verdict was produced: %w",
		observedBy:   "TestCheckRun_RefusesACallThatDidNotComplete",
	},
	{
		construction: "check run: the knowledge daemon at %s answered the %s call with HTTP %d, so no verdict was produced",
		observedBy:   "TestCheckRun_RefusesANon2xxToolCall",
	},
	{
		construction: "check run: read the %s response from %s: %w",
		observedBy:   "TestCheckRun_RefusesAResponseItCannotFinishReading",
	},
	{
		construction: "check run: the knowledge daemon at %s answered the %s call with a body this client could not decode: %w",
		observedBy:   "TestCheckRun_RefusesABodyItCannotDecode",
	},
	{
		construction: "check run: the knowledge daemon at %s answered the %s call with JSON-RPC id %s, but this client sent id %s, so the body is not this call's answer and no verdict was produced",
		observedBy:   "TestCheckRun_RefusesAnAnswerCarryingAnotherRequestsID",
	},
	{
		construction: "check run: the knowledge daemon at %s refused the %s call: %s (code %d)",
		observedBy:   "TestCheckRun_RefusesAnEnvelopeCarryingAnRPCError",
	},
	{
		construction: "check run: the knowledge daemon at %s answered the %s call with no content, so there is no verdict to report",
		observedBy:   "TestCheckRun_RefusesAnAnswerWithNoContent",
	},
	{
		construction: "check run: the knowledge daemon at %s refused the %s call: %s",
		observedBy:   "TestCheckRun_RefusesAToolResultFlaggedAsAnError",
	},

	// THE SIX SITES THAT HAND BACK AN ERROR THIS FILE DID NOT BUILD. Every
	// instrument before the type checker was blind to these: they construct
	// nothing, so a walk looking for constructions walked straight past them,
	// and any one could have been swallowed with the suite green. They are the
	// class the go/types enumeration added, and each names the test that reds
	// when the error stops being handed back.
	{
		construction: "the error openDaemonSession returns",
		observedBy:   "TestCheckRun_RefusesARefusedHandshake",
	},
	{
		construction: "the error callDaemonTool returns",
		observedBy:   "TestCheckRun_RefusesANon2xxToolCall",
	},
	{
		construction: "the error decodeToolText returns",
		observedBy:   "TestCheckRun_RefusesABodyItCannotDecode",
	},
	{
		construction: "the error http.NewRequestWithContext returns",
		observedBy:   "TestPostDaemon_RefusesAnEndpointItCannotBuildARequestFor",
	},
	{
		construction: "the error (*http.Client).Do returns",
		observedBy:   "TestCheckRun_RefusesACallThatDidNotComplete",
	},
	{
		construction: "the error (*net.Dialer).DialContext returns",
		observedBy:   "TestNewDaemonMCPClient_KeepsTheDialFailureOnTheChain",
	},
}

// TestRefusalReturnCensus is the keeper of the failure-arm class.
func TestRefusalReturnCensus(t *testing.T) {
	complaints, arms, err := auditRefusalCensus(nil, refusalRows, packageTestFuncs(t))
	require.NoError(t, err, "the census must type-check %s in its package, or it counts nothing", routeSourceFile)
	require.NotEmpty(t, arms, "control: the enumeration must find refusal sites at all, or every assertion below passes vacuously")

	for _, complaint := range complaints {
		t.Error(complaint)
	}

	var guarded int
	for _, arm := range arms {
		if arm.guard.guarded {
			guarded++
		}
	}
	t.Logf("%s: %d refusal sites — %d carrying a json.Marshal's error, %d drivable",
		routeSourceFile, len(arms), guarded, len(arms)-guarded)
}

// auditRefusalCensus enumerates the routing file's refusal sites, compares them
// against rows, and returns one complaint per disagreement. An empty result is
// the census passing. src stands in for the file on disk when it is non-nil.
//
// It is a function over (src, rows, tests) rather than a body inside the test so
// the shape tables can point the census at a mutated copy of the source and
// assert that every refusal shape is caught.
func auditRefusalCensus(src []byte, rows []refusalRow, tests map[string]bool) ([]string, []censusArm, error) {
	arms, refused, err := enumerateRefusals(src)
	if err != nil {
		return nil, nil, err
	}
	complaints := append([]string(nil), refused...)

	inSource := map[string]censusArm{}
	for _, arm := range arms {
		if prev, dup := inSource[arm.key]; dup {
			complaints = append(complaints, fmt.Sprintf(
				"two refusal sites carry one construction (%s:%d and %s:%d, both %s), so the census cannot tell them apart — "+
					"give one of them its own words, or fold the two sites into one",
				routeSourceFile, prev.line, routeSourceFile, arm.line, arm.kind))
			continue
		}
		inSource[arm.key] = arm
	}

	declared := map[string]refusalRow{}
	for _, row := range rows {
		complaints = append(complaints, rowShapeComplaints(row, declared, tests)...)
		declared[row.construction] = row
	}

	complaints = append(complaints, setEqualityComplaints(inSource, declared)...)
	for _, key := range sortedArms(inSource) {
		complaints = append(complaints, reachabilityComplaints(inSource[key], declared[key])...)
	}

	// THE ARITHMETIC, said out loud. It is a restatement of the set comparison
	// above rather than an independent check — every way to make the counts
	// disagree also breaks the sets or collides two constructions — and it is
	// here so a reader sees the number the census is standing behind.
	if len(arms) != len(rows) {
		complaints = append(complaints, fmt.Sprintf(
			"%s holds %d refusal sites but the census declares %d rows: every site is either observed by a named test or named as unreachable",
			routeSourceFile, len(arms), len(rows)))
	}
	return complaints, arms, nil
}

// rowShapeComplaints checks one row against the row contract, before any
// comparison with the source.
func rowShapeComplaints(row refusalRow, seen map[string]refusalRow, tests map[string]bool) []string {
	var complaints []string
	if (row.observedBy == "") == (row.unreachableBecause == "") {
		complaints = append(complaints, fmt.Sprintf(
			"row %q must set exactly one of observedBy and unreachableBecause", row.construction))
	}
	if row.valueProvenanceBy != "" && row.unreachableBecause == "" {
		complaints = append(complaints, fmt.Sprintf(
			"row %q names a value-provenance test but does not claim to be unreachable", row.construction))
	}
	if _, dup := seen[row.construction]; dup {
		complaints = append(complaints, fmt.Sprintf("row %q is declared twice", row.construction))
	}
	for _, named := range []struct{ field, name string }{
		{"observedBy", row.observedBy},
		{"valueProvenanceBy", row.valueProvenanceBy},
	} {
		if named.name != "" && !tests[named.name] {
			complaints = append(complaints, fmt.Sprintf(
				"row %q names %s %q, which is not declared in this package", row.construction, named.field, named.name))
		}
	}
	return complaints
}

// setEqualityComplaints reports sites with no row and rows with no site.
func setEqualityComplaints(inSource map[string]censusArm, declared map[string]refusalRow) []string {
	var complaints []string
	for _, key := range sortedArms(inSource) {
		if _, ok := declared[key]; ok {
			continue
		}
		arm := inSource[key]
		complaints = append(complaints, fmt.Sprintf(
			"%s:%d in %s hands back an error this census does not declare (%s, from %s):\n\t%q\n"+
				"Add a row to refusalRows naming the test that observes it, or naming why nothing can reach it. "+
				"An undeclared site is a refusal that can be deleted with the suite green.",
			routeSourceFile, arm.line, arm.fn, arm.kind, arm.construction, key))
	}
	for _, key := range sortedRows(declared) {
		if _, ok := inSource[key]; !ok {
			complaints = append(complaints, fmt.Sprintf(
				"refusalRows declares a refusal that %s no longer contains:\n\t%q\nDelete the row, or restore the refusal.",
				routeSourceFile, key))
		}
	}
	return complaints
}

// reachabilityComplaints checks one site's DERIVED reachability against what its
// row claims, in both directions, and checks the provenance of the value the
// guarding json.Marshal encoded.
func reachabilityComplaints(arm censusArm, row refusalRow) []string {
	if row.construction == "" {
		return nil // undeclared: already reported by setEqualityComplaints.
	}
	if !arm.guard.guarded {
		if row.unreachableBecause == "" {
			return nil
		}
		return []string{fmt.Sprintf(
			"%s:%d does not carry the error of a json.Marshal, so it is drivable, but its row calls it unreachable: %q",
			routeSourceFile, arm.line, row.unreachableBecause)}
	}
	if row.unreachableBecause == "" {
		return []string{fmt.Sprintf(
			"%s:%d carries the error of a json.Marshal of %s, so nothing can drive it, but its row claims the test %q observes it",
			routeSourceFile, arm.line, arm.guard.valueText, row.observedBy)}
	}
	switch arm.guard.valueKind {
	case marshalValueIdentifier, marshalValueOpenLiteral:
		if row.valueProvenanceBy == "" {
			return []string{fmt.Sprintf(
				"%s:%d is guarded by a json.Marshal of %s, which is %s — the code alone cannot say the site is unreachable, "+
					"because the answer is what was put in that name, whatever built it. Name the test that pins that in valueProvenanceBy.",
				routeSourceFile, arm.line, arm.guard.valueText, arm.guard.valueKind)}
		}
	case marshalValueUnknown:
		return []string{fmt.Sprintf(
			"%s:%d is guarded by a json.Marshal of %s, which is %s, so this census cannot agree that the site is unreachable",
			routeSourceFile, arm.line, arm.guard.valueText, marshalValueUnknown)}
	default:
		if row.valueProvenanceBy != "" {
			return []string{fmt.Sprintf(
				"%s:%d is guarded by a json.Marshal of %s, which is %s, so no caller-provenance test is needed: drop valueProvenanceBy",
				routeSourceFile, arm.line, arm.guard.valueText, arm.guard.valueKind)}
		}
	}
	return nil
}

// sortedArms renders a site map's keys in a stable order, so a failing run names
// the same offender first every time.
func sortedArms(m map[string]censusArm) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// sortedRows renders a row map's keys in a stable order.
func sortedRows(m map[string]refusalRow) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
