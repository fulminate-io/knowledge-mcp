// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_daemon_route_census_shapes_types_test.go — THE SHAPES THAT BEAT THE
// CONSTRUCTOR WHITELIST.
//
// The census before this one keyed a refusal on a two-name whitelist, fmt.Errorf
// and errors.New, and refused only other calls whose selector package was
// literally "errors". A reviewer drove six shapes past it, each an ordinary line
// a Go author would write without a second thought: a package-local constructor
// returning a typed error, a composite literal of that type returned directly,
// one sentinel returned from two sites, a second import alias of fmt, a deferred
// func literal setting a named result, and a wrapping helper in the same file.
// Every one of them added a refusal that could then be deleted with the whole
// suite green.
//
// Each is a row here, asserted RED through the same auditRefusalCensus and the
// same in-run control as the shipped table. Two more rows carry the classes the
// type-directed enumeration made expressible for the first time: an error
// flowing in from a PARAMETER, which is a refusal by line because its
// construction is outside the file; and the round-4 provenance laundering, an
// empty map literal filled from the caller's parameter in a copy loop, which the
// old derivation classified as the function's own value and which the census now
// answers by demanding the provenance test whatever the identifier is.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// probeErrorType is the declaration the typed-error rows splice in: a named type
// with an Error method, which is what makes its composite literal a legal error
// value and therefore a refusal the census must see.
const probeErrorType = `
type probeRouteError struct{ msg string }

func (e *probeRouteError) Error() string { return e.msg }
`

// probeErrorCtor adds the package-local constructor whose CALL is the shape a
// constructor whitelist walked past: nothing at the call site says "error".
const probeErrorCtor = `
func newProbeRouteError(msg string) error { return &probeRouteError{msg: msg} }
`

