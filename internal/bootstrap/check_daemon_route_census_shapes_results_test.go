// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_daemon_route_census_shapes_results_test.go — THE SHAPES THAT BEAT THE
// RESULT-TYPE IDENTITY TEST AND THE ASSIGNMENT-ONLY WRITER.
//
// The census before this one asked whether a result's type was IDENTICAL to the
// predeclared error, and took a write into a named result only from an
// assignment whose left-hand side was a plain identifier. Both are spellings, and
// both were beaten by ordinary Go: a helper declaring an interface that EMBEDS
// error, or a concrete error type, was no refusal site at all, so an arm inside
// one could be added and deleted with the census green; and a range assignment,
// the same range inside a deferred literal, a write through a pointer to the
// result and a write through an alias holding its address each set a caller's
// error with no assignment statement naming it.
//
// Each is a row here, asserted RED through the same auditRefusalCensus and the
// same in-run control as the shipped table. Two more rows carry what the
// contents-reading provenance rule and the classify fallthrough made observable:
// a caller's value wrapped in a composite literal before the guarded marshal, and
// a refusal whose expression kind the keying switch does not name.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// censusResultShapeCases are the shapes the result-type identity test and the
// assignment-only writer were silent on.
func censusResultShapeCases() []censusShapeCase {
	return []censusShapeCase{
		{
			// W1 OF THE REVIEWER'S PROBE: the arm ADDED. probeFailure embeds
			// error, so a value filling it is an error by every rule but the
			// identity one, and this arm was free to add.
			name: "a refusal inside a helper whose result is an interface embedding error", wasSilent: true,
			want: `hands back an error this census does not declare (a composite literal of an error type, from &probeWideError{`,
			mutate: func(_ *testing.T, src string) string {
				return src + probeWideErrorType + `
func probeWideRefusal(n int) probeFailure {
	if n == 7 {
		return &probeWideError{msg: "check run: probe: a wide-result refusal"}
	}
	return nil
}
`
			},
		},
		{
			// W3 OF THE REVIEWER'S PROBE: the arm DELETED. With the site declared,
			// removing the only arm of the wide-result helper reds.
			//
			// THIS ROW IS A COUPLING, NOT A DISCRIMINATION, and it is marked so:
			// it reds on the identity enumeration too, because there the arm was
			// never a site and the row matched nothing either way. What CHANGED is
			// the row above, where the arm is now counted; this one records that
			// once counted, its deletion is caught by the same set equality that
			// catches every other site's.
			name: "the only arm of a wide-result helper deleted while its row stands", wasSilent: false,
			want: "refusalRows declares a refusal that check_daemon_route.go no longer contains",
			mutate: func(_ *testing.T, src string) string {
				return src + probeWideErrorType + `
func probeWideRefusal(n int) probeFailure {
	return nil
}
`
			},
			rows: func(rows []refusalRow) []refusalRow {
				return append(rows, refusalRow{
					construction: "a probeWideError value",
					observedBy:   "TestCheckRun_RefusesARefusedHandshake",
				})
			},
		},
		{
			// The other half of the same class: a result typed as the CONCRETE
			// error type rather than as an interface.
			name: "a refusal filling a result typed as a concrete error type", wasSilent: true,
			want: `hands back an error this census does not declare (a composite literal of an error type, from &probeRouteError{`,
			mutate: func(_ *testing.T, src string) string {
				return src + probeErrorType + `
func probeConcreteRefusal(n int) *probeRouteError {
	if n == 7 {
		return &probeRouteError{msg: "check run: probe: a concrete-result refusal"}
	}
	return nil
}
`
			},
		},
		{
			name: "a range assignment writing a named error result", wasSilent: true,
			want: "writes an element of errs into the named error result err",
			mutate: func(_ *testing.T, src string) string {
				return src + `
func probeRangeRefusal(errs []error) (err error) {
	for _, err = range errs {
	}
	return
}
`
			},
		},
		{
			name: "a deferred literal ranging into its caller's named error result", wasSilent: true,
			want: "writes an element of errs into the named error result err",
			mutate: func(_ *testing.T, src string) string {
				return src + `
func probeDeferredRangeRefusal(errs []error) (err error) {
	defer func() {
		for _, err = range errs {
		}
	}()
	return nil
}
`
			},
		},
		{
			name: "a write through a pointer to the named error result", wasSilent: true,
			want: "writes into *p, an error this walk cannot resolve to a name",
			mutate: func(_ *testing.T, src string) string {
				return src + `
func probePointerRefusal(n int) (err error) {
	p := &err
	if n == 7 {
		*p = fmt.Errorf("check run: probe: a pointer-write refusal for %d", n)
	}
	return
}
`
			},
		},
		{
			name: "a write through a slice holding the result's address", wasSilent: true,
			want: "writes into *alias[0], an error this walk cannot resolve to a name",
			mutate: func(_ *testing.T, src string) string {
				return src + `
func probeAliasRefusal(n int) (err error) {
	alias := []*error{&err}
	if n == 7 {
		*alias[0] = fmt.Errorf("check run: probe: an alias-write refusal for %d", n)
	}
	return
}
`
			},
		},
		{
			// THE ROUND-5 LAUNDERING. Wrapping the caller's own parameter in a
			// literal at the call site classified the marshaled value as the
			// function's own, so the row could drop its provenance test and go
			// green while the bytes stayed entirely caller-controlled.
			name: "a caller's value wrapped in a literal before the guarded marshal", wasSilent: true,
			want: `is guarded by a json.Marshal of map[string]any{"a": args}, which is a composite literal reading identifiers`,
			mutate: func(t *testing.T, src string) string {
				return replaceArgsMarshal(t, src, "\tencodedArgs, err := json.Marshal(map[string]any{\"a\": args})")
			},
			rows: withoutProvenanceTest,
		},
		{
			// The keying switch's fallthrough refusal, which no other row reaches:
			// the two untraceable rows in the table hit the depth guard and the
			// conversion guard instead. A channel receive is an expression kind
			// the switch does not name, so it is the one site that reads the
			// fallthrough's own words.
			name: "a refusal whose expression kind the keying switch does not name", wasSilent: true,
			want: "which the census cannot trace to a construction",
			mutate: func(t *testing.T, src string) string {
				src = spliceBefore(t, src, "const mcpSessionHeader", "var probeErrCh = make(chan error, 1)\n\n")
				return spliceBefore(t, src, "\tif envelope.Error != nil {",
					`	if len(raw) == 291 {
		return "", <-probeErrCh
	}
`)
			},
		},
	}
}