func censusTypedShapeCases() []censusShapeCase {
	return []censusShapeCase{
		{
			name: "a call to a package-local constructor returning a typed error", wasSilent: true,
			want: `hands back an error this census does not declare (a call into this package, from newProbeRouteError(`,
			mutate: func(t *testing.T, src string) string {
				src += probeErrorType + probeErrorCtor
				return spliceBefore(t, src, "\tif envelope.Error != nil {",
					`	if len(raw) == 299 {
		return "", newProbeRouteError("check run: probe: a typed refusal from a local constructor")
	}
`)
			},
		},
		{
			name: "a composite literal of a local error type returned directly", wasSilent: true,
			want: `hands back an error this census does not declare (a composite literal of an error type, from &probeRouteError{`,
			mutate: func(t *testing.T, src string) string {
				src += probeErrorType
				return spliceBefore(t, src, "\tif envelope.Error != nil {",
					`	if len(raw) == 298 {
		return "", &probeRouteError{msg: "check run: probe: a composite-literal refusal"}
	}
`)
			},
		},
		{
			// The class the construction-counting census could not express at
			// all: it counted CONSTRUCTIONS, so one sentinel declared once and
			// returned twice was one row, and the second site was observed by
			// nothing and could be deleted green. Sites are counted here, so the
			// two collide.
			name: "one package sentinel returned from two sites", wasSilent: true,
			want: "two refusal sites carry one construction",
			mutate: func(t *testing.T, src string) string {
				src = spliceBefore(t, src, "\t\"context\"", "\t\"errors\"\n")
				src = spliceBefore(t, src, "const mcpSessionHeader",
					"var errProbeTwice = errors.New(\"check run: probe: a sentinel returned from two sites\")\n\n")
				src = spliceBefore(t, src, "\tsession := resp.Header.Get(mcpSessionHeader)",
					`	if resp.StatusCode == 299 {
		return "", errProbeTwice
	}
`)
				return spliceBefore(t, src, "\traw, err := io.ReadAll(resp.Body)",
					`	if resp.StatusCode == 298 {
		return "", errProbeTwice
	}
`)
			},
			rows: func(rows []refusalRow) []refusalRow {
				return append(rows, refusalRow{
					construction: "the package-level sentinel errProbeTwice",
					observedBy:   "TestCheckRun_RefusesARefusedHandshake",
				})
			},
		},
		{
			// A whitelist keyed on the selector text "fmt.Errorf" sees nothing
			// here; go/types resolves the alias to the same *types.Func.
			name: "a refusal built through a second import alias of fmt", wasSilent: true, want: undeclared,
			mutate: func(t *testing.T, src string) string {
				src = spliceBefore(t, src, "\t\"context\"", "\txfmt \"fmt\"\n")
				return spliceBefore(t, src, "\tsession := resp.Header.Get(mcpSessionHeader)",
					`	if resp.StatusCode == 299 {
		return "", xfmt.Errorf("check run: probe: an aliased-fmt refusal at %s", endpoint)
	}
`)
			},
		},
		{
			// A deferred literal that sets its caller's named result refuses
			// through an ASSIGNMENT, with no error expression in any return.
			name: "a deferred func literal setting a named error result", wasSilent: true, want: undeclared,
			mutate: func(_ *testing.T, src string) string {
				return src + `
func probeDeferredRefusal(n int) (err error) {
	defer func() {
		if err == nil && n == 7 {
			err = fmt.Errorf("check run: probe: a deferred refusal for %d", n)
		}
	}()
	return nil
}
`
			},
		},
		{
			name: "a wrapping helper declared in the same file", wasSilent: true, want: undeclared,
			mutate: func(_ *testing.T, src string) string {
				return src + `
func probeWrapRefusal(cause error) error {
	return fmt.Errorf("check run: probe: a same-file wrapping helper: %w", cause)
}
`
			},
		},
		{
			// The class the enumeration REFUSES rather than keys: the value was
			// built by a caller, so there is nothing in this file to declare.
			name: "an error that flows in from a parameter", wasSilent: true,
			want: "flows in from a parameter of probePassthroughRefusal",
			mutate: func(_ *testing.T, src string) string {
				return src + `
func probePassthroughRefusal(cause error) error {
	if cause != nil {
		return cause
	}
	return nil
}
`
			},
		},
		{
			// THE ROUND-4 LAUNDERING. An empty composite literal filled from the
			// caller's parameter in a copy loop read as "a value built earlier in
			// the same function", and the row could then drop its provenance
			// test while the marshaled bytes were entirely caller-controlled. The
			// derivation that classified it is gone: an identifier is an
			// identifier, and its row owes the provenance test.
			name: "a parameter copied into a local literal before the guarded marshal", wasSilent: true,
			want: "is guarded by a json.Marshal of localArgs, which is an identifier",
			mutate: func(t *testing.T, src string) string {
				return replaceArgsMarshal(t, src,
					`	localArgs := map[string]any{}
	for k, v := range args {
		localArgs[k] = v
	}
	encodedArgs, err := json.Marshal(localArgs)`)
			},
			rows: withoutProvenanceTest,
		},

		// THE REFUSAL BRANCHES, one row each. Every one of these was written as a
		// guard and would otherwise be unobserved: killing it would leave the
		// whole suite green, which is exactly what this census exists to forbid.
		{
			name: "a refusal handed on through a second local", want: "which the census cannot trace to a construction",
			mutate: func(t *testing.T, src string) string {
				return spliceBefore(t, src, "\tif envelope.Error != nil {",
					`	if len(raw) == 297 {
		first := fmt.Errorf("check run: probe: a refusal handed through two locals")
		second := first
		return "", second
	}
`)
			},
		},
		{
			name: "a refusal built by a conversion to error", want: "which the census cannot trace to a construction",
			mutate: func(t *testing.T, src string) string {
				return spliceBefore(t, src, "\tif envelope.Error != nil {",
					`	if len(raw) == 296 {
		return "", error(io.EOF)
	}
`)
			},
		},
		{
			name: "a refusal built by a call through a func-typed variable", want: "whose callee the census cannot name",
			mutate: func(t *testing.T, src string) string {
				src = spliceBefore(t, src, "const mcpSessionHeader",
					"var probeMakeRefusal = func(s string) error { return fmt.Errorf(\"check run: probe: %s\", s) }\n\n")
				return spliceBefore(t, src, "\tif envelope.Error != nil {",
					`	if len(raw) == 295 {
		return "", probeMakeRefusal("a refusal through a func-typed variable")
	}
`)
			},
		},
		{
			name: "a local the function assigns in two places", want: "assigns in 2 places, so the census cannot say which construction reaches here",
			mutate: func(t *testing.T, src string) string {
				return spliceBefore(t, src, "\tif envelope.Error != nil {",
					`	if len(raw) == 294 {
		probeErr := fmt.Errorf("check run: probe: the first construction")
		if tool == "impossible" {
			probeErr = fmt.Errorf("check run: probe: the second construction")
		}
		return "", probeErr
	}
`)
			},
		},
		{
			name: "a local declared and never assigned", want: "which this walk cannot see assigned anywhere in decodeToolText",
			mutate: func(t *testing.T, src string) string {
				return spliceBefore(t, src, "\tif envelope.Error != nil {",
					`	if len(raw) == 293 {
		var probeUnassigned error
		return "", probeUnassigned
	}
`)
			},
		},
		{
			name: "a method handing back its own receiver", want: "flows in from a parameter of probeSelfRefusal",
			mutate: func(_ *testing.T, src string) string {
				return src + probeErrorType + `
func (e *probeRouteError) probeSelfRefusal() error {
	if e.msg != "" {
		return e
	}
	return nil
}
`
			},
		},
		{
			// Not a refusal but a KEY: a sentinel declared in another package is
			// still a refusal this file hands back, and belongs in the table.
			name: "a sentinel declared in another package", want: `(a package-level sentinel, from io.EOF)`,
			mutate: func(t *testing.T, src string) string {
				return spliceBefore(t, src, "\tif envelope.Error != nil {",
					`	if len(raw) == 292 {
		return "", io.EOF
	}
`)
			},
		},
	}
}

// censusArgsMarshalLine is the one line the provenance mutations replace: the
// guarded encode of callDaemonTool's `args` parameter, which is the site every
// laundering probe has had to go through.
const censusArgsMarshalLine = "\tencodedArgs, err := json.Marshal(args)"

// replaceArgsMarshal swaps that line, which must appear EXACTLY ONCE, so a
// mutation cannot silently land in the wrong function when the source moves.
func replaceArgsMarshal(t *testing.T, src, replacement string) string {
	t.Helper()
	require.Equal(t, 1, strings.Count(src, censusArgsMarshalLine),
		"the anchor %q must appear exactly once in %s", censusArgsMarshalLine, routeSourceFile)
	return strings.Replace(src, censusArgsMarshalLine, replacement, 1)
}