// probeWideErrorType is the declaration the wide-result rows splice in: an
// interface that EMBEDS error and asks for one more method, and a type filling
// it. A result of that interface type is filled by refusals exactly as an `error`
// result is, which is what the identity test could not see.
const probeWideErrorType = `
type probeFailure interface {
	error
	ProbeCode() int
}

type probeWideError struct{ msg string }

func (e *probeWideError) Error() string  { return e.msg }
func (e *probeWideError) ProbeCode() int { return 7 }
`

// TestRefusalCensusDoesNotInstructDroppingAProvenanceTest is the other half of
// the laundering row above, and it asserts a SILENCE rather than a red.
//
// Under the shape-reading exemption the census did not merely miss the wrap: with
// the provenance test KEPT it complained, and the complaint NAMED the removal as
// the remedy — "so no caller-provenance test is needed: drop valueProvenanceBy".
// An author wrapping a marshaled value for any unrelated reason was told by the
// instrument to delete the coupling that made the unreachability claim honest.
// Reading the literal's contents makes the wrap a value the census will not vouch
// for, so the row that keeps its provenance test is simply correct and the census
// says nothing at all.
func TestRefusalCensusDoesNotInstructDroppingAProvenanceTest(t *testing.T) {
	source, err := censusReadSource(routeSourceFile)
	require.NoError(t, err)
	tests := packageTestFuncs(t)

	mutated := replaceArgsMarshal(t, string(source), "\tencodedArgs, err := json.Marshal(map[string]any{\"a\": args})")

	complaints, arms, err := auditRefusalCensus([]byte(mutated), refusalRows, tests)
	require.NoError(t, err)
	require.NotEmpty(t, arms, "control: the mutated copy must still enumerate, or the silence below is vacuous")
	assert.Empty(t, complaints, "wrapping a marshaled value is legal and the row keeps its provenance test, so the census has nothing to say")
	assert.NotContains(t, strings.Join(complaints, "\n"), "drop valueProvenanceBy",
		"the census must never name deleting a provenance test as the remedy for a value it cannot see into")
}

// TestRefusalCensusRefusesAnIndirectWriteOnlyWhereItCanReachANamedResult observes
// the NARROWING on the indirect-write refusal: it fires inside a function that
// declares a named error result and nowhere else.
//
// The refusal is deliberately coarse — it cannot say which result a pointer
// write reaches, so it refuses by line — and without the narrowing it would red a
// file whose author wrote an error through a pointer into a local, which reaches
// no result at all. Both legs run in the same call, so the silence is the
// narrowing and not a census that says nothing.
func TestRefusalCensusRefusesAnIndirectWriteOnlyWhereItCanReachANamedResult(t *testing.T) {
	source, err := censusReadSource(routeSourceFile)
	require.NoError(t, err)
	tests := packageTestFuncs(t)

	// THE KNOWN POSITIVE: the same statement where the pointer holds a named
	// error result's address is refused by line.
	positive, _, err := auditRefusalCensus([]byte(string(source)+`
func probeReachableIndirect(n int) (err error) {
	p := &err
	if n == 7 {
		*p = fmt.Errorf("check run: probe: an indirect write into a named result for %d", n)
	}
	return
}
`), refusalRows, tests)
	require.NoError(t, err)
	require.Contains(t, strings.Join(positive, "\n"), "writes into *p, an error this walk cannot resolve to a name",
		"control: an indirect write that can reach a named error result must be refused, or the silence below proves nothing")

	// AND THE SILENCE: no result of this function is a named error, so the write
	// reaches none and refusing it would red correct code.
	silent, arms, err := auditRefusalCensus([]byte(string(source)+`
func probeUnreachableIndirect(n int) int {
	var local error
	p := &local
	if n == 7 {
		*p = fmt.Errorf("check run: probe: an indirect write into a local for %d", n)
	}
	if local != nil {
		return 1
	}
	return 0
}
`), refusalRows, tests)
	require.NoError(t, err)
	require.NotEmpty(t, arms, "control: the mutated copy must still enumerate")
	assert.Empty(t, silent, "an indirect write in a function declaring no named error result reaches no result of it")
}

// TestRefusalCensusExemptsOnlyALiteralWhoseLeavesAreConstants observes the other
// half of the contents-reading rule: a literal is exempt for what it READS, so a
// struct literal of constants owes no provenance test while the same literal
// carrying one identifier does.
//
// The field-name half is the part a reader will doubt: a struct literal's keys
// are FIELD NAMES rather than values, and reading them as values would make every
// struct literal open and demand a provenance test for a value no caller can
// touch. That is why the exemption decides from the literal's TYPE.
func TestRefusalCensusExemptsOnlyALiteralWhoseLeavesAreConstants(t *testing.T) {
	source, err := censusReadSource(routeSourceFile)
	require.NoError(t, err)
	tests := packageTestFuncs(t)

	// The args row's provenance test is what a constant-leaf literal must make
	// unnecessary, so it is stripped for both legs and nothing else is touched.
	rows := append([]refusalRow(nil), refusalRows...)
	for i := range rows {
		if rows[i].construction == "check run: encode the %s arguments: %w" {
			rows[i].valueProvenanceBy = ""
		}
	}

	constant := replaceArgsMarshal(t, string(source), "\tencodedArgs, err := json.Marshal(kgtools.RPCError{Code: 7, Message: \"probe\"})")
	complaints, arms, err := auditRefusalCensus([]byte(constant), rows, tests)
	require.NoError(t, err)
	require.NotEmpty(t, arms, "control: the mutated copy must still enumerate")
	assert.Empty(t, complaints, "a struct literal whose leaves are constants carries nothing a caller could have put there, "+
		"so its row owes no provenance test — and its field NAMES are not leaves")

	// THE KNOWN POSITIVE, same shape and one identifier different.
	open := replaceArgsMarshal(t, string(source), "\tencodedArgs, err := json.Marshal(kgtools.RPCError{Code: 7, Message: tool})")
	complaints, _, err = auditRefusalCensus([]byte(open), rows, tests)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(complaints, "\n"), "which is a composite literal reading identifiers",
		"control: one identifier leaf must make the same literal open, or the exemption above is not reading leaves at all")
}

// TestRefusalCensusDoesNotCountARangeThatDeclaresItsOwnVariables is the test the
// deleted `rng.Tok != token.ASSIGN` guard would have been, written where it can
// actually fail.
//
// The guard was redundant: a `:=` range declares FRESH variables, whose objects
// are not the ones the named-result set holds, so the membership test excludes
// them already — and killing the guard left the whole suite green. That argument
// is the thing under test here, with both forms in the same call: the assigning
// range is refused and the declaring one, spelled one character apart, is not.
func TestRefusalCensusDoesNotCountARangeThatDeclaresItsOwnVariables(t *testing.T) {
	source, err := censusReadSource(routeSourceFile)
	require.NoError(t, err)
	tests := packageTestFuncs(t)

	assigning, _, err := auditRefusalCensus([]byte(string(source)+`
func probeAssigningRange(errs []error) (err error) {
	for _, err = range errs {
	}
	return
}
`), refusalRows, tests)
	require.NoError(t, err)
	require.Contains(t, strings.Join(assigning, "\n"), "writes an element of errs into the named error result err",
		"control: the assigning form must be refused, or the silence below proves nothing")

	declaring, arms, err := auditRefusalCensus([]byte(string(source)+`
func probeDeclaringRange(errs []error) error {
	for _, e := range errs {
		_ = e
	}
	return nil
}
`), refusalRows, tests)
	require.NoError(t, err)
	require.NotEmpty(t, arms, "control: the mutated copy must still enumerate")
	assert.Empty(t, declaring, "a `:=` range binds variables of its own, which are not the function's named result")
}
